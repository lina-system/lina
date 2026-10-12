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
