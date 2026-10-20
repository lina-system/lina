// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { join } from 'node:path';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { run } from './git.mjs';
import { developmentRepository, installScripts, sandbox } from './test-fixture.mjs';

function foundation(sb, repo, args = [], env = {}) {
  const result = run(process.execPath, [join(repo, 'scripts/foundation.mjs'), ...args], { cwd: repo, env: { ...sb.env, ...env } });
  const line = result.stdout.trim().split('\n').filter(Boolean).at(-1);
  return { ...result, evidence: line ? JSON.parse(line) : null };
}

const outcomes = (evidence) => Object.fromEntries(Object.entries(evidence.checks).map(([name, check]) => [name, check.result]));
const noteOf = (sb, repo, commit) => sb.g(repo, 'notes', '--ref=refs/notes/foundation', 'show', commit);

test('a full run on a clean checkout passes and records the evidence as a note', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const result = foundation(sb, work, ['--full']);
  assert.equal(result.status, 0, result.stderr);
  const head = sb.g(work, 'rev-parse', 'HEAD');
  assert.equal(result.evidence.result, 'success');
  assert.equal(result.evidence.mode, 'full');
  assert.equal(result.evidence.head, head);
  assert.deepEqual(outcomes(result.evidence), { selection: 'success', docs: 'success', automation: 'success', dependencies: 'skipped', components: 'skipped' });
  for (const check of Object.values(result.evidence.checks).filter(check => check.result !== 'skipped')) assert.equal(typeof check.seconds, 'number');
  assert.equal(result.evidence.tools.node, process.version);
  const note = JSON.parse(noteOf(sb, work, head));
  assert.equal(note.head, head);
  assert.equal(note.tree, sb.g(work, 'rev-parse', 'HEAD^{tree}'));
  assert.equal(note.result, 'success');
});

test('a documents-only range skips automation, and the default range starts at the merge base with dev', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  sb.g(work, 'checkout', '-q', '-b', 'feature');
  sb.commit(work, 'docs: more', { 'docs/design/guide.md': '# Guide\n\nMore.\n' });
  const result = foundation(sb, work);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.evidence.mode, 'docs');
  assert.equal(result.evidence.base, sb.g(work, 'rev-parse', 'dev'));
  assert.deepEqual(outcomes(result.evidence), { selection: 'success', docs: 'success', automation: 'skipped', dependencies: 'skipped', components: 'skipped' });
});

test('on dev itself the default range is the previous dev head', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const previous = sb.g(work, 'rev-parse', 'HEAD');
  sb.commit(work, 'docs: update', { 'README.md': '# Fixture\n\n[guide](docs/design/guide.md)\n\nText.\n' });
  const result = foundation(sb, work);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.evidence.base, previous);
  assert.equal(result.evidence.mode, 'docs');
});

test('an explicit range is measured as given', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const first = sb.g(work, 'rev-parse', 'HEAD');
  const second = sb.commit(work, 'docs: update', { 'README.md': '# Fixture\n\n[guide](docs/design/guide.md)\n\nText.\n' });
  const result = foundation(sb, work, ['--range', `${first}..${second}`]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.evidence.base, first);
  assert.equal(result.evidence.head, second);
  assert.equal(foundation(sb, work, ['--range', 'nonsense']).status, 1);
  assert.equal(foundation(sb, work, ['--full', '--range', `${first}..${second}`]).status, 1);
});

test('a range naming a different candidate fails selection before running checks', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const checkout = sb.g(work, 'rev-parse', 'HEAD');
  assert.equal(foundation(sb, work, ['--full', '--no-note']).status, 0);
  const base = sb.commit(work, 'docs: base', { 'README.md': '# Base\n' });
  const candidate = sb.commit(work, 'docs: broken candidate', { 'README.md': '[missing](missing.md)\n' });
  sb.g(work, 'checkout', '-q', '--detach', checkout);

  for (const flags of [[], ['--no-note']]) {
    const result = foundation(sb, work, ['--range', `${base}..${candidate}`, ...flags]);
    assert.equal(result.status, 1, result.stdout);
    assert.equal(result.evidence.result, 'failure');
    assert.equal(result.evidence.head, candidate);
    assert.deepEqual(outcomes(result.evidence), { selection: 'failure' });
    assert.match(result.stderr, /check out the candidate first/i);
    assert.equal(result.evidence.note, 'not written');
  }
  assert.notEqual(run('git', ['notes', '--ref=refs/notes/foundation', 'show', candidate], { cwd: work, env: sb.env }).status, 0);

  // Once checked out, the same candidate reaches its real failing docs check.
  sb.g(work, 'checkout', '-q', '--detach', candidate);
  const result = foundation(sb, work, ['--range', `${base}..${candidate}`, '--no-note']);
  assert.equal(result.status, 1);
  assert.equal(result.evidence.checks.selection.result, 'success');
  assert.equal(result.evidence.checks.docs.result, 'failure');
});

