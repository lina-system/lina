#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Deterministic schema-to-Go projection. No worker process or network is needed.
import { readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('.', import.meta.url));
const schema = JSON.parse(readFileSync(`${root}upstream/schema/v1/schema.json`, 'utf8'));
const definitions = new Map();
const refName = ref => ref.replace('#/$defs/', '').split('/').map(identifier).join('');
function identifier(value) {
  const name = value.replace(/[^a-zA-Z0-9]+/g, ' ').split(' ').filter(Boolean)
    .map(part => part[0].toUpperCase() + part.slice(1)).join('');
  return (/^\d/.test(name) ? `N${name}` : name).replace(/Id$/, 'ID').replace(/Url$/, 'URL');
}
for (const [name, node] of Object.entries(schema.$defs)) definitions.set(`#/$defs/${name}`, node);
const needed = new Set(definitions.keys());
// Resolve only this pinned document. Flatten schema composition, retaining tags.
function normalize(node) {
  if (node.$ref) {
    if (!definitions.has(node.$ref)) throw new Error(`unresolved ${node.$ref}`);
    return node;
  }
  const variants = node.oneOf ?? node.anyOf;
  if (variants?.every(v => v.type === 'integer' || v.type === 'string') && new Set(variants.map(v => v.type)).size === 1) {
    const result = { ...node, type: variants[0].type };
    if (result.type === 'string') result.enum = variants.filter(v => v.const !== undefined).map(v => v.const);
    if (!result.enum?.length) delete result.enum;
    delete result.oneOf; delete result.anyOf; return result;
  }
  if (variants && node.type === 'object') {
    const result = { ...node };
    delete result.type; delete result.properties; delete result.required;
    result[node.oneOf ? 'oneOf' : 'anyOf'] = variants.map(v => ({ ...v,
      properties: { ...node.properties, ...v.properties }, required: [...new Set([...(node.required ?? []), ...(v.required ?? [])])] }));
    return result;
  }
  if (!node.allOf) return node;
  if (!node.type && !node.properties && node.allOf.length === 1) return normalize(node.allOf[0]);
  let merged = { ...node }; delete merged.allOf;
  for (const part of node.allOf) {
    const base = part.$ref ? definitions.get(part.$ref) : part;
    if (!base || base.type !== 'object') throw new Error(`unsupported allOf composition: ${JSON.stringify(part)}`);
    merged = { ...base, ...merged, properties: { ...base.properties, ...merged.properties },
      required: [...new Set([...(base.required ?? []), ...(merged.required ?? [])])] };
  }
  return normalize(merged);
}
const declarations = new Map();
function declare(name, node) {
  node = normalize(node);
  if (declarations.has(name)) {
    if (JSON.stringify(declarations.get(name)) !== JSON.stringify(node)) throw new Error(`name collision: ${name}`);
  } else declarations.set(name, node);
  return name;
}
for (const ref of [...needed].sort()) declare(refName(ref), definitions.get(ref));

const pointer = type => type.startsWith('*') ? type : `*${type}`;
function goType(node, name) {
  node = normalize(node);
  if (Object.hasOwn(node, "const")) node = { ...node, enum: [node.const] };
  if (node === true || (node && Object.keys(node).length === 0)) return 'json.RawMessage';
  if (!node || typeof node !== 'object') throw new Error(`unsupported schema ${name}`);
  if (node.$ref) return refName(node.$ref);
  if (node.allOf?.length === 1) return goType(node.allOf[0], name);
  if (Array.isArray(node.type)) {
    const types = node.type.filter(type => type !== 'null');
    if (types.length === 1 && node.type.includes('null')) return pointer(goType({ ...node, type: types[0] }, name));
    throw new Error(`unsupported type union ${name}`);
  }
  const variants = node.anyOf ?? node.oneOf;
  if (variants) {
    const nonnull = variants.filter(v => v.type !== 'null');
    if (nonnull.length === 1 && nonnull.length !== variants.length) return pointer(goType(nonnull[0], name));
    return declare(name, node);
  }
  if (node.enum?.every(value => typeof value === 'string')) return declare(name, node);
  switch (node.type) {
    case 'string': return 'string';
    case 'boolean': return 'bool';
    case 'integer': return node.format?.startsWith('uint') ? 'uint64' : 'int64';
    case 'number': return 'float64';
    case 'null': return 'json.RawMessage';
    case 'array': return `[]${goType(node.items, `${name}Item`)}`;
    case 'object':
      if (node.properties && Object.keys(node.properties).length) return declare(name, node);
      return `map[string]${node.additionalProperties && node.additionalProperties !== true ? goType(node.additionalProperties, `${name}Value`) : 'json.RawMessage'}`;
    default:
      if (Object.keys(node).every(key => ['description', 'title', '$schema', 'default'].includes(key) || key.startsWith('x-'))) return 'json.RawMessage';
      throw new Error(`unsupported schema node ${name}: ${JSON.stringify(node)}`);
  }
}

