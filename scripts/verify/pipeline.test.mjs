// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import test from 'node:test';
import { developmentRepository, sandbox } from '../test-fixture.mjs';
import { selectFoundation, finishFoundation } from '../foundation.mjs';
import { aggregate, executeTask, runPipeline, sealPlan, validatePlan, writeJSON } from './pipeline.mjs';

function fixture(t) {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const base = sb.g(work, 'rev-parse', 'HEAD');
  sb.commit(work, 'docs: fixture change', { 'README.md': '# Changed fixture\n' });
  const { plan } = selectFoundation({ cwd: work, range: base + '..HEAD', log() {} });
  assert.equal(plan.selection.result, 'success');
  assert.deepEqual(plan.tasks.map(task => task.id), ['docs']);
  return { sb, work, plan };
}

test('selection, shard execution and aggregation share one exact candidate', async t => {
  const { sb, work, plan } = fixture(t);
  const record = await executeTask(work, plan, 'docs', () => {});
  assert.equal(record.result, 'success');
  assert.equal(record.head, plan.head);
  assert.equal(record.digest, plan.digest);
  assert.equal(sb.g(work, 'for-each-ref', 'refs/notes/foundation'), '');
  const evidence = finishFoundation(work, plan, aggregate(plan, [record]), { note: false });
  assert.equal(evidence.result, 'success');
  assert.equal(evidence.checks.components.result, 'skipped');
  assert.equal(evidence.note, 'not written');
});

test('missing, stale, duplicate, skipped and cancelled shard results cannot pass', async t => {
  const { work, plan } = fixture(t);
  const record = await executeTask(work, plan, 'docs', () => {});
  const failing = [
    [], [record, record], [record, { ...record, id: 'unexpected' }],
    [{ ...record, head: 'f'.repeat(40) }], [{ ...record, tree: 'f'.repeat(40) }],
    [{ ...record, digest: 'f'.repeat(64) }],
    ...['failure', 'cancelled', 'skipped', 'pending'].map(result => [{ ...record, result }]),
  ];
  for (const records of failing) {
    assert.equal(finishFoundation(work, plan, aggregate(plan, records), { note: false }).result, 'failure');
  }
  for (const upstream of ['failure', 'cancelled', 'skipped']) {
    assert.equal(finishFoundation(work, plan, aggregate(plan, [record], upstream), { note: false }).result, 'failure');
  }
});

test('tampered plans, absent build checks and changed checkouts fail before execution', t => {
  const { sb, work, plan } = fixture(t);
  assert.throws(() => validatePlan({ ...plan, tasks: [] }, work));
  assert.throws(() => validatePlan(sealPlan({ ...plan, tasks: [] }), work));
  const missingBuild = sealPlan({ ...plan,
    scope: { ...plan.scope, components: true, selectedComponents: ['kit'], componentShards: ['unit-1'] },
    tasks: [...plan.tasks, { id: 'unit-1', kind: 'unit', components: [{ name: 'kit', module: 'kit', packages: ['./...'] }] }],
  });
  assert.throws(() => validatePlan(missingBuild, work));
  const escapedPackage = sealPlan({ ...missingBuild,
    scope: { ...missingBuild.scope, componentShards: ['build', 'unit-1'] },
    tasks: [...plan.tasks,
      { id: 'build', kind: 'build', components: [{ name: 'kit', module: 'kit', packages: ['./...'] }] },
      { id: 'unit-1', kind: 'unit', components: [{ name: 'kit', module: 'kit', packages: ['../other'] }] }],
  });
  assert.throws(() => validatePlan(escapedPackage, work));
  sb.commit(work, 'docs: another candidate', { 'README.md': '# New candidate\n' });
  assert.throws(() => validatePlan(plan, work));
});

test('a commit changed during verification cannot receive a passing aggregate or note', async t => {
  const { sb, work, plan } = fixture(t);
  const record = await executeTask(work, plan, 'docs', () => {});
  sb.commit(work, 'docs: moved while checking', { 'README.md': '# Moved candidate\n' });
  const evidence = finishFoundation(work, plan, aggregate(plan, [record]));
  assert.equal(evidence.result, 'failure');
  assert.equal(evidence.note, 'not written');
  assert.equal(sb.g(work, 'for-each-ref', 'refs/notes/foundation'), '');
});

