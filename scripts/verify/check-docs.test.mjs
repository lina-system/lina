// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { cleanEnv } from '../git.mjs';

const checker = fileURLToPath(new URL('./check-docs.mjs', import.meta.url));

function run(command, args, cwd) {
  const result = spawnSync(command, args, { cwd, encoding: 'utf8', env: cleanEnv() });
  assert.ifError(result.error);
  return result;
}

function fixture(t, files) {
  const dir = mkdtempSync(path.join(tmpdir(), 'lina-docs-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  assert.equal(run('git', ['init', '-q'], dir).status, 0);
  for (const [name, content] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(dir, name)), { recursive: true });
    writeFileSync(path.join(dir, name), content);
  }
  assert.equal(run('git', ['add', '.'], dir).status, 0);
  return { dir, check: () => run(process.execPath, [checker], dir) };
}

test('valid relative, repository-root, encoded, image, and reference targets', (t) => {
  const { check } = fixture(t, {
    'README.md': '[relative](docs/guide.md) [root](/docs/guide.md) [encoded](docs/a%20b.md) ![image](docs/icon.png) [directory](docs/)\n[guide][g]\n\n[g]: docs/guide.md\n',
    'docs/guide.md': '# Guide\n',
    'docs/a b.md': '# Spaces\n',
    'docs/icon.png': 'image',
    LICENSE: '# License\n',
  });
  assert.equal(check().status, 0);
});

test('missing and deleted targets fail with source and destination', (t) => {
  const { dir, check } = fixture(t, {
    'README.md': '[missing](not-here.md) [deleted](gone.md)\n',
    'gone.md': '# Gone\n',
  });
  rmSync(path.join(dir, 'gone.md'));
  const result = check();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /README\.md: broken target not-here\.md/);
  assert.match(result.stderr, /README\.md: broken target gone\.md/);
});

test('unchanged tracked document detects deleted target', (t) => {
  const { dir, check } = fixture(t, {
    'docs/links.md': '[target](target.md)\n',
    'docs/target.md': '# Target\n',
  });
  assert.equal(check().status, 0);
  rmSync(path.join(dir, 'docs/target.md'));
  const result = check();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /docs\/links\.md: broken target target\.md/);
});

test('code examples and remote and fragment links are not file targets', (t) => {
  const { check } = fixture(t, {
    'README.md': '`[inline](missing.md)`\n```md\n[fenced](missing.md)\n```\n[web](https://example.invalid/missing) [anchor](#missing)\n',
  });
  assert.equal(check().status, 0);
});

test('repository escape is rejected, including percent-encoded traversal', (t) => {
  const { check } = fixture(t, { 'docs/guide.md': '[escape](../../outside.md) [encoded](%2e%2e/%2e%2e/outside.md)\n' });
  const result = check();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /docs\/guide\.md: target escapes repository: \.\.\/\.\.\/outside\.md/);
  assert.match(result.stderr, /docs\/guide\.md: target escapes repository: %2e%2e\/.*outside\.md/);
});

test('local Markdown fragments resolve relative, root, encoded, and reference links', (t) => {
  const { check } = fixture(t, {
    'README.md': '[relative](docs/guide.md#tools-and-sandbox) [root](/docs/guide.md#tools-and-sandbox)\n'
      + '[encoded](docs/a%20b.MD#%EC%84%A4%EC%A0%95) [query](docs/guide.md?plain=1#tools-and-sandbox)\n'
      + '[reference][tools]\n[tools]: docs/guide.md#tools-and-sandbox\n',
    'docs/guide.md': '# Tools and sandbox\n',
    'docs/a b.MD': '# 설정\n',
  });
  const result = check();
  assert.equal(result.status, 0, result.stderr);
});