let out = '// SPDX-License-Identifier: Apache-2.0\n\n// Code generated by generate.mjs from ACP schema-v1.26.0 JSON Schema. DO NOT EDIT.\n'
  + '// Package acp implements Agent Client Protocol version 1.\npackage acp\n\nimport ("encoding/json"; "fmt")\n\n';
const emit = text => { out += `${text}\n`; };
for (const [name, node] of declarations) {
  if (node.description) emit(`// ${name}: ${node.description.replaceAll('\n', '\n// ')}`);
  if (node.enum?.every(value => typeof value === 'string')) {
    emit(`type ${name} string\nconst (`);
    for (const value of node.enum) emit(`${name}${identifier(value) || 'Empty'} ${name} = ${JSON.stringify(value)}`);
    emit(')');
  } else if (node.type === 'object' && node.properties && Object.keys(node.properties).length) {
    emit(`type ${name} struct {`);
    for (const [key, property] of Object.entries(node.properties).sort(([a], [b]) => a.localeCompare(b, 'en'))) {
      const optional = !(node.required ?? []).includes(key);
      let type = goType(property, `${name}${identifier(key)}`);
      if (optional) type = pointer(type);
      emit(`${identifier(key)} ${type} \`json:"${key}${optional ? ',omitempty' : ''}"\``);
    }
    emit('}');
  } else if (node.anyOf || node.oneOf) {
    const variants = (node.anyOf ?? node.oneOf).map(normalize);
    const tag = ['sessionUpdate', 'outcome', 'type', 'method', 'action', 'mode'].find(key => variants.every(v => v.type === 'object'
      && v.properties?.[key]?.type === 'string' && v.required?.includes(key)) && variants.some(v => typeof v.properties?.[key]?.const === 'string' || v.properties?.[key]?.enum?.length === 1));
    if (!tag) {
      // Untagged unions (notably string/integer RequestId and arbitrary JSON)
      // keep their exact representation instead of guessing or rounding numbers.
      emit(`type ${name} = json.RawMessage`);
      for (let i = 0; i < variants.length; i++) {
        if (variants[i].type === 'object' && variants[i].properties) declare(`${name}Variant${i + 1}`, variants[i]);
      }
      continue;
    }
    const branches = variants.filter(v => v.properties[tag].const !== undefined || v.properties[tag].enum?.length === 1).map(v => {
      const value = v.properties[tag].const ?? v.properties[tag].enum[0];
      const field = identifier(value);
      return { value, field, type: declare(`${name}${field}`, v) };
    });
    if (new Set(branches.map(b => b.field)).size !== branches.length) throw new Error(`duplicate union variant ${name}`);
    emit(`type ${name} struct {`);
    for (const b of branches) emit(`${b.field} *${b.type} \`json:"-"\``);
    emit('Unrecognized json.RawMessage `json:"-"`\n}');
    emit(`func (v *${name}) UnmarshalJSON(data []byte) error {
      var tag struct { Value string \`json:"${tag}"\` }
      if err := json.Unmarshal(data, &tag); err != nil { return err }
      var next ${name}
      switch tag.Value {`);
    for (const b of branches) emit(`case ${JSON.stringify(b.value)}:
      next.${b.field} = new(${b.type})
      if err := json.Unmarshal(data, next.${b.field}); err != nil { return err }`);
    emit('default: next.Unrecognized = append(json.RawMessage(nil), data...)\n}\n*v = next\nreturn nil\n}');
    emit(`func (v ${name}) MarshalJSON() ([]byte, error) {
      var selected any
      count := 0`);
    for (const b of branches) emit(`if v.${b.field} != nil {
      copy := *v.${b.field}; copy.${identifier(tag)} = ${JSON.stringify(b.value)}
      selected = copy; count++
    }`);
    emit(`if v.Unrecognized != nil { selected = v.Unrecognized; count++ }
      if count != 1 { return nil, fmt.Errorf("${name}: expected one variant, got %d", count) }
      return json.Marshal(selected)
    }`);
  } else {
    const type = goType(node, name);
    emit(`type ${name} ${type === 'json.RawMessage' ? '= ' : ''}${type}`);
  }
  emit('');
}
const formatted = execFileSync('gofmt', [], { input: out, maxBuffer: 16 * 1024 * 1024 });
for (const target of [`${root}upstream/types.go`, `${root}../../kit/acp/types.gen.go`]) {
  if (process.argv.includes('--check')) {
    if (!readFileSync(target).equals(formatted)) throw new Error(`generated ACP types differ: ${target}`);
  } else writeFileSync(target, formatted);
}
console.log(`ACP schema: ${needed.size} definitions, ${declarations.size} Go declarations (${process.argv.includes('--check') ? 'checked' : 'generated'})`);