test('a successful Go command cannot conceal an e2e TestMain that bypasses selected tests', async t => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const toolchain = 'go 1.27\ntoolchain go1.27.3\n';
  sb.commit(work, 'test: seed a test main that omits the suite', {
    'go.mod': 'module example.invalid/lina\n' + toolchain,
    'kit/go.mod': 'module example.invalid/lina/kit\n' + toolchain,
    'go.work': toolchain + 'use (\n.\n./kit\n)\n',
    'doc.go': 'package lina\n', 'kit/doc.go': 'package kit\n',
    'cmd/lina/main.go': 'package main\nfunc main() {}\n',
    'e2e/scenario_test.go': 'package e2e\nimport ("os"; "testing")\nfunc TestMain(m *testing.M) { os.Exit(0) }\nfunc TestOne(t *testing.T) {}\n',
  });
  const { plan } = selectFoundation({ cwd: work, full: true, log() {} });
  assert.equal(plan.selection.result, 'success');
  const task = plan.tasks.find(task => task.kind === 'e2e');
  assert.deepEqual(task.tests, ['TestOne']);
  const logs = [];
  const result = await executeTask(work, plan, task.id, output => logs.push(output));
  assert.equal(result.result, 'failure');
  assert.ok(logs.some(output => output.includes('Go omitted selected work: TestOne')));
});

test('local workers overlap other checks while retaining one four-scenario e2e process', async t => {
  const { work, plan: docs } = fixture(t);
  const component = { name: 'kit', module: 'kit', packages: ['./...'] };
  const tasks = [
    ...docs.tasks, { id: 'build', kind: 'build', components: [component] },
    ...[1, 2].map(number => ({ id: 'e2e-m1-' + number, kind: 'e2e', tests: ['TestOne'],
      testPattern: '^(TestOne)$', components: [{ ...component, packages: ['example.invalid/acceptance' + number] }] })),
    { id: 'unit-1', kind: 'unit', components: [{ ...component, packages: ['example.invalid/kit'] }] },
  ];
  const plan = sealPlan({ ...docs, tasks,
    scope: { ...docs.scope, mode: 'full', components: true, selectedComponents: ['kit'],
      componentShards: tasks.filter(task => task.id !== 'docs').map(task => task.id) } });
  let release;
  const unitStarted = new Promise(resolve => { release = resolve; });
  let active = 0, overlap = false, unitDuringAcceptance = false;
  const result = await runPipeline(work, plan, { jobs: 2, log() {}, async run(command, args) {
    const id = args[args.indexOf('--shard') + 1];
    const task = tasks.find(task => task.id === id);
    if (task.kind === 'e2e') {
      active++;
      if (active > 1) { overlap = true; release(); }
      await unitStarted;
      active--;
    } else if (task.kind === 'unit') {
      unitDuringAcceptance = active > 0;
      release();
    }
    writeJSON(args[args.indexOf('--out') + 1], {
      schema: 1, id, kind: task.kind, head: plan.head, tree: plan.tree, digest: plan.digest,
      result: 'success', started: 1, finished: 2, seconds: 0.001,
    });
    return { status: 0, output: '' };
  } });
  assert.equal(result.checks.components.result, 'success');
  assert.equal(overlap, false);
  assert.equal(unitDuringAcceptance, true);
});

test('an actual failing Go scenario publishes the diagnostic for post-merge QA', async t => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const toolchain = 'go 1.27\ntoolchain go1.27.3\n';
  sb.commit(work, 'test: seed failing acceptance', {
    'go.mod': 'module example.invalid/lina\n' + toolchain,
    'kit/go.mod': 'module example.invalid/lina/kit\n' + toolchain,
    'go.work': toolchain + 'use (\n.\n./kit\n)\n',
    'doc.go': 'package lina\n', 'kit/doc.go': 'package kit\n',
    'cmd/lina/main.go': 'package main\nfunc main() {}\n',
    'e2e/scenario_test.go': 'package e2e\nimport "testing"\nfunc TestOne(t *testing.T) { t.Run("child", func(t *testing.T) { t.Fatal("forced QA diagnostic") }) }\n',
  });
  const { plan } = selectFoundation({ cwd: work, full: true, event: 'push', log() {} });
  assert.equal(plan.selection.result, 'success');
  const task = plan.tasks.find(task => task.kind === 'e2e');
  const record = await executeTask(work, plan, task.id, () => {});
  assert.equal(record.result, 'failure');
  assert.equal(record.tests.length, 1);
  assert.equal(record.tests[0].test, 'TestOne');
  assert.equal(record.tests[0].result, 'fail');
  assert.match(record.tests[0].output, /scenario_test.go:3: forced QA diagnostic/);
});