test('a broken link fails the docs check, exits non-zero and writes no note', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const head = sb.commit(work, 'docs: break a link', { 'README.md': '[gone](docs/design/gone.md)\n' });
  const result = foundation(sb, work);
  assert.equal(result.status, 1);
  assert.equal(result.evidence.result, 'failure');
  assert.equal(result.evidence.checks.docs.result, 'failure');
  assert.match(result.stderr, /broken target docs\/design\/gone\.md/);
  assert.notEqual(run('git', ['notes', '--ref=refs/notes/foundation', 'show', head], { cwd: work, env: sb.env }).status, 0);
});

test('whitespace errors in the range fail selection, and --full checks the HEAD commit', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  sb.commit(work, 'docs: trailing space', { 'docs/design/guide.md': '# Guide  \n' });
  const range = foundation(sb, work);
  assert.equal(range.status, 1);
  assert.equal(range.evidence.checks.selection.result, 'failure');
  assert.equal(range.evidence.checks.docs, undefined);
  assert.match(range.stderr, /trailing whitespace/);
  assert.equal(foundation(sb, work, ['--full']).status, 1);
});

test('an unregistered path blocks the gate until its verification is registered', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  sb.commit(work, 'feat: new component', { 'component/main.go': 'package main\n' });
  const result = foundation(sb, work);
  assert.equal(result.status, 1);
  assert.match(result.evidence.failures.join('\n'), /unmapped paths: component\/main\.go/);
});

test('a script syntax error fails the automation check', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  sb.commit(work, 'chore: break a script', { 'scripts/verify/broken.mjs': 'const = ;\n' });
  const result = foundation(sb, work);
  assert.equal(result.status, 1);
  assert.equal(result.evidence.checks.automation.result, 'failure');
});

test('a failing test file fails the automation check, and a passing one runs', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  sb.commit(work, 'test: passes', { 'scripts/example.test.mjs': "import test from 'node:test';\ntest('ok', () => {});\n" });
  assert.equal(foundation(sb, work).status, 0);
  sb.commit(work, 'test: fails', { 'scripts/example.test.mjs': "import test from 'node:test';\ntest('bad', () => { throw new Error('bad'); });\n" });
  const result = foundation(sb, work);
  assert.equal(result.status, 1);
  assert.equal(result.evidence.checks.automation.result, 'failure');
});

test('an uncommitted work tree is checked but leaves no evidence note', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  sb.write(work, 'docs/design/guide.md', '# Guide\n\nEdited and not committed.\n');
  const result = foundation(sb, work, ['--full']);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.evidence.note, /not written/);
  const head = sb.g(work, 'rev-parse', 'HEAD');
  assert.notEqual(run('git', ['notes', '--ref=refs/notes/foundation', 'show', head], { cwd: work, env: sb.env }).status, 0);
});

test('GIT_* variables of the caller never redirect foundation to another repository', (t) => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const decoy = join(sb.dir, 'decoy');
  sb.g(sb.dir, 'init', '-q', decoy);
  sb.commit(decoy, 'decoy', { 'README.md': '# Decoy\n' });
  const before = sb.g(decoy, 'for-each-ref');
  const env = { GIT_DIR: join(decoy, '.git'), GIT_WORK_TREE: decoy, GIT_INDEX_FILE: join(decoy, '.git/index') };
  const result = foundation(sb, work, ['--full'], env);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.evidence.head, sb.g(work, 'rev-parse', 'HEAD'));
  assert.equal(sb.g(decoy, 'for-each-ref'), before);
  assert.equal(sb.g(decoy, 'status', '--porcelain'), '');
});

