// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';
import { run } from '../git.mjs';
import { developmentRepository, sandbox } from '../test-fixture.mjs';
import { selectFoundation } from '../foundation.mjs';
import { executeTask, readResults, sealPlan, writeJSON } from './pipeline.mjs';

async function fixture(t) {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const base = sb.g(work, 'rev-parse', 'HEAD');
  sb.commit(work, 'docs: fixture change', { 'README.md': '# Changed fixture\n' });
  const { plan: selected } = selectFoundation({ cwd: work, range: base + '..HEAD', log() {} });
  // A second selected shard represents the successful job that is not rerun.
  const plan = sealPlan({ ...selected, scope: { ...selected.scope, automation: true },
    tasks: [...selected.tasks, { id: 'automation', kind: 'automation' }] });
  const record = await executeTask(work, plan, 'docs', () => {});
  assert.equal(record.result, 'success');
  const results = join(sb.dir, 'results');
  const artifact = (id, attempt) => join(results, `foundation-result-${id}-attempt-${attempt}`);
  const put = (id, attempt, overrides = {}) => writeJSON(join(artifact(id, attempt), id + '.json'),
    { ...record, id, kind: id, ...overrides });
  put('automation', 1);
  const planFile = join(sb.dir, 'plan.json');
  writeJSON(planFile, plan);
  const evaluate = (upstream = 'success') => {
    const result = run(process.execPath, [join(work, 'scripts/foundation.mjs'),
      '--plan', planFile, '--results', results, '--upstream', upstream, '--no-note'],
    { cwd: work, env: sb.env });
    const line = result.stdout.trim().split('\n').filter(Boolean).at(-1);
    return { ...result, evidence: line ? JSON.parse(line) : null };
  };
  return { sb, results, record, artifact, put, evaluate };
}

test('foundation CLI accepts a failed-shard rerun and retains jobs not rerun', async t => {
  const { put, evaluate } = await fixture(t);
  put('docs', 1, { result: 'failure' });
  assert.equal(evaluate('failure').status, 1);
  put('docs', 2);
  const passed = evaluate();
  assert.equal(passed.status, 0, passed.stderr);
  assert.equal(passed.evidence.result, 'success');
  assert.equal(passed.evidence.checks.docs.result, 'success');
  assert.equal(passed.evidence.checks.automation.result, 'success');
  assert.equal(evaluate('failure').status, 1, 'upstream failures still block the gate');
});

test('attempt ordering is numeric and a newer failure wins over an older success', async t => {
  const { put, evaluate } = await fixture(t);
  put('docs', 2, { result: 'failure' });
  put('docs', 10);
  assert.equal(evaluate().status, 0);
  put('docs', 11, { result: 'failure' });
  const failed = evaluate();
  assert.equal(failed.status, 1);
  assert.equal(failed.evidence.checks.docs.result, 'failure');
});

test('the latest artifact must match the selected candidate and plan', async t => {
  const { put, evaluate } = await fixture(t);
  put('docs', 1);
  for (const overrides of [{ head: 'f'.repeat(40) }, { tree: 'f'.repeat(40) },
    { digest: 'f'.repeat(64) }, { result: 'skipped' }, { result: 'cancelled' }]) {
    put('docs', 2, overrides);
    const failed = evaluate();
    assert.equal(failed.status, 1);
    assert.ok(failed.evidence.failures.includes('Stale or malformed shard result: docs'));
  }
});

test('missing, malformed, mismatched and duplicate latest artifact contents cannot fall back', async t => {
  const { sb, results, record, artifact, put, evaluate } = await fixture(t);
  put('docs', 1);
  mkdirSync(artifact('docs', 2), { recursive: true });
  assert.equal(evaluate().status, 1);
  sb.write(artifact('docs', 2), 'docs.json', '{');
  assert.equal(evaluate().status, 1);
  put('docs', 2, { id: 'automation', kind: 'automation' });
  assert.equal(evaluate().status, 1);
  put('docs', 2);
  writeJSON(join(artifact('docs', 2), 'duplicate.json'), record);
  assert.equal(evaluate().status, 1);
  rmSync(join(artifact('docs', 2), 'duplicate.json'));
  assert.equal(evaluate().status, 0);
  // Flat local results remain readable and cannot mask a duplicate shard.
  writeJSON(join(results, 'docs.json'), record);
  assert.equal(evaluate().status, 1);
});

test('local flat result files remain supported', t => {
  const sb = sandbox(t);
  const results = join(sb.dir, 'results');
  assert.deepEqual(readResults(results), []);
  const record = { id: 'docs', result: 'success' };
  writeJSON(join(results, 'docs.json'), record);
  assert.deepEqual(readResults(results), [record]);
});

test('CI keeps attempt artifacts separate until foundation selects each latest result', () => {
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const upload = workflow.split('- name: Share the result and package timings')[1].split('\n  foundation:')[0];
  assert.match(upload, /name: foundation-result-\$\{\{ matrix.id \}\}-attempt-\$\{\{ github.run_attempt \}\}/);
  const download = workflow.split('- name: Read shard results')[1].split('- name: Evaluate foundation')[0];
  assert.match(download, /pattern: foundation-result-\*/);
  assert.match(download, /merge-multiple: false/);
});
