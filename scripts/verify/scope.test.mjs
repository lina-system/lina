// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { git as runGit } from '../git.mjs';
import { componentRegistry, kind, scopeFor } from './scope.mjs';
import { evaluateGate } from './gate.mjs';

test('evidence records are documents, including additions, edits, renames and deletions', t => {
  const r = repository(t);
  mkdirSync(join(r.cwd, 'evidence'));
  for (const file of ['v6-supply-chain.json', 'v7-dev-loop.json']) {
    const name = `evidence/${file}`;
    const old = r.git('rev-parse', 'HEAD');
    writeFileSync(join(r.cwd, name), '{}\n');
    const added = r.commit();
    writeFileSync(join(r.cwd, name), '{"result":"pass"}\n');
    const modified = r.commit();
    renameSync(join(r.cwd, name), join(r.cwd, 'evidence/renamed.json'));
    const renamed = r.commit();
    rmSync(join(r.cwd, 'evidence/renamed.json'));
    const deleted = r.commit();
    for (const [base, head] of [[old, added], [added, modified], [modified, renamed], [renamed, deleted]]) {
      const scope = r.classify(head, { base });
      assert.equal(scope.reason, 'documents-only');
      assert.equal(scope.automation, false);
      assert.equal(scope.components, false);
      assert.deepEqual(scope.unmapped, []);
    }
  }
  assert.equal(kind('evidence/nested/measurement.json'), 'docs');
  assert.equal(kind('evidence/README.md'), 'docs');
  assert.equal(kind('evidence/run.mjs'), 'unmapped');
  assert.equal(kind('evidence-other/measurement.json'), 'unmapped');
});

test('build metadata changes select both binaries and verify the shared package', t => {
  const r = repository(t);
  const files = {
    'go.mod': 'module example.invalid/lina\ngo 1.27\ntoolchain go1.27.3\n',
    'kit/go.mod': 'module example.invalid/lina/kit\ngo 1.27\ntoolchain go1.27.3\n',
    'go.work': 'go 1.27\ntoolchain go1.27.3\nuse (\n.\n./kit\n)\n',
    'doc.go': 'package lina\n',
    'kit/doc.go': 'package kit\n',
    'internal/buildinfo/info.go': 'package buildinfo\n',
    'cmd/lina/main.go': 'package main\nimport _ "example.invalid/lina/internal/buildinfo"\nfunc main() {}\n',
    'cmd/lina-node/main.go': 'package main\nimport _ "example.invalid/lina/internal/buildinfo"\nfunc main() {}\n',
    'internal/archtest/imports_test.go': 'package archtest\n',
  };
  for (const [name, content] of Object.entries(files)) {
    mkdirSync(join(r.cwd, name, '..'), { recursive: true });
    writeFileSync(join(r.cwd, name), content);
  }
  const base = r.commit();
  writeFileSync(join(r.cwd, 'internal/buildinfo/info.go'), 'package buildinfo\n// changed\n');
  const scope = r.classify('HEAD', { base });
  assert.deepEqual(scope.unmapped, []);
  assert.deepEqual(scope.selectedComponents, ['lina', 'lina-node']);
  assert.equal(scope.automation, false);
  assert.equal(scope.dependencies, false);
  assert.ok(componentRegistry.find(component => component.name === 'lina').packages.includes('./internal/buildinfo/...'));
});

test('every Go component change also selects the architecture test', t => {
  const r = repository(t);
  const files = {
    'go.mod': 'module example.invalid/lina\ngo 1.27\ntoolchain go1.27.3\n',
    'kit/go.mod': 'module example.invalid/lina/kit\ngo 1.27\ntoolchain go1.27.3\n',
    'go.work': 'go 1.27\ntoolchain go1.27.3\nuse (\n.\n./kit\n)\n',
    'doc.go': 'package lina\n',
    'kit/doc.go': 'package kit\n',
    'cmd/lina/main.go': 'package main\nfunc main() {}\n',
    'cmd/lina-node/main.go': 'package main\nfunc main() {}\n',
    'internal/archtest/imports_test.go': 'package archtest\n',
  };
  for (const [name, content] of Object.entries(files)) {
    mkdirSync(join(r.cwd, name, '..'), { recursive: true });
    writeFileSync(join(r.cwd, name), content);
  }
  const base = r.commit();
  for (const [path, component] of [
    ['kit/doc.go', 'kit'], ['cmd/lina/main.go', 'lina'], ['cmd/lina-node/main.go', 'lina-node'],
    ['internal/archtest/imports_test.go', 'lina'],
  ]) {
    writeFileSync(join(r.cwd, path), files[path] + '// edited\n');
    const scope = r.classify('HEAD', { base });
    assert.deepEqual(scope.selectedComponents, ['kit', 'lina', 'lina-node'].filter(name => name === component || name === 'lina'));
    assert.equal(scope.automation, false);
    assert.deepEqual(scope.unmapped, []);
    const results = Object.fromEntries(['docs', 'automation', 'dependencies', 'components'].map(name => [name, { result: scope[name] ? 'success' : 'skipped' }]));
    assert.equal(evaluateGate(scope, { selection: { result: 'success' }, ...results }).result, 'success');
    writeFileSync(join(r.cwd, path), files[path]);
  }
  assert.ok(componentRegistry.find(component => component.name === 'lina').packages.includes('./internal/archtest/...'));
  writeFileSync(join(r.cwd, 'README.md'), '# Docs\n');
  assert.equal(r.classify('HEAD', { base }).components, false);
});

