// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { git as runGit } from '../git.mjs';
import { scopeFor } from './scope.mjs';

function repository(t) {
  const cwd = mkdtempSync(join(tmpdir(), 'lina-scope-'));
  t.after(() => rmSync(cwd, { recursive: true, force: true }));
  const env = {
    HOME: cwd, XDG_CONFIG_HOME: cwd, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_AUTHOR_NAME: 'Fixture', GIT_AUTHOR_EMAIL: 'fixture@example.invalid',
    GIT_COMMITTER_NAME: 'Fixture', GIT_COMMITTER_EMAIL: 'fixture@example.invalid',
  };
  const git = (...args) => {
    const result = runGit(cwd, args, { env });
    assert.equal(result.status, 0, result.stderr);
    return result.stdout.trim();
  };
  git('init', '--initial-branch=dev');
  writeFileSync(join(cwd, 'README.md'), '# Fixture\n');
  git('add', '.');
  git('commit', '-m', 'baseline');
  const base = git('rev-parse', 'HEAD');
  const commit = () => {
    git('add', '-A');
    git('commit', '-m', 'fixture change');
    return git('rev-parse', 'HEAD');
  };
  const classify = (head, options = {}) => scopeFor({ base, head, cwd, ...options });
  return { cwd, base, git, commit, classify };
}

test('a real document-only Git diff excludes automation', t => {
  const r = repository(t);
  writeFileSync(join(r.cwd, 'README.md'), '# Changed document\n');
  const scope = r.classify(r.commit());
  assert.equal(scope.mode, 'docs');
  assert.equal(scope.reason, 'documents-only');
  assert.equal(scope.docs, true);
  assert.equal(scope.automation, false);
  assert.deepEqual(scope.changed, ['README.md']);
  assert.deepEqual(scope.unmapped, []);
  assert.equal(r.classify(scope.head, { full: true }).automation, true);
  assert.equal(r.classify(scope.head, { full: true }).reason, 'manual-full');
});

test('a design document added directly under docs/design excludes automation', t => {
  const r = repository(t);
  mkdirSync(join(r.cwd, 'docs/design'), { recursive: true });
  writeFileSync(join(r.cwd, 'docs/design/example.md'), '# Design\n');
  const scope = r.classify(r.commit());
  assert.equal(scope.mode, 'docs');
  assert.equal(scope.automation, false);
  assert.deepEqual(scope.unmapped, []);
  assert.deepEqual(scope.changed, ['docs/design/example.md']);
});

test('modifying, renaming, and deleting a design document stay documentation-only', t => {
  const r = repository(t);
  mkdirSync(join(r.cwd, 'docs/design'), { recursive: true });
  writeFileSync(join(r.cwd, 'docs/design/example.md'), '# Design\n');
  const added = r.commit();
  writeFileSync(join(r.cwd, 'docs/design/example.md'), '# Revised design\n');
  const modified = r.commit();
  renameSync(join(r.cwd, 'docs/design/example.md'), join(r.cwd, 'docs/design/renamed.md'));
  const renamed = r.commit();
  rmSync(join(r.cwd, 'docs/design/renamed.md'));
  const deleted = r.commit();
  const steps = [
    [added, modified, ['docs/design/example.md']],
    [modified, renamed, ['docs/design/example.md', 'docs/design/renamed.md']],
    [renamed, deleted, ['docs/design/renamed.md']],
  ];
  for (const [previousHead, head, changed] of steps) {
    const scope = r.classify(head, { base: previousHead });
    assert.equal(scope.mode, 'docs');
    assert.equal(scope.automation, false);
    assert.deepEqual(scope.unmapped, []);
    assert.deepEqual(scope.changed, changed);
  }
});

for (const path of ['docs/design/example.mjs', 'docs/design/nested/example.md', 'docs/designer/example.md', 'docs/design/example.MD']) {
  test(`${path} is not a registered design document`, t => {
    const r = repository(t);
    mkdirSync(join(r.cwd, path, '..'), { recursive: true });
    writeFileSync(join(r.cwd, path), 'fixture\n');
    const scope = r.classify(r.commit());
    assert.equal(scope.mode, 'full');
    assert.equal(scope.reason, 'unmapped-paths');
    assert.deepEqual(scope.unmapped, [path]);
  });
}

