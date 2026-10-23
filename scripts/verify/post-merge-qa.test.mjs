// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import test from 'node:test';
import { collectObservations, reconcileQA } from './post-merge-qa.mjs';
import { goTestOutcomes } from './pipeline.mjs';

const head = n => String(n).repeat(40);
function fixture(result = 'fail', n = 1) {
  const plan = { head: head(n), tree: 'tree', digest: 'digest', selection: { result: 'success' },
    scope: { reason: 'manual-full', event: 'push' }, tasks: [{ id: 'e2e-m1-1', kind: 'e2e',
      tests: ['TestOne', 'TestTwo'], components: [{ packages: ['example/e2e'] }] }] };
  const record = { schema: 1, id: 'e2e-m1-1', kind: 'e2e', head: plan.head, tree: plan.tree, digest: plan.digest,
    result: result === 'fail' ? 'failure' : 'success', tests: [
      { package: 'example/e2e', test: 'TestOne', result, output: 'scenario_test.go:12: forced failure\n' },
      { package: 'example/e2e', test: 'TestTwo', result: 'pass', output: '' },
    ] };
  return { plan, records: [record] };
}
function github() {
  const issues = [];
  const writes = [];
  const api = async (method, path, body) => {
    if (method === 'GET') return issues;
    writes.push({ method, path, body });
    if (method === 'POST') {
      const issue = { ...body, number: issues.length + 1, state: 'open' };
      issues.push(issue);
      return issue;
    }
    const issue = issues.find(issue => issue.number === Number(path.split('/').at(-1)));
    Object.assign(issue, body);
    return issue;
  };
  return { issues, writes, api };
}
const options = mock => ({ api: mock.api, repository: 'owner/repo', milestone: 3, branch: 'dev',
  before: head(0), runUrl: 'https://github.com/owner/repo/actions/runs/1',
  isAncestor: async (a, b) => Number(a[0]) <= Number(b[0]) });

test('Go failures retain nested diagnostic lines under the top-level test identity', () => {
  const output = [
    { Action: 'run', Package: 'example/e2e', Test: 'TestOne' },
    { Action: 'output', Package: 'example/e2e', Test: 'TestOne/child', Output: 'scenario.go:12: broken\n' },
    { Action: 'fail', Package: 'example/e2e', Test: 'TestOne/child' },
    { Action: 'fail', Package: 'example/e2e', Test: 'TestOne' },
    { Action: 'skip', Package: 'example/e2e', Test: 'TestTwo' },
  ].map(event => JSON.stringify(event)).join('\n');
  const outcomes = goTestOutcomes(output);
  assert.equal(outcomes.length, 2);
  assert.deepEqual(outcomes[0], { package: 'example/e2e', test: 'TestOne', result: 'fail', output: 'scenario.go:12: broken\n' });
  assert.equal(outcomes[1].result, 'skip');
});

test('failure opens one QA issue, repeats update it, and a later passing head closes it', async () => {
  const mock = github();
  const opts = options(mock);
  await reconcileQA({ ...opts, ...fixture() });
  assert.equal(mock.issues.length, 1);
  assert.deepEqual(mock.issues[0].labels, ['qa']);
  assert.equal(mock.issues[0].milestone, 3);
  assert.match(mock.issues[0].body, /scenario_test.go:12: forced failure/);
  assert.ok(mock.issues[0].body.includes(head(0) + '..' + head(1)));
  await reconcileQA({ ...opts, ...fixture('fail', 2), before: head(1) });
  assert.equal(mock.issues.length, 1);
  assert.ok(mock.issues[0].body.includes(head(0) + '..' + head(1)), 'the first failing range survives updates');
  await reconcileQA({ ...opts, ...fixture('pass', 2) });
  assert.equal(mock.issues[0].state, 'open', 'same-head rerun is not a later dev head');
  await reconcileQA({ ...opts, ...fixture('pass', 3) });
  assert.equal(mock.issues[0].state, 'closed');
  await reconcileQA({ ...opts, ...fixture('fail', 1) });
  assert.equal(mock.issues[0].state, 'closed', 'late older runs cannot reopen a recovered test');
  await reconcileQA({ ...opts, ...fixture('fail', 4), before: head(3) });
  assert.equal(mock.issues.length, 1);
  assert.equal(mock.issues[0].state, 'open');
});

test('missing, skipped or untrusted results do not close QA issues', async () => {
  const mock = github();
  const opts = options(mock);
  await reconcileQA({ ...opts, ...fixture() });
  await reconcileQA({ ...opts, ...fixture('skip', 2) });
  await reconcileQA({ ...opts, ...fixture('pass', 2), records: [] });
  assert.equal(mock.issues[0].state, 'open');
  const invalid = fixture('pass', 2);
  invalid.records[0].digest = 'stale';
  await assert.rejects(reconcileQA({ ...opts, ...invalid }), /result/i);
  assert.equal(mock.issues[0].state, 'open');
  const omitted = fixture('pass', 2);
  omitted.records[0].tests = [];
  assert.throws(() => collectObservations(omitted.plan, omitted.records), /missing/i);
  const unselected = fixture('fail', 2);
  unselected.records[0].tests[0].test = 'TestUnselected';
  assert.throws(() => collectObservations(unselected.plan, unselected.records), /selected/i);
});

test('PR results cannot write QA issues and validation branches cannot close dev issues', async () => {
  const mock = github();
  const opts = options(mock);
  await reconcileQA({ ...opts, ...fixture() });
  await reconcileQA({ ...opts, ...fixture('pass', 2), branch: 'qa/e2e-validation' });
  assert.equal(mock.issues[0].state, 'open');
  const pr = fixture('fail', 2);
  pr.plan.scope.event = 'pull_request';
  await assert.rejects(reconcileQA({ ...opts, ...pr }), /post-merge/i);
});

test('a passing test closes independently of another failing test in its shard', async () => {
  const mock = github();
  const opts = options(mock);
  await reconcileQA({ ...opts, ...fixture() });
  const later = fixture('pass', 2);
  later.records[0].result = 'failure';
  later.records[0].tests[1].result = 'fail';
  await reconcileQA({ ...opts, ...later });
  assert.equal(mock.issues[0].state, 'closed');
  assert.equal(mock.issues[1].state, 'open');
});

test('later passing observations advance a resolved issue watermark', async () => {
  const mock = github();
  const opts = options(mock);
  await reconcileQA({ ...opts, ...fixture() });
  await reconcileQA({ ...opts, ...fixture('pass', 2) });
  await reconcileQA({ ...opts, ...fixture('pass', 4) });
  await reconcileQA({ ...opts, ...fixture('fail', 3) });
  assert.equal(mock.issues[0].state, 'closed');
});


test('a failure on a rerun reopens a finding closed by a pass at the same head', async () => {
  const mock = github();
  const opts = options(mock);
  await reconcileQA({ ...opts, ...fixture() });
  await reconcileQA({ ...opts, ...fixture('pass', 2) });
  assert.equal(mock.issues[0].state, 'closed');
  await reconcileQA({ ...opts, ...fixture('fail', 2) });
  assert.equal(mock.issues[0].state, 'open');
  assert.equal(mock.issues.length, 1);
});