for (const path of ['protocol/schema/client/1.0/envelope.json', 'protocol/fixtures/client/1.0/hello.json']) {
  test(`${path} selects kit and its consumers`, t => {
    const r = repository(t);
    const files = {
      'go.mod': 'module example.invalid/lina\ngo 1.27\ntoolchain go1.27.3\n',
      'kit/go.mod': 'module example.invalid/lina/kit\ngo 1.27\ntoolchain go1.27.3\n',
      'go.work': 'go 1.27\ntoolchain go1.27.3\nuse (\n.\n./kit\n)\n',
      'doc.go': 'package lina\n',
      'kit/protocol/doc.go': 'package protocol\n',
      'cmd/lina/main.go': 'package main\nimport _ "example.invalid/lina/kit/protocol"\nfunc main() {}\n',
      'cmd/lina-node/main.go': 'package main\nfunc main() {}\n',
      [path]: '{}\n',
    };
    for (const [name, content] of Object.entries(files)) {
      mkdirSync(join(r.cwd, name, '..'), { recursive: true });
      writeFileSync(join(r.cwd, name), content);
    }
    const base = r.commit();
    writeFileSync(join(r.cwd, path), '{"changed":true}\n');
    const scope = r.classify(r.commit(), { base });
    assert.equal(kind(path), 'components');
    assert.deepEqual(scope.unmapped, []);
    assert.deepEqual(scope.selectedComponents, ['kit', 'lina']);
    assert.equal(scope.automation, false);
    rmSync(join(r.cwd, path));
    assert.deepEqual(r.classify(r.commit(), { base }).selectedComponents, ['kit', 'lina']);
  });
}

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
  assert.equal(scope.dependencies, false);
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
  assert.deepEqual(r.classify(head, { cwd: r.cwd, base: r.base }).changed, ['.gitignore', 'tracked.txt']);
});

test('Go components select importers transitively, including test imports, and leave unrelated components out', t => {
  const r = repository(t);
  const files = {
    'go.mod': 'module example.invalid/lina\n\ngo 1.27\ntoolchain go1.27.3\n',
    'kit/go.mod': 'module example.invalid/lina/kit\n\ngo 1.27\ntoolchain go1.27.3\n',
    'go.work': 'go 1.27\ntoolchain go1.27.3\nuse (\n.\n./kit\n)\n',
    'doc.go': 'package lina\n',
    'kit/doc.go': 'package kit\n',
    'cmd/lina/main.go': 'package main\nimport _ "example.invalid/lina"\nfunc main() {}\n',
    'cmd/lina/main_test.go': 'package main\nimport _ "example.invalid/lina/kit"\n',
    'cmd/lina-node/main.go': 'package main\nfunc main() {}\n',
  };
  for (const [name, content] of Object.entries(files)) {
    mkdirSync(join(r.cwd, name, '..'), { recursive: true });
    writeFileSync(join(r.cwd, name), content);
  }
  const base = r.commit();
  writeFileSync(join(r.cwd, 'kit/doc.go'), 'package kit\n// changed\n');
  const head = r.commit();
  const scope = r.classify(head, { base });
  assert.deepEqual(scope.selectedComponents, ['kit', 'lina']);
  assert.equal(scope.components, true);
  assert.equal(scope.dependencies, false);
  assert.deepEqual(scope.unmapped, []);
  assert.equal(scope.automation, false);
  mkdirSync(join(r.cwd, 'cmd/lina/bridge'));
  writeFileSync(join(r.cwd, 'cmd/lina/bridge/bridge.go'), 'package bridge\nimport _ "example.invalid/lina/kit"\n');
  writeFileSync(join(r.cwd, 'cmd/lina-node/main.go'), 'package main\nimport _ "example.invalid/lina/cmd/lina/bridge"\nfunc main() {}\n');
  const chain = r.commit();
  writeFileSync(join(r.cwd, 'kit/doc.go'), 'package kit\n// changed again\n');
  assert.deepEqual(r.classify(r.commit(), { base: chain }).selectedComponents, ['kit', 'lina', 'lina-node']);
  writeFileSync(join(r.cwd, 'go.mod'), files['go.mod'] + '\n// changed\n');
  assert.deepEqual(r.classify('HEAD', { base: chain }).selectedComponents, ['kit', 'lina', 'lina-node']);
  r.commit();
  mkdirSync(join(r.cwd, 'kit/removed'));
  writeFileSync(join(r.cwd, 'kit/removed/doc.go'), 'package removed\n');
  writeFileSync(join(r.cwd, 'cmd/lina/bridge/bridge.go'), 'package bridge\nimport _ "example.invalid/lina/kit/removed"\n');
  const beforeDelete = r.commit();
  rmSync(join(r.cwd, 'kit/removed'), { recursive: true });
  const config = r.commit();
  assert.deepEqual(r.classify(config, { base: beforeDelete }).selectedComponents, ['kit', 'lina', 'lina-node']);
  writeFileSync(join(r.cwd, 'README.md'), '# Docs\n');
  assert.equal(r.classify(r.commit(), { base: config }).components, false);
  // Existing unregistered Go code cannot hide in a docs-only diff.
  writeFileSync(join(r.cwd, 'unexpected.go'), 'package lina\n');
  assert.deepEqual(r.classify('HEAD', { base: config }).unmapped, ['unexpected.go']);
});