test('foundation records selected Go components, skips them for prose and fails on broken Go code', t => {
  const sb = sandbox(t);
  const caches = run('go', ['env', '-json', 'GOCACHE', 'GOMODCACHE']);
  assert.equal(caches.status, 0, caches.stderr);
  Object.assign(sb.env, JSON.parse(caches.stdout));
  const { work } = developmentRepository(sb);
  const source = new URL('../', import.meta.url);
  // This fixture tests Go component orchestration; dependency behavior has offline runner tests.
  sb.write(work, 'scripts/verify/check-dependencies.mjs', `// SPDX-License-Identifier: Apache-2.0
process.exitCode = 0;
`);
  for (const name of ['go.mod', 'go.sum', 'go.work', 'kit/go.mod']) {
    sb.write(work, name, readFileSync(new URL(name, source)));
  }
  sb.write(work, 'doc.go', 'package lina\n');
  sb.write(work, 'kit/doc.go', 'package kit\n');
  for (const name of ['lina', 'lina-node']) sb.write(work, `cmd/${name}/main.go`,
    'package main\nimport ("fmt"; "os")\nfunc main() { fmt.Fprintln(os.Stderr, "usage: fixture"); os.Exit(64) }\n');
  sb.commit(work, 'feat: Go components');
  const green = foundation(sb, work, ['--full']);
  assert.equal(green.status, 0, green.stderr);
  assert.deepEqual(green.evidence.selectedComponents, ['kit', 'lina', 'lina-node']);
  assert.equal(green.evidence.checks.components.result, 'success');
  assert.equal(green.evidence.checks.dependencies.result, 'success');
  assert.equal(green.evidence.tools.go, 'go1.27.3');
  sb.commit(work, 'docs: change', { 'README.md': '# Updated fixture\n' });
  assert.equal(foundation(sb, work).evidence.checks.components.result, 'skipped');
  assert.equal(foundation(sb, work).evidence.checks.dependencies.result, 'skipped');
  const broken = sb.commit(work, 'feat: broken Go', { 'kit/doc.go': 'package kit\nvar broken = doesNotExist\n' });
  const red = foundation(sb, work);
  assert.equal(red.status, 1);
  assert.equal(red.evidence.checks.components.result, 'failure');
  assert.match(red.stderr, /doesNotExist/);
  assert.notEqual(run('git', ['notes', '--ref=refs/notes/foundation', 'show', broken], { cwd: work, env: sb.env }).status, 0);
});

test('foundation runs dependencies once, records failures, and skips it for prose', t => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  // Process stubs exercise orchestration without invoking Go or accessing the network.
  sb.write(work, 'go.mod', 'module example.invalid/fixture\n');
  sb.write(work, 'scripts/verify/check-dependencies.mjs', `// SPDX-License-Identifier: Apache-2.0
import { appendFileSync, existsSync } from 'node:fs';
appendFileSync('calls', 'dependencies\\n');
if (existsSync('go.sum')) { console.error('seeded module verification failure'); process.exitCode = 1; }
`);
  sb.write(work, '.gitignore', 'calls\n');
  sb.commit(work, 'test: seed dependencies');
  const green = foundation(sb, work);
  assert.equal(green.status, 0, green.stderr);
  assert.equal(green.evidence.checks.dependencies.result, 'success');
  assert.equal(readFileSync(join(work, 'calls'), 'utf8'), 'dependencies\n');
  const failedHead = sb.commit(work, 'test: bad sums', { 'go.sum': 'CORRUPT\n' });
  const red = foundation(sb, work);
  assert.equal(red.status, 1);
  assert.equal(red.evidence.checks.dependencies.result, 'failure');
  assert.match(red.stderr, /seeded module verification failure/);
  assert.equal(red.evidence.note, 'not written');
  assert.notEqual(run('git', ['notes', '--ref=refs/notes/foundation', 'show', failedHead], { cwd: work, env: sb.env }).status, 0);
  sb.commit(work, 'docs: change', { 'README.md': '# Changed\n' });
  assert.equal(foundation(sb, work).evidence.checks.dependencies.result, 'skipped');
  assert.equal(readFileSync(join(work, 'calls'), 'utf8'), 'dependencies\ndependencies\n');
});
const workflow = `# SPDX-License-Identifier: Apache-2.0
name: Fixture
on: push
jobs:
  foundation:
    runs-on: ubuntu-24.04
    steps:
      - run: echo verified
`;

for (const extension of ['yml', 'yaml']) {
  test(`a valid ${extension} workflow passes foundation`, (t) => {
    const sb = sandbox(t);
    const { work } = developmentRepository(sb);
    sb.commit(work, 'ci: add workflow', { [`.github/workflows/ci.${extension}`]: workflow });
    const result = foundation(sb, work);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.evidence.checks.automation.result, 'success');
  });

  test(`an invalid ${extension} workflow fails automation and writes no note`, (t) => {
    const sb = sandbox(t);
    const { work } = developmentRepository(sb);
    const head = sb.commit(work, 'ci: invalid workflow', {
      [`.github/workflows/ci.${extension}`]: workflow.replace('runs-on:', 'runs_on:'),
    });
    const result = foundation(sb, work);
    assert.equal(result.status, 1);
    assert.equal(result.evidence.checks.automation.result, 'failure');
    assert.match(result.stderr, /actionlint failed/);
    assert.match(result.stderr, /runs_on/);
    assert.notEqual(run('git', ['notes', '--ref=refs/notes/foundation', 'show', head], { cwd: work, env: sb.env }).status, 0);
  });
}
