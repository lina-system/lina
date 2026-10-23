// SPDX-License-Identifier: Apache-2.0

// Command generate emits the Go client protocol from its canonical JSON Schema.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"sort"
	"strings"

	"github.com/thisisjun786/lina/kit/protocol/internal/schema"
)

func main() {
	input := flag.String("schema", "../../protocol/schema/client/1.0/envelope.json", "canonical schema")
	output := flag.String("output", "client.gen.go", "generated Go file")
	check := flag.Bool("check", false, "compare without writing")
	flag.Parse()
	data, err := os.ReadFile(*input)
	if err == nil {
		data, err = generate(data)
	}
	if err == nil && *check {
		var current []byte
		current, err = os.ReadFile(*output)
		if err == nil && !bytes.Equal(current, data) {
			err = fmt.Errorf("%s differs from regenerated types; run go generate ./kit/protocol", *output)
		}
	} else if err == nil {
		err = os.WriteFile(*output, data, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func name(value string) string {
	var out strings.Builder
	for _, part := range strings.Split(value, "_") {
		switch part {
		case "id", "ids", "url", "uri", "api", "utc":
			if part == "ids" {
				out.WriteString("IDs")
			} else {
				out.WriteString(strings.ToUpper(part))
			}
		default:
			if part != "" {
				out.WriteString(strings.ToUpper(part[:1]) + part[1:])
			}
		}
	}
	return out.String()
}

func keys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func generate(data []byte) ([]byte, error) {
	if bytes.ContainsRune(data, '`') {
		return nil, fmt.Errorf("schema contains a backtick; cannot embed it as a Go raw string")
	}
	root, err := schema.Parse(data)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintln(&b, "// SPDX-License-Identifier: Apache-2.0\n\n// Code generated from protocol/schema/client/1.0/envelope.json; DO NOT EDIT.\n\npackage protocol\n\nimport (\"encoding/json\"; \"fmt\")")
	var goType func(*schema.Node) (string, error)
	goType = func(n *schema.Node) (string, error) {
		if n.GoType != "" {
			return n.GoType, nil
		}
		if n.Ref != "" {
			key := strings.TrimPrefix(n.Ref, "#/$defs/")
			def := root.Defs[key]
			if def.Type == "object" || len(def.Enum) != 0 || len(def.OneOf) != 0 {
				return name(key), nil
			}
			return goType(def)
		}
		if len(n.AnyOf) == 2 && n.AnyOf[1].Type == "null" {
			value, err := goType(n.AnyOf[0])
			return "*" + value, err
		}
		switch n.Type {
		case "string":
			return "string", nil
		case "integer", "number":
			return "json.Number", nil
		case "boolean":
			return "bool", nil
		case "array":
			if n.Items == nil {
				return "", fmt.Errorf("array needs items")
			}
			item, err := goType(n.Items)
			return "[]" + item, err
		case "object":
			if n.AdditionalProperties != nil && n.AdditionalProperties.Schema != nil && len(n.Properties) == 0 {
				item, err := goType(n.AdditionalProperties.Schema)
				return "map[string]" + item, err
			}
		case "":
			if len(n.Const) != 0 {
				var value string
				if json.Unmarshal(n.Const, &value) == nil {
					return "string", nil
				}
			}
		}
		return "", fmt.Errorf("schema needs a named supported Go type: %+v", n)
	}
	emitObject := func(typeName string, n *schema.Node, envelope bool) error {
		if n.AdditionalProperties == nil || n.AdditionalProperties.Allowed || n.AdditionalProperties.Schema != nil {
			return fmt.Errorf("%s must be a closed object to preserve every field", typeName)
		}
		fmt.Fprintf(&b, "type %s struct {\n", typeName)
		for _, key := range keys(n.Properties) {
			property := n.Properties[key]
			typ := ""
			var err error
			if envelope && key == "payload" {
				typ = "P"
			} else if envelope && key == "kind" {
				typ = "Kind"
			} else {
				typ, err = goType(property)
			}
			if err != nil {
				return fmt.Errorf("%s.%s: %w", typeName, key, err)
			}
			optional := true
			for _, required := range n.Required {
				if required == key {
					optional = false
				}
			}
			tag := key
			if optional {
				if strings.HasPrefix(typ, "*") {
					return fmt.Errorf("%s.%s: optional nullable fields need a presence-aware type", typeName, key)
				}
				typ = "*" + typ
				tag += ",omitempty"
			}
			fmt.Fprintf(&b, "%s %s `json:%q`\n", name(key), typ, tag)
		}
		fmt.Fprintln(&b, "}")
		return nil
	}
	emitEnum := func(typeName string, values []string, prefix string) {
		if prefix == "" {
			prefix = typeName
		}
		fmt.Fprintf(&b, "type %s string\nconst (\n", typeName)
		for _, value := range values {
			fmt.Fprintf(&b, "%s%s %s = %q\n", prefix, name(value), typeName, value)
		}
		fmt.Fprintln(&b, ")")
	}
	kind := root.Properties["kind"]
	if kind == nil || len(kind.Enum) == 0 {
		return nil, fmt.Errorf("envelope needs a closed kind list")
	}
	emitEnum("Kind", kind.Enum, "Kind")
	payloads := map[string]string{}
	for _, rule := range root.AllOf {
		if rule.If == nil || rule.Then == nil {
			continue
		}
		if discriminator := rule.If.Properties["kind"]; discriminator != nil && len(discriminator.Const) != 0 {
			var kind string
			if err := json.Unmarshal(discriminator.Const, &kind); err != nil {
				return nil, err
			}
			payload := rule.Then.Properties["payload"]
			if payload == nil {
				continue
			}
			if payload.Ref == "" {
				return nil, fmt.Errorf("%s needs a payload reference", kind)
			}
			payloads[kind] = strings.TrimPrefix(payload.Ref, "#/$defs/")
		}
	}
	for _, kind := range kind.Enum {
		if payloads[kind] == "" {
			return nil, fmt.Errorf("kind %s has no payload schema", kind)
		}
	}
	for _, key := range keys(root.Defs) {
		n := root.Defs[key]
		typeName := name(key)
		if len(n.Enum) != 0 {
			emitEnum(typeName, n.Enum, n.GoPrefix)
			continue
		}
		if len(n.OneOf) != 0 {
			fmt.Fprintf(&b, "type %s struct {\n", typeName)
			variants := make([]string, len(n.OneOf))
			for i, variant := range n.OneOf {
				typ, err := goType(variant)
				if err != nil {
					return nil, err
				}
				variants[i] = name(variant.Type)
				if variant.Ref != "" {
					variants[i] = name(strings.TrimPrefix(variant.Ref, "#/$defs/"))
				}
				if variants[i] == "" {
					return nil, fmt.Errorf("union variant needs a type or reference")
				}
				fmt.Fprintf(&b, "%s *%s\n", variants[i], typ)
			}
			fmt.Fprintln(&b, "}")
			fmt.Fprintf(&b, "func (v %s) MarshalJSON() ([]byte, error) { var value any; count := 0\n", typeName)
			for _, field := range variants {
				fmt.Fprintf(&b, "if v.%s != nil { value = v.%s; count++ }\n", field, field)
			}
			fmt.Fprintln(&b, "if count != 1 { return nil, fmt.Errorf(\"union requires exactly one value\") }; return json.Marshal(value) }")
			fmt.Fprintf(&b, "func (v *%s) UnmarshalJSON(data []byte) error {\n", typeName)
			for i, variant := range n.OneOf {
				typ, _ := goType(variant)
				fmt.Fprintf(&b, "if clientSchema.Defs[%q].OneOf[%d].Validate(data, clientSchema) == nil { var value %s; if err := json.Unmarshal(data, &value); err != nil { return err }; *v = %s{%s: &value}; return nil }\n", key, i, typ, typeName, variants[i])
			}
			fmt.Fprintln(&b, "return fmt.Errorf(\"invalid union value\") }")
			continue
		}
		if n.Type != "object" {
			continue
		}
		if n.AdditionalProperties != nil && n.AdditionalProperties.Schema != nil && len(n.Properties) == 0 {
			typ, err := goType(n.AdditionalProperties.Schema)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&b, "type %s map[string]%s\n", typeName, typ)
		} else {
			if n.Comment != "" {
				fmt.Fprintf(&b, "// %s: %s\n", typeName, strings.ReplaceAll(n.Comment, "\n", " "))
			}
			if err := emitObject(typeName, n, false); err != nil {
				return nil, err
			}
		}
	}
	fmt.Fprintln(&b, "// Payload is the closed set of client 1.0 payload types.\ntype Payload interface { isPayload() }")
	seen := map[string]bool{}
	for _, kind := range kind.Enum {
		typ := name(payloads[kind])
		if !seen[typ] {
			fmt.Fprintf(&b, "func (%s) isPayload() {}\n", typ)
			seen[typ] = true
		}
	}
	if err := emitObject("message[P any]", root, true); err != nil {
		return nil, err
	}
	fmt.Fprintln(&b, "// Envelope preserves required nulls and arrays; its codec validates the schema.\ntype Envelope message[Payload]")
	fmt.Fprintln(&b, "func decodePayload(kind Kind, data json.RawMessage) (Payload, error) { switch kind {")
	for _, kind := range kind.Enum {
		fmt.Fprintf(&b, "case Kind%s: var p %s; err := json.Unmarshal(data, &p); return p, err\n", name(kind), name(payloads[kind]))
	}
	b.WriteString("default: return nil, fmt.Errorf(\"unknown kind %q\", kind) } }\n")
	fmt.Fprintln(&b, "func decodeMessage(data []byte) (Envelope, error) { var wire message[json.RawMessage]; if err := json.Unmarshal(data, &wire); err != nil { return Envelope{}, err }; payload, err := decodePayload(wire.Kind, wire.Payload); if err != nil { return Envelope{}, err }; return Envelope{")
	for _, key := range keys(root.Properties) {
		field := name(key)
		if key == "payload" {
			fmt.Fprintln(&b, "Payload: payload,")
		} else {
			fmt.Fprintf(&b, "%s: wire.%s,\n", field, field)
		}
	}
	fmt.Fprintln(&b, "}, nil }")
	fmt.Fprintf(&b, "\nconst clientSchemaJSON = `%s`\n", data)
	source, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated types: %w", err)
	}
	return source, nil
}