test('module and workspace changes select dependencies and every component without running Go', t => {
  const r = repository(t);
  const files = ['go.mod', 'go.sum', 'go.work', 'go.work.sum', 'kit/go.mod', 'kit/go.sum',
    'doc.go', 'kit/doc.go', 'cmd/lina/main.go', 'cmd/lina-node/main.go'];
  for (const path of files) {
    mkdirSync(join(r.cwd, path, '..'), { recursive: true });
    writeFileSync(join(r.cwd, path), 'fixture\n');
  }
  const base = r.commit();
  for (const path of files.filter(path => /(?:go\.(?:mod|sum|work)|go\.work\.sum)$/.test(path))) {
    writeFileSync(join(r.cwd, path), 'changed fixture\n');
    const scope = r.classify('HEAD', { base });
    assert.equal(scope.dependencies, true, path);
    assert.deepEqual(scope.selectedComponents, ['kit', 'lina', 'lina-node'], path);
    writeFileSync(join(r.cwd, path), 'fixture\n');
  }
  assert.equal(r.classify('HEAD', { full: true }).dependencies, true);
  assert.equal(r.classify('HEAD', { base }).dependencies, true);
  assert.equal(r.classify('HEAD', { base: '' }).dependencies, true);
  writeFileSync(join(r.cwd, 'README.md'), '# Changed\n');
  assert.equal(r.classify('HEAD', { base }).dependencies, false);
  rmSync(join(r.cwd, 'go.sum'));
  assert.equal(r.classify('HEAD', { base }).dependencies, true);
});

for (const path of ['.github/workflows/ci.yml', '.github/workflows/ci.yaml', '.node-version']) {
  test(`${path} selects automation without unmapped paths`, t => {
    const r = repository(t);
    mkdirSync(join(r.cwd, path, '..'), { recursive: true });
    writeFileSync(join(r.cwd, path), 'fixture\n');
    const scope = r.classify(r.commit());
    assert.equal(scope.mode, 'full');
    assert.equal(scope.reason, 'automation-change');
    assert.equal(scope.automation, true);
    assert.deepEqual(scope.unmapped, []);
  });
}

for (const path of ['.github/workflows/nested/ci.yml', '.github/workflows/script.sh', '.github/actions/example/action.yml']) {
  test(`${path} still needs its own verification registration`, t => {
    const r = repository(t);
    mkdirSync(join(r.cwd, path, '..'), { recursive: true });
    writeFileSync(join(r.cwd, path), 'fixture\n');
    assert.deepEqual(r.classify(r.commit()).unmapped, [path]);
  });
}

for (const path of ['scripts/verify/dependency-exceptions.json', 'THIRD-PARTY-NOTICES.md']) {
  test(`${path}: additions, modifications and deletions select dependencies without components`, t => {
    const r = repository(t);
    mkdirSync(join(r.cwd, path, '..'), { recursive: true });
    writeFileSync(join(r.cwd, path), 'fixture\n');
    const added = r.commit();
    writeFileSync(join(r.cwd, path), 'changed fixture\n');
    const modified = r.commit();
    rmSync(join(r.cwd, path));
    const deleted = r.commit();
    for (const [base, head] of [[r.base, added], [added, modified], [modified, deleted]]) {
      const scope = r.classify(head, { base });
      assert.equal(scope.dependencies, true);
      assert.equal(scope.automation, false);
      assert.equal(scope.components, false);
      assert.deepEqual(scope.unmapped, []);
    }
  });
}
