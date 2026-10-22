#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
import { existsSync, readdirSync, readFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { join, dirname, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { pathToFileURL } from 'node:url';

const names = ['light', 'dark', 'high-contrast'];
const fields = {
  color: ['canvas', 'surface', 'text', 'muted', 'accent', 'line'],
  type: ['body', 'code', 'size', 'lineHeight', 'weight', 'titleWeight'],
  space: ['inline', 'gutter', 'section', 'page'], radius: ['card'],
  motion: ['settle', 'reduced', 'activityFrame', 'activityElapsed'], icon: ['size'],
};
const glyphs = ['mark', 'connecting', 'live', 'resyncing', 'retrying', 'refused', 'recovering', 'stopped', 'failed', 'quit', 'rule', 'action', 'waiting', 'unknown', 'activityFrames', 'activitySeparator', 'person', 'agent'];
const expected = [...Object.entries(fields).flatMap(([group, roles]) => roles.map(role => `${group}.${role}`)),
  ...['unicode', 'ascii'].flatMap(set => glyphs.map(role => `glyph.${set}.${role}`))].sort();
const identifier = path => path.split(/[.-]/).map(word => word[0].toUpperCase() + word.slice(1)).join('');
const cssName = path => path.replaceAll('.', '-').replace(/[A-Z]/g, c => `-${c.toLowerCase()}`);
const ansi16 = ['#000000', '#800000', '#008000', '#808000', '#000080', '#800080', '#008080', '#c0c0c0',
  '#808080', '#ff0000', '#00ff00', '#ffff00', '#0000ff', '#ff00ff', '#00ffff', '#ffffff'];
function terminalColor(n) {
  if (n < 16) return ansi16[n];
  const levels = [0, 95, 135, 175, 215, 255];
  const v = n - 16;
  const rgb = n >= 232 ? Array(3).fill(8 + (n - 232) * 10)
    : [levels[Math.floor(v / 36)], levels[Math.floor(v / 6) % 6], levels[v % 6]];
  return `#${rgb.map(n => n.toString(16).padStart(2, '0')).join('')}`;
}
export function contrast(a, b) {
  const luminance = hex => [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map(n => n <= 0.04045 ? n / 12.92 : ((n + 0.055) / 1.055) ** 2.4)
    .reduce((sum, n, i) => sum + n * [0.2126, 0.7152, 0.0722][i], 0);
  const x = luminance(a), y = luminance(b);
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}
function flatten(group, prefix = '', result = {}) {
  for (const [key, value] of Object.entries(group)) {
    if (key.startsWith('$')) continue;
    const path = prefix ? `${prefix}.${key}` : key;
    if ('$value' in value) result[path] = value;
    else flatten(value, path, result);
  }
  return result;
}
function validate(path, token) {
  const value = token.$value;
  const fail = () => { throw new Error(`Invalid token ${path}`); };
  switch (token.$type) {
    case 'color': {
      if (value.colorSpace !== 'srgb' || value.alpha !== 1 || !/^#[a-f\d]{6}$/i.test(value.hex)
        || value.components?.length !== 3 || value.components.some((v, i) => !Number.isFinite(v) || v < 0 || v > 1
          || Math.round(v * 255) !== parseInt(value.hex.slice(1 + i * 2, 3 + i * 2), 16))) fail();
      const fallback = token.$extensions?.['lina.terminal'];
      if (!fallback || !Number.isInteger(fallback.ansi16) || fallback.ansi16 < 0 || fallback.ansi16 > 15
        || !Number.isInteger(fallback.ansi256) || fallback.ansi256 < 0 || fallback.ansi256 > 255) fail();
      break;
    }
    case 'dimension':
    case 'duration':
      if (!Number.isFinite(value.value) || value.value < 0 || value.unit !== (token.$type === 'dimension' ? 'px' : 'ms')) fail();
      if (path.startsWith('space.') && (!Number.isInteger(token.$extensions?.['lina.cells']) || token.$extensions['lina.cells'] < 0)) fail();
      break;
    case 'fontFamily':
      if (!Array.isArray(value) || !value.length || value.some(v => typeof v !== 'string' || !v)) fail();
      break;
    case 'fontWeight':
    case 'number':
      if (!Number.isFinite(value) || value <= 0) fail();
      break;
    case 'string':
      if (typeof value !== 'string' || !value || /[\p{Co}\p{Cc}\p{Cf}]/u.test(value)
        || (path.startsWith('glyph.ascii.') && /[^\x20-\x7e]/.test(value))) fail();
      break;
    default: fail();
  }
  const type = path.startsWith('color.') ? 'color' : path.startsWith('glyph.') ? 'string'
    : path.startsWith('motion.') ? 'duration' : /^(space|radius|icon)\./.test(path) || path === 'type.size' ? 'dimension'
      : /type\.(body|code)$/.test(path) ? 'fontFamily' : path.endsWith('Weight') || path === 'type.weight' ? 'fontWeight' : 'number';
  if (type !== token.$type) fail();
}
export function loadTokens(root) {
  return names.map(name => {
    const data = JSON.parse(readFileSync(join(root, `design/tokens/${name}.json`), 'utf8'));
    if (data.$extensions?.['lina.theme'] !== name) throw new Error(`Invalid theme: ${name}`);
    const tokens = flatten(data);
    if (JSON.stringify(Object.keys(tokens).sort()) !== JSON.stringify(expected)) throw new Error(`Incomplete roles: ${name}`);
    for (const [path, token] of Object.entries(tokens)) validate(path, token);
    for (const foreground of ['text', 'muted', 'accent']) for (const background of ['canvas', 'surface']) {
      for (const profile of ['hex', 'ansi256', 'ansi16']) {
        const color = role => profile === 'hex' ? tokens[`color.${role}`].$value.hex
          : terminalColor(tokens[`color.${role}`].$extensions['lina.terminal'][profile]);
        const ratio = contrast(color(foreground), color(background));
        if (ratio < 4.5) throw new Error(`${name} ${profile}: ${foreground}/${background} contrast ${ratio.toFixed(2)} < 4.5`);
      }
    }
    return { name, tokens };
  });
}
function goType(token) {
  return { color: 'Color', dimension: 'Dimension', duration: 'float64', fontFamily: 'string',
    string: 'string', number: 'float64', fontWeight: 'int' }[token.$type];
}
function goValue(token) {
  const v = token.$value;
  switch (token.$type) {
    case 'color': return `Color{Hex: ${JSON.stringify(v.hex)}, ANSI256: ${token.$extensions['lina.terminal'].ansi256}, ANSI16: ${token.$extensions['lina.terminal'].ansi16}}`;
    case 'dimension': return `Dimension{PX: ${v.value}, Cells: ${token.$extensions?.['lina.cells'] ?? 0}}`;
    case 'duration': return `${v.value}`;
    case 'fontFamily': return JSON.stringify(v.join(', '));
    default: return JSON.stringify(v);
  }
}
function cssValue(token) {
  const v = token.$value;
  if (token.$type === 'color') return v.hex;
  if (token.$type === 'dimension' || token.$type === 'duration') return `${v.value}${v.unit}`;
  if (token.$type === 'fontFamily') return v.map(JSON.stringify).join(', ');
  return JSON.stringify(v);
}
function outputs(root) {
  const themes = loadTokens(root);
  let go = '// SPDX-License-Identifier: Apache-2.0\n// Code generated by scripts/tokens.mjs; DO NOT EDIT.\n\npackage design\n\n';
  go += 'type Color struct { Hex string; ANSI256, ANSI16 int }\ntype Dimension struct { PX float64; Cells int }\n\ntype Tokens struct {\n';
  go += expected.map(path => `${identifier(path)} ${goType(themes[0].tokens[path])}`).join('\n') + '\n}\n';
  for (const theme of themes) {
    go += `\nfunc ${identifier(theme.name)}() Tokens { return Tokens{\n`;
    go += expected.map(path => `${identifier(path)}: ${goValue(theme.tokens[path])},`).join('\n') + '\n} }\n';
  }
  const formatted = spawnSync('gofmt', [], { input: go, encoding: 'utf8' });
  if (formatted.status !== 0) throw new Error(`gofmt: ${formatted.stderr || formatted.error}`);
  const css = '/* SPDX-License-Identifier: Apache-2.0 */\n/* Generated by scripts/tokens.mjs; DO NOT EDIT. */\n' + themes.map(theme =>
    `${theme.name === 'light' ? ':root,\n' : ''}[data-lina-theme="${theme.name}"] {\n${expected.map(path => `  --lina-${cssName(path)}: ${cssValue(theme.tokens[path])};`).join('\n')}\n}\n`).join('\n');
  const table = '\nGenerated values; terminal indexes are 256 / 16. Spacing cells are independent\nof app pixels. Duration values are milliseconds.\n\n| Role | Light | Dark | High contrast |\n| --- | --- | --- | --- |\n'
    + expected.map(path => `| ${path} | ${themes.map(theme => {
      const token = theme.tokens[path];
      const fallback = token.$extensions?.['lina.terminal'];
      return `\`${cssValue(token)}\`${fallback ? ` (${fallback.ansi256} / ${fallback.ansi16})` : token.$extensions?.['lina.cells'] !== undefined ? ` (${token.$extensions['lina.cells']} cells)` : ''}`;
    }).join(' | ')} |`).join('\n') + '\n\n';
  const doc = readFileSync(join(root, 'DESIGN.md'), 'utf8');
  if (!/<!-- tokens:start -->[\s\S]*<!-- tokens:end -->/.test(doc)) throw new Error('Missing DESIGN.md token table markers');
  const light = themes[0].tokens;
  const mark = `<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Generated by scripts/tokens.mjs; DO NOT EDIT. -->
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 80 32" role="img" aria-label="LINA">
  <text x="0" y="25" font-family="${light['type.body'].$value.join(', ')}" font-size="24" font-weight="${light['type.titleWeight'].$value}" fill="var(--lina-color-text, ${light['color.text'].$value.hex})">lina</text>
  <circle cx="48" cy="22" r="2" fill="var(--lina-color-accent, ${light['color.accent'].$value.hex})"/>
</svg>
`;
  return new Map([
    ['internal/design/tokens_gen.go', formatted.stdout], ['design/tokens/tokens.css', css],
    ['design/tokens/mark.svg', mark],
    ['DESIGN.md', doc.replace(/(?<=<!-- tokens:start -->)[\s\S]*(?=<!-- tokens:end -->)/, table)],
  ]);
}
// Scan Go literals, not comments or ordinary Korean text. Tests and Unicode
// measurement data are not presentation code; only production literals are checked.
export function lintGo(source) {
  const failures = [];
  const lexical = /\/\/[^\n]*|\/\*[\s\S]*?\*\/|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`[^`]*`/g;
  const uncommented = source.replace(lexical, value => value.startsWith('//') || value.startsWith('/*') ? ' '.repeat(value.length) : value);
  for (const match of uncommented.matchAll(/"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|`[^`]*`/g)) {
    const literal = match[0].slice(1, -1).replace(/\\(?:u([a-f\d]{4})|U([a-f\d]{8})|x([a-f\d]{2})|([0-7]{3}))/gi,
      (_, u, U, x, oct) => String.fromCodePoint(parseInt(u || U || x || oct, oct ? 8 : 16)));
    if (/#[a-f\d]{3,8}\b/i.test(literal) || /\x1b\[[\d;:]*m/.test(literal)
      || /[\u2190-\u2bff\p{Co}\p{Extended_Pictographic}·›]/u.test(literal) || /^(?:->|<-|--+|\[\+\])$/.test(literal)) {
      failures.push(`line ${source.slice(0, match.index).split('\n').length}: raw colour or glyph`);
    }
  }
  if (/\b(?:Color|ANSIColor|RGBColor|RGBA|NRGBA|HexToRGB|BasicColor|IndexedColor|RGB)\s*(?:\(|\{)\s*(?:["'`\d]|0x)/.test(uncommented)) failures.push('literal colour constructor');
  if (/\.(?:Black|Red|Green|Yellow|Blue|Magenta|Cyan|White|BrightBlack|BrightRed|BrightGreen|BrightYellow|BrightBlue|BrightMagenta|BrightCyan|BrightWhite)\b/.test(uncommented)
    || /\b(?:RGBA|NRGBA|RGBA64|NRGBA64)\s*\{/.test(uncommented)) failures.push('raw colour value');
  if (/\b\w*(?:mark|glyph|icon|rule|separator)\w*\s*(?::=|=)\s*["'`][^\w\s"'`]+["'`]/i.test(uncommented)) failures.push('literal presentation glyph');
  return failures;
}
function goFiles(directory) {
  if (!existsSync(directory)) return [];
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    if (entry.name === 'testdata') return [];
    const path = join(directory, entry.name);
    return entry.isDirectory() ? goFiles(path) : entry.name.endsWith('.go') && !entry.name.endsWith('_test.go') ? [path] : [];
  });
}
export function check(root) {
  const failures = [];
  for (const [path, value] of outputs(root)) {
    if (!existsSync(join(root, path)) || readFileSync(join(root, path), 'utf8') !== value) failures.push(`Generated drift: ${path}`);
  }
  for (const path of goFiles(join(root, 'internal/tui'))) {
    failures.push(...lintGo(readFileSync(path, 'utf8')).map(error => `${path.slice(root.length + 1)}: ${error}`));
  }
  return failures;
}
export function generate(root) {
  for (const [path, value] of outputs(root)) {
    mkdirSync(dirname(join(root, path)), { recursive: true });
    writeFileSync(join(root, path), value);
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const root = resolve(import.meta.dirname, '..');
    if (process.argv.slice(2).some(arg => arg !== '--check')) throw new Error('Usage: node scripts/tokens.mjs [--check]');
    if (process.argv.includes('--check')) {
      const errors = check(root);
      if (errors.length) throw new Error(errors.join('\n'));
      console.log('tokens: generated files, contrast and TUI literals pass');
    } else generate(root);
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
