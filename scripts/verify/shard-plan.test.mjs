// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import test from 'node:test';
import { balance, selectScenarios, planComponents, distinctScenarios } from './shard-plan.mjs';
import { sandbox } from '../test-fixture.mjs';

test('package timings balance deterministic shards without omissions or duplicates', () => {
  const items = [9, 8, 7, 6, 3, 3].map((seconds, i) => ({ id: String.fromCharCode(97 + i), seconds }));
  const shards = balance(items, 3);
  assert.equal(shards.length, 3);
  assert.deepEqual(shards.flat().map(item => item.id).sort(), items.map(item => item.id));
  assert.equal(Math.max(...shards.map(shard => shard.reduce((sum, item) => sum + item.seconds, 0))), 13);
  assert.deepEqual(balance([...items].reverse(), 3), shards);
  assert.deepEqual(items.map(item => item.id), ['a', 'b', 'c', 'd', 'e', 'f'], 'planning does not mutate its inventory');
});

test('new packages receive a default weight and empty shards are omitted', () => {
  const items = [{ id: 'new' }, { id: 'no-tests', seconds: 0 }];
  assert.deepEqual(balance(items, 8).flat().map(item => item.id).sort(), ['new', 'no-tests']);
  assert.equal(balance(items, 8).length, 2);
  assert.deepEqual(balance([], 4), []);
  assert.throws(() => balance(items, 0));
  assert.throws(() => balance([{ id: 'same' }, { id: 'same' }], 2));
});

test('duplicate Go names join only the files that cannot be selected separately', () => {
  const file = (name, tests) => ({ file: name, package: 'example/e2e', tests, imports: [], binaries: [], inputs: [name], milestone: 'm1' });
  const grouped = distinctScenarios([
    file('e2e/a_test.go', ['TestShared']), file('e2e/b_test.go', ['TestShared', 'TestB']),
    file('e2e/c_test.go', ['TestIndependent']),
  ]);
  assert.equal(grouped.length, 2);
  assert.deepEqual(grouped[0].files, ['e2e/a_test.go', 'e2e/b_test.go']);
  assert.deepEqual(grouped[0].tests, ['TestB', 'TestShared']);
  assert.deepEqual(grouped[1].files, ['e2e/c_test.go']);
});

const packages = [
  { path: 'example/core', directory: 'internal/core/one', dependencies: ['example/kit'] },
  { path: 'example/kit', directory: 'kit/shared', dependencies: [] },
  { path: 'example/other', directory: 'internal/other', dependencies: [] },
  { path: 'example/unused', directory: 'internal/unused', dependencies: [] },
  { path: 'example/lina', directory: 'cmd/lina', dependencies: ['example/core'] },
  { path: 'example/worker', directory: 'cmd/worker', dependencies: ['example/other'] },
];
const scenarios = [
  { file: 'e2e/m1/core_test.go', imports: ['example/core'], binaries: [], inputs: ['e2e/m1/core.json'] },
  { file: 'e2e/m1/installed_test.go', imports: [], binaries: ['example/lina'], inputs: [] },
  { file: 'e2e/m2/worker_test.go', imports: [], binaries: ['example/worker'], inputs: ['e2e/m2/worker.json'] },
];
const selected = (changed, options = {}) => selectScenarios({ scenarios, packages, changed, ...options }).selected.map(scenario => scenario.file);

test('scenario imports and launched binaries include transitive Go dependencies', () => {
  assert.deepEqual(selected(['kit/shared/value.go']), ['e2e/m1/core_test.go', 'e2e/m1/installed_test.go']);
  assert.deepEqual(selected(['internal/core/one/value_test.go']), ['e2e/m1/core_test.go', 'e2e/m1/installed_test.go']);
  assert.deepEqual(selected(['cmd/lina/main.go']), ['e2e/m1/installed_test.go']);
  assert.deepEqual(selected(['internal/other/value.go']), ['e2e/m2/worker_test.go']);
  assert.deepEqual(selected(['internal/unused/value.go']), []);
});

test('a scenario source or its fixture selects the owning scenario', () => {
  assert.deepEqual(selected(['e2e/m2/worker_test.go']), ['e2e/m2/worker_test.go']);
  assert.deepEqual(selected(['e2e/m2/worker.json']), ['e2e/m2/worker_test.go']);
  assert.deepEqual(selected(['e2e/m1/core.json']), ['e2e/m1/core_test.go']);
});