test('missing and malformed Markdown fragments fail with source and destination', (t) => {
  const { check } = fixture(t, {
    'README.md': '[missing](docs/guide.md#tools) [encoded](docs/guide.md#%ZZ)\n'
      + '[case](docs/guide.md#Tools-and-sandbox) [reference][bad]\n[bad]: docs/guide.md#absent\n',
    'docs/guide.md': '# Tools and sandbox\n',
  });
  const result = check();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /README\.md: broken anchor docs\/guide\.md#tools\n/);
  assert.match(result.stderr, /README\.md: invalid encoded target docs\/guide\.md#%ZZ/);
  assert.match(result.stderr, /README\.md: broken anchor docs\/guide\.md#Tools-and-sandbox/);
  assert.match(result.stderr, /README\.md: broken anchor docs\/guide\.md#absent/);
});

test('unchanged incoming links fail after a heading is renamed', (t) => {
  const { dir, check } = fixture(t, {
    'README.md': '[tools](docs/guide.md#tools)\n',
    'docs/guide.md': '## Tools\n',
  });
  assert.equal(check().status, 0);
  writeFileSync(path.join(dir, 'docs/guide.md'), '## Tools and sandbox\n');
  const result = check();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /README\.md: broken anchor docs\/guide\.md#tools/);
});

test('GitHub heading slugs keep inline text, Unicode, hyphens, and underscores', (t) => {
  const { check } = fixture(t, {
    'README.md': '[formatted](guide.md#use-lina-and-tools-now) [punctuation](guide.md#hello-world--ok)\n'
      + '[unicode](guide.md#설정-café) [code](guide.md#run-read_file-and-file_name)\n'
      + '[setext](guide.md#another-heading) [closing](guide.md#closing-hashes)\n'
      + '[entities](guide.md#café--tea)\n',
    'guide.md': '# Use **LINA** and [tools](guide.md) ~~now~~\n'
      + '## Hello, world! — OK?\n### 설정 Café\n'
      + '## Run `read_file` and file_name\n\nAnother *heading*\n---\n\n'
      + '   ### Closing hashes ###\n\n## Caf&#233; &amp; tea\n',
  });
  const result = check();
  assert.equal(result.status, 0, result.stderr);
});

test('duplicate headings use collision-free suffixes in document order', (t) => {
  const { dir, check } = fixture(t, {
    'README.md': '[first](guide.md#tools) [second](guide.md#tools-1)\n'
      + '[collision](guide.md#tools-1-1) [third](guide.md#tools-2) [separate](other.md#tools)\n',
    'guide.md': '# Tools\n## Tools\n### Tools-1\n#### Tools\n',
    'other.md': '# Tools\n',
  });
  assert.equal(check().status, 0);
  writeFileSync(path.join(dir, 'README.md'), '[missing duplicate](guide.md#tools-3)\n');
  const result = check();
  assert.equal(result.status, 1);
  assert.match(result.stderr, /broken anchor guide\.md#tools-3/);
});

test('headings in code and comments cannot satisfy an anchor', (t) => {
  const { check } = fixture(t, {
    'README.md': ['fenced', 'nested', 'tilde', 'indented', 'comment', 'not-a-heading']
      .map(anchor => `[example](guide.md#${anchor})`).join('\n'),
    'guide.md': '````md\n# Fenced\n```\n# Nested\n````\n'
      + '~~~\n# Tilde\n~~~\n    # Indented\n<!--\n# Comment\n-->\n#Not-a-heading\n',
  });
  const result = check();
  assert.equal(result.status, 1);
  for (const anchor of ['fenced', 'nested', 'tilde', 'indented', 'comment', 'not-a-heading']) {
    assert.ok(result.stderr.includes(`broken anchor guide.md#${anchor}`), result.stderr);
  }
});

test('external URLs, bare fragments, non-Markdown fragments, and code links stay excluded', (t) => {
  const { check } = fixture(t, {
    'README.md': '[remote](https://example.invalid/guide.md#missing) [bare](#missing)\n'
      + '[empty](guide.md#) [image](icon.svg#missing) [query](guide.md?plain=1)\n'
      + '`[inline](guide.md#missing)`\n```md\n[fenced](guide.md#missing)\n```\n',
    'guide.md': '# Guide\n',
    'icon.svg': '<svg/>',
  });
  const result = check();
  assert.equal(result.status, 0, result.stderr);
});
