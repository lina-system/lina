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

test('drift in Go, CSS and the documentation tables fails without rewriting', t => {
  const dir = fixture(t);
  generate(dir);
  for (const file of ['internal/design/tokens_gen.go', 'design/tokens/tokens.css', 'design/tokens/mark.svg', 'DESIGN.md']) {
    const path = join(dir, file);
    const original = readFileSync(path, 'utf8');
    const changed = original.replace(/#2[Ff]4[Bb][Dd][Bb]/g, '#2244dd');
    assert.notEqual(original, changed);
    writeFileSync(path, changed);
    assert.ok(check(dir).some(error => error.includes(file)));
    assert.equal(readFileSync(path, 'utf8'), changed);
    writeFileSync(path, original);
  }
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