test('package testdata and embedded files retain their package dependency', () => {
  assert.deepEqual(selected(['internal/core/one/testdata/nested/input.json']), ['e2e/m1/core_test.go', 'e2e/m1/installed_test.go']);
  const embedded = packages.map(pkg => pkg.path === 'example/core'
    ? { ...pkg, files: ['internal/core/one/prompts/default.md'] } : pkg);
  assert.deepEqual(selected(['internal/core/one/prompts/default.md'], { packages: embedded }), ['e2e/m1/core_test.go', 'e2e/m1/installed_test.go']);
});

test('full, shared, empty and unmapped changes conservatively select all scenarios', () => {
  const all = scenarios.map(scenario => scenario.file);
  assert.deepEqual(selected(['internal/unused/value.go'], { full: true }), all);
  assert.deepEqual(selected(['e2e/m1/harness_test.go'], { shared: true }), all);
  assert.deepEqual(selected([]), all);
  assert.deepEqual(selected(['internal/deleted/old.go']), all);
  assert.deepEqual(selected(['e2e/m1/dynamic-fixture.json']), all);
  assert.deepEqual(selected(['e2e/m2/worker_test.go', 'e2e/m2/removed_test.go']), all);
});

test('docs selection never loads Go or creates component shards', () => {
  const result = planComponents({ cwd: '/unused', scope: { components: false, selectedComponents: [] },
    inventory() { throw new Error('docs must not invoke Go'); } });
  assert.deepEqual(result.tasks, []);
});

test('a real workspace plan covers selected packages and scenario files exactly once', t => {
  const sb = sandbox(t);
  const toolchain = 'go 1.27\ntoolchain go1.27.3\n';
  for (const [path, source] of Object.entries({
    'go.mod': `module example.invalid/lina\n${toolchain}`,
    'kit/go.mod': `module example.invalid/lina/kit\n${toolchain}`,
    'go.work': `${toolchain}use (\n.\n./kit\n)\n`,
    'doc.go': 'package lina\n', 'kit/doc.go': 'package kit\n',
    'internal/core/records/data.go': 'package records\n',
    'cmd/lina/main.go': 'package main\nimport _ "example.invalid/lina/internal/core/records"\nfunc main() {}\n',
    'e2e/m1/one_test.go': 'package acceptance_test\nimport "testing"\nfunc TestOne(t *testing.T) { buildLina(t) }\n',
    'e2e/m1/two_test.go': 'package acceptance_test\nimport "testing"\nfunc TestTwo(t *testing.T) { buildLina(t) }\n',
    'e2e/m1/harness_test.go': 'package acceptance_test\nimport "testing"\nfunc buildLina(t *testing.T) { buildCommand(t, "lina") }\nfunc buildCommand(t *testing.T, name string) {}\n',
  })) sb.write(sb.dir, path, source);
  const scope = { components: true, selectedComponents: ['kit', 'lina'], reason: 'manual-full', changed: [], automation: true };
  const full = planComponents({ cwd: sb.dir, scope, unitCount: 2, e2eCounts: { m1: 2 } });
  assert.equal(full.tasks.filter(task => task.kind === 'build').length, 1);
  const units = full.tasks.filter(task => task.kind === 'unit').flatMap(task => task.components.flatMap(component => component.packages));
  assert.deepEqual(units.sort(), ['example.invalid/lina', 'example.invalid/lina/cmd/lina', 'example.invalid/lina/internal/core/records', 'example.invalid/lina/kit']);
  const acceptance = full.tasks.filter(task => task.kind === 'e2e');
  assert.equal(acceptance.length, 2);
  assert.deepEqual(acceptance.flatMap(task => task.files).sort(), ['e2e/m1/one_test.go', 'e2e/m1/two_test.go']);
  assert.deepEqual(acceptance.flatMap(task => task.tests).sort(), ['TestOne', 'TestTwo']);
  const changed = planComponents({ cwd: sb.dir, scope: { ...scope, reason: 'component-change', automation: false,
    changed: ['e2e/m1/one_test.go'] } });
  assert.deepEqual(changed.tasks.filter(task => task.kind === 'e2e').flatMap(task => task.tests), ['TestOne']);
  sb.write(sb.dir, 'scripts/verify/test-times.json', '{"schema":2,"packages":{},"scenarios":{}}\n');
  assert.throws(() => planComponents({ cwd: sb.dir, scope }), /timing/i);
});
