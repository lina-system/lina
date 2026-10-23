// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtempSync, cpSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { check, generate, loadTokens, contrast, lintGo } from './tokens.mjs';

const root = resolve(import.meta.dirname, '..');
function fixture(t) {
  const dir = mkdtempSync(join(tmpdir(), 'lina-tokens-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  cpSync(join(root, 'design/tokens'), join(dir, 'design/tokens'), { recursive: true });
  cpSync(join(root, 'DESIGN.md'), join(dir, 'DESIGN.md'));
  return dir;
}

test('committed outputs regenerate exactly and all theme pairs pass AA', () => {
  assert.deepEqual(check(root), []);
  assert.equal(contrast('#000000', '#ffffff'), 21);
  assert.equal(contrast('#ffffff', '#ffffff'), 1);
});

test('every theme has the same complete role set', () => {
  const themes = loadTokens(root);
  assert.equal(themes.length, 3);
  const paths = Object.keys(themes[0].tokens).sort();
  for (const theme of themes) assert.deepEqual(Object.keys(theme.tokens).sort(), paths);
  for (const family of ['color', 'type', 'space', 'radius', 'motion', 'glyph', 'icon']) {
    assert.ok(paths.some(path => path.startsWith(`${family}.`)));
  }
});

test('drift in Go, CSS, Swift, the SVG mark and the documentation tables fails without rewriting', t => {
  const dir = fixture(t);
  generate(dir);
  for (const [file, before, after] of [
    ['internal/design/tokens_gen.go', '#2F4BDB', '#2244dd'],
    ['design/tokens/tokens.css', '#2F4BDB', '#2244dd'],
    ['design/tokens/tokens.swift', '0x2F4BDB', '0x2244DD'],
    ['design/tokens/mark.svg', '#2F4BDB', '#2244dd'],
    ['DESIGN.md', '#2F4BDB', '#2244dd'],
  ]) {
    const path = join(dir, file);
    const original = readFileSync(path, 'utf8');
    const changed = original.replaceAll(before, after);
    assert.notEqual(original, changed, file);
    writeFileSync(path, changed);
    assert.ok(check(dir).some(error => error.includes(file)), file);
    assert.equal(readFileSync(path, 'utf8'), changed);
    writeFileSync(path, original);
  }
});

test('Swift output has dynamic AppKit colors and layout constants from the source', t => {
  const dir = fixture(t);
  generate(dir);
  const swift = readFileSync(join(dir, 'design/tokens/tokens.swift'), 'utf8');
  assert.match(swift, /^\/\/ SPDX-License-Identifier: Apache-2\.0\n/);
  assert.match(swift, /import AppKit/);
  for (const role of ['canvas', 'surface', 'text', 'muted', 'accent', 'line']) {
    assert.match(swift, new RegExp(`static let ${role}: NSColor = dynamic\\(light: 0x[\\dA-F]{6}, dark: 0x[\\dA-F]{6}, highContrast: 0x[\\dA-F]{6}\\)`));
  }
  assert.match(swift, /light: 0xF8F9FC, dark: 0x17191F, highContrast: 0x000000/);
  assert.match(swift, /light: 0x2F4BDB, dark: 0x5685FF, highContrast: 0x6795FF/);
  for (const [name, value] of [['inline', '8'], ['gutter', '24'], ['section', '24'], ['page', '32']]) {
    assert.match(swift, new RegExp(`static let ${name}: CGFloat = ${value}\\b`));
  }
  assert.match(swift, /static let card: CGFloat = 12\b/);
  for (const [name, value] of [['settle', '0.16'], ['reduced', '0'], ['activityFrame', '0.1'], ['activityElapsed', '1']]) {
    assert.match(swift, new RegExp(`static let ${name}: TimeInterval = ${value}\\b`));
  }
  assert.match(swift, /accessibilityHighContrastAqua/);
  assert.match(swift, /accessibilityHighContrastDarkAqua/);
  assert.match(swift, /bestMatch\(from:/);
});

test('a light high-contrast set fails because the launcher needs a dark one', t => {
  const dir = fixture(t);
  const path = join(dir, 'design/tokens/high-contrast.json');
  const data = JSON.parse(readFileSync(path, 'utf8'));
  const set = (role, hex, ansi256, ansi16) => {
    const token = data.color[role];
    const components = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255);
    token.$value = { colorSpace: 'srgb', components, alpha: 1, hex };
    token.$extensions = { 'lina.terminal': { ansi256, ansi16 } };
  };
  set('canvas', '#FFFFFF', 231, 15);
  set('surface', '#FFFFFF', 231, 15);
  set('text', '#000000', 16, 0);
  set('muted', '#000000', 16, 0);
  set('accent', '#000080', 17, 4);
  set('line', '#000000', 16, 0);
  writeFileSync(path, JSON.stringify(data));
  assert.throws(() => generate(dir), /high-contrast.*dark/i);
});

test('low contrast, absent roles and missing terminal fallbacks fail closed', t => {
  for (const mutate of [
    data => { data.color.text.$value = data.color.canvas.$value; },
    data => { delete data.glyph.unicode.failed; },
    data => { delete data.color.accent.$extensions['lina.terminal']; },
    data => { data.color.accent.$extensions['lina.terminal'].ansi16 = 15; },
  ]) {
    const dir = fixture(t);
    const path = join(dir, 'design/tokens/light.json');
    const data = JSON.parse(readFileSync(path, 'utf8'));
    mutate(data);
    writeFileSync(path, JSON.stringify(data));
    assert.throws(() => generate(dir));
  }
});

test('TUI guard rejects raw color, ANSI colors and presentation glyphs', () => {
  for (const source of [
    'var c = "#2F4BDB"', 'var c = lipgloss.Color("12")',
    'var c = lipgloss.Color(12)', 'var c = lipgloss.Color("blue")',
    'var c = lipgloss.RGBColor(0x123456)', 'var c = lipgloss.Red',
    'var c = color.RGBA{R: 47, G: 75, B: 219}', 'var mark = "🟢"', 'var mark = "*"', 'var c = lipgloss.ANSIColor(4)',
    'var c = "\\x1b[34m"', 'var mark = "●"', 'var mark = "\\u25cf"',
    'var mark = \'✓\'', 'var mark = "->"',
  ]) assert.ok(lintGo(source).length, source);
  for (const source of [
    '// a comment may describe #2F4BDB or ●\nvar n = 16',
    'var c = lipgloss.Color(tokens.ColorAccent.Hex)',
    'var mark = tokens.GlyphUnicodeLive', 'var text = "연결됨"',
    'var wire = "\\x1b]52;c;payload\\a"',
  ]) assert.deepEqual(lintGo(source), [], source);
});