test('a script change selects both checks, and so does shared configuration', t => {
  const r = repository(t);
  mkdirSync(join(r.cwd, 'scripts/verify'), { recursive: true });
  writeFileSync(join(r.cwd, 'scripts/verify/example.mjs'), 'export {};\n');
  writeFileSync(join(r.cwd, 'scripts/example.sh'), 'true\n');
  const scripts = r.classify(r.commit());
  assert.equal(scripts.mode, 'full');
  assert.equal(scripts.automation, true);
  assert.deepEqual(scripts.unmapped, []);
  writeFileSync(join(r.cwd, '.gitignore'), 'output/\n');
  const configuration = r.classify(r.commit(), { base: scripts.head });
  assert.equal(configuration.mode, 'full');
  assert.equal(configuration.reason, 'automation-change');
  assert.deepEqual(configuration.unmapped, []);
});

test('a script nested deeper than scripts/verify is unmapped', t => {
  const r = repository(t);
  mkdirSync(join(r.cwd, 'scripts/verify/nested'), { recursive: true });
  writeFileSync(join(r.cwd, 'scripts/verify/nested/example.mjs'), 'export {};\n');
  writeFileSync(join(r.cwd, 'scripts/example.py'), 'pass\n');
  assert.deepEqual(r.classify(r.commit()).unmapped, ['scripts/example.py', 'scripts/verify/nested/example.mjs']);
});

test('a renamed document cannot hide an unregistered product file', t => {
  const r = repository(t);
  renameSync(join(r.cwd, 'README.md'), join(r.cwd, 'prompt.md'));
  const scope = r.classify(r.commit());
  assert.deepEqual(scope.changed, ['README.md', 'prompt.md']);
  assert.deepEqual(scope.unmapped, ['prompt.md']);
  assert.equal(scope.automation, true);
});

test('a deleted unregistered file keeps the change out of documents-only without blocking it', t => {
  const r = repository(t);
  mkdirSync(join(r.cwd, 'legacy'), { recursive: true });
  writeFileSync(join(r.cwd, 'legacy/old.yml'), 'a: 1\n');
  const added = r.commit();
  rmSync(join(r.cwd, 'legacy'), { recursive: true });
  writeFileSync(join(r.cwd, 'README.md'), '# Changed\n');
  const scope = r.classify(r.commit(), { base: added });
  assert.deepEqual(scope.changed, ['README.md', 'legacy/old.yml']);
  assert.deepEqual(scope.unmapped, []);
  assert.equal(scope.mode, 'full');
  assert.equal(scope.automation, true);
});

test('deletions stay in the diff, and an empty diff or missing base selects full', t => {
  const r = repository(t);
  rmSync(join(r.cwd, 'README.md'));
  const head = r.commit();
  assert.deepEqual(r.classify(head).changed, ['README.md']);
  assert.equal(r.classify(head, { base: head }).mode, 'full');
  assert.equal(r.classify(head, { base: head }).reason, 'empty-diff');
  assert.equal(r.classify(head, { base: '' }).reason, 'base-unavailable');
  assert.equal(r.classify(head, { base: 'does-not-exist' }).mode, 'full');
  assert.throws(() => r.classify('does-not-exist'));
});

test('unmapped tracked code stays uncovered even if this diff only changes a document', t => {
  const r = repository(t);
  writeFileSync(join(r.cwd, 'unverified.mjs'), 'throw new Error("unverified");\n');
  const base = r.commit();
  writeFileSync(join(r.cwd, 'README.md'), '# Document only\n');
  const scope = r.classify(r.commit(), { base });
  assert.deepEqual(scope.unmapped, ['unverified.mjs']);
  assert.equal(scope.automation, true);
});

test('the checked-out candidate is read from the work tree, including untracked and removed files', t => {
  const r = repository(t);
  writeFileSync(join(r.cwd, 'tracked.txt'), 'tracked\n');
  const head = r.commit();
  assert.deepEqual(r.classify(head).unmapped, ['tracked.txt']);
  rmSync(join(r.cwd, 'tracked.txt'));
  writeFileSync(join(r.cwd, 'untracked.txt'), 'new\n');
  assert.deepEqual(r.classify(head).unmapped, ['untracked.txt']);
  writeFileSync(join(r.cwd, '.gitignore'), 'untracked.txt\n');
  assert.deepEqual(r.classify(head).unmapped, []);
  // A candidate that is not checked out is read from its commit.
  assert.deepEqual(r.classify(r.base).unmapped, []);
  assert.deepEqual(r.classify(head, { cwd: r.cwd, base: r.base }).changed, ['tracked.txt']);
});
