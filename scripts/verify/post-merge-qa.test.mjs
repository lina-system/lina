// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { sandbox } from '../test-fixture.mjs';
import { collectObservations, reconcileQA } from './post-merge-qa.mjs';
import { goTestOutcomes, sealPlan } from './pipeline.mjs';

const head = n => String(n).repeat(40);
function fixture(result = 'fail', n = 1) {
  const plan = sealPlan({ schema: 2, head: head(n), tree: head(8), selection: { result: 'success' },
    scope: { head: head(n), reason: 'manual-full', event: 'push' }, tasks: [{ id: 'e2e-m1-1', kind: 'e2e',
      tests: ['TestOne', 'TestTwo'], components: [{ packages: ['example/e2e'] }] }] });
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
  const runs = [];
  const artifacts = new Map();
  const contents = new Map();
  const publish = (input, { id = Number(input.plan.head[0]), branch = 'dev', event = 'push', expired = false } = {}) => {
    runs.push({ id, head_sha: input.plan.head, head_branch: branch, event, status: 'completed', run_attempt: 1 });
    const files = [{ id: id * 100, name: 'foundation-plan', expired }];
    if (input.records.length) files.push({ id: id * 100 + 1, name: 'foundation-result-e2e-m1-1-attempt-1', expired });
    artifacts.set(id, files);
    contents.set(files[0].id, input.plan);
    if (input.records.length) contents.set(files[1].id, input.records[0]);
    return files;
  };
  const api = async (method, path, body) => {
    if (method === 'GET' && path.includes('/actions/')) {
      if (path.includes('/workflows/ci.yml/runs?')) return { workflow_runs: runs };
      const match = path.match(/\/actions\/runs\/(\d+)(\/artifacts)?/);
      assert.ok(match, path);
      if (match[2]) return { artifacts: artifacts.get(Number(match[1])) ?? [] };
      return { created_at: '2026-10-23T00:00:00Z', head_sha: head(1) };
    }
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
  return { issues, writes, api, publish, contents,
    readArtifact: async artifact => structuredClone(contents.get(artifact.id)) };
}
const options = mock => ({ api: mock.api, repository: 'owner/repo', milestone: 3, branch: 'dev',
  runId: 1, readArtifact: mock.readArtifact,
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

test('abnormal package failure reports only started unfinished tests with bounded diagnostics', () => {
  const events = [
    { Action: 'run', Package: 'example/e2e', Test: 'TestPassed' },
    { Action: 'pass', Package: 'example/e2e', Test: 'TestPassed' },
    { Action: 'skip', Package: 'example/e2e', Test: 'TestSkipped' },
    { Action: 'run', Package: 'other/e2e', Test: 'TestOther' },
    { Action: 'run', Package: 'example/e2e', Test: 'TestOne' },
    { Action: 'run', Package: 'example/e2e', Test: 'TestOne/child' },
    { Action: 'run', Package: 'example/e2e', Test: 'TestTwo' },
    { Action: 'output', Package: 'example/e2e', Output: 'x'.repeat(20000) + '\npanic: test timed out\n' },
    { Action: 'fail', Package: 'example/e2e' },
  ];
  const outcomes = goTestOutcomes(events.map(event => JSON.stringify(event)).join('\n'));
  assert.deepEqual(outcomes.map(({ test, result }) => [test, result]), [
    ['TestPassed', 'pass'], ['TestSkipped', 'skip'], ['TestOther', 'unknown'], ['TestOne', 'fail'], ['TestTwo', 'fail'],
  ]);
  for (const outcome of outcomes.filter(outcome => outcome.result === 'fail')) {
    assert.match(outcome.output, /panic: test timed out/);
    assert.ok(outcome.output.length <= 16000);
  }
});

test('a package timeout leaves paused scenarios unknown instead of opening QA issues', async () => {
  // Reduced from the #985 timeout: serial work consumes the package budget
  // while an admission scenario waits at t.Parallel, including its child.
  for (const nested of [false, true]) {
    const events = [
      { Action: 'run', Test: 'TestTwo' },
      { Action: 'pause', Test: 'TestTwo' },
      ...(nested ? [
        { Action: 'cont', Test: 'TestTwo' },
        { Action: 'run', Test: 'TestTwo/approved-before-restart' },
        { Action: 'pause', Test: 'TestTwo/approved-before-restart' },
      ] : []),
      { Action: 'run', Test: 'TestOne' },
      { Action: 'output', Test: 'TestTwo', Output: 'waiting for a parallel slot\n' },
      { Action: 'output', Output: 'panic: test timed out\n' },
      { Action: 'fail' },
    ].map(event => JSON.stringify({ Package: 'example/e2e', ...event })).join('\n');
    const input = fixture();
    input.records[0].tests = goTestOutcomes(events);
    assert.deepEqual(input.records[0].tests.map(({ test, result }) => [test, result]), [
      ['TestTwo', 'unknown'], ['TestOne', 'fail'],
    ], `nested=${nested}`);
    const mock = github();
    await reconcileQA({ ...options(mock), ...input });
    assert.equal(mock.issues.length, 1);
    assert.match(mock.issues[0].title, /TestOne/);
    // Unknown is not recovery evidence for an already open finding either.
    const earlier = fixture();
    earlier.records[0].tests[1].result = 'fail';
    await reconcileQA({ ...options(mock), ...earlier });
    const later = fixture('fail', 2);
    later.records[0].tests = input.records[0].tests;
    await reconcileQA({ ...options(mock), ...later });
    assert.equal(mock.issues[1].state, 'open');
  }
});

test('a real Go timeout reaches QA reporting without inventing unexecuted tests', async t => {
  const sb = sandbox(t);
  sb.write(sb.dir, 'go.mod', 'module example/e2e\n\ngo 1.24\n');
  sb.write(sb.dir, 'timeout_test.go', `// SPDX-License-Identifier: Apache-2.0
package e2e
import "testing"
func TestAPaused(t *testing.T) { t.Parallel(); t.Fatal("must not run") }
func TestOne(t *testing.T) { t.Log("waiting for timeout"); select {} }
func TestTwo(t *testing.T) { t.Fatal("must not run") }
`);
  const run = spawnSync('go', ['test', '-json', '-count=1', '-timeout=1s', '.'], {
    cwd: sb.dir, env: { PATH: process.env.PATH, HOME: sb.dir, GOWORK: 'off', GOENV: 'off',
      GOCACHE: process.env.GOCACHE, GOTOOLCHAIN: 'local' }, encoding: 'utf8', timeout: 120000,
  });
  assert.equal(run.error, undefined);
  assert.equal(run.status, 1, run.stderr);
  assert.match(run.stdout, /panic: test timed out/);
  const input = fixture();
  input.records[0].tests = goTestOutcomes(run.stdout);
  assert.deepEqual(input.records[0].tests.map(({ test, result }) => [test, result]),
    [['TestAPaused', 'unknown'], ['TestOne', 'fail']]);
  input.plan.tasks[0].tests.push('TestAPaused');
  const mock = github();
  await reconcileQA({ ...options(mock), ...input });
  assert.equal(mock.issues.length, 1);
  assert.match(mock.issues[0].body, /panic: test timed out/);
  assert.match(mock.issues[0].body, /timeout_test.go/);
});

test('package timeout leaves paused parallel scenarios unknown, including nested quota work', async () => {
  const events = [];
  const emit = (Action, Test, Output) => events.push({ Action, Package: 'example/e2e', Test, Output });
  emit('run', 'TestPaused');
  emit('pause', 'TestPaused');
  emit('run', 'TestQuota');
  emit('pause', 'TestQuota');
  emit('cont', 'TestQuota');
  emit('run', 'TestQuota/success');
  emit('pause', 'TestQuota/success');
  emit('run', 'TestActive');
  emit('pause', 'TestActive');
  emit('cont', 'TestActive');
  emit('run', 'TestNested');
  emit('run', 'TestNested/group');
  emit('run', 'TestNested/group/waiting');
  emit('pause', 'TestNested/group/waiting');
  emit('run', 'TestNested/group/active');
  emit('pause', 'TestNested/group/active');
  emit('cont', 'TestNested/group/active');
  emit('run', 'TestFailed');
  emit('run', 'TestFailed/failed');
  emit('fail', 'TestFailed/failed');
  emit('run', 'TestFailed/waiting');
  emit('pause', 'TestFailed/waiting');
  emit('run', 'TestFinished');
  emit('pass', 'TestFinished');
  emit('run', 'TestCleanup');
  emit('run', 'TestCleanup/finished');
  emit('pass', 'TestCleanup/finished');
  emit('run', 'TestPartial');
  emit('run', 'TestPartial/finished');
  emit('pass', 'TestPartial/finished');
  emit('run', 'TestPartial/waiting');
  emit('pause', 'TestPartial/waiting');
  emit('output', undefined, 'panic: test timed out\n');
  emit('fail');
  const outcomes = goTestOutcomes(events.map(event => JSON.stringify(event)).join('\n'));
  assert.deepEqual(outcomes.map(({ test, result }) => [test, result]), [
    ['TestPaused', 'unknown'], ['TestQuota', 'unknown'], ['TestActive', 'fail'],
    ['TestNested', 'fail'], ['TestFailed', 'fail'], ['TestFinished', 'pass'],
    ['TestCleanup', 'fail'], ['TestPartial', 'unknown'],
  ]);
  const input = fixture();
  input.plan.tasks[0].tests = outcomes.map(outcome => outcome.test);
  input.records[0].tests = outcomes;
  const mock = github();
  await reconcileQA({ ...options(mock), ...input });
  assert.deepEqual(mock.issues.map(issue => JSON.parse(issue.body.match(/foundation-qa:(\{[^\n]+\})/)[1]).test),
    ['TestActive', 'TestNested', 'TestFailed', 'TestCleanup']);
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
  const interrupted = fixture('unknown', 2);
  interrupted.records[0].result = 'failure';
  await reconcileQA({ ...opts, ...interrupted });
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

test('a newer completed pass before the first issue prevents a late older failure from opening it', async () => {
  const mock = github();
  const newer = fixture('pass', 2);
  await reconcileQA({ ...options(mock), ...newer });
  assert.equal(mock.issues.length, 0);
  mock.publish(newer);
  await reconcileQA({ ...options(mock), ...fixture() });
  assert.equal(mock.issues.length, 0, 'the completed descendant run already proves recovery');
});

test('only a later descendant pass for the same branch and test proves recovery', async () => {
  for (const variant of ['same-head', 'other-branch', 'unrelated', 'skip', 'expired', 'missing']) {
    const mock = github();
    const input = fixture(variant === 'skip' ? 'skip' : 'pass', variant === 'same-head' ? 1 : 2);
    if (variant === 'missing') input.records = [];
    mock.publish(input, { id: 2, branch: variant === 'other-branch' ? 'qa/e2e-other' : 'dev', expired: variant === 'expired' });
    await reconcileQA({ ...options(mock), ...fixture(),
      ...(variant === 'unrelated' ? { isAncestor: async () => false } : {}) });
    assert.equal(mock.issues.length, 1, variant);
    assert.equal(mock.issues[0].state, 'open', variant);
  }
});

test('the latest terminal observation and shard attempt take precedence over older passes', async () => {
  for (const rerun of [false, true]) {
    const mock = github();
    const files = mock.publish(fixture('pass', 2));
    if (rerun) {
      files.push({ id: 299, name: 'foundation-result-e2e-m1-1-attempt-2', expired: false });
      mock.contents.set(299, fixture('fail', 2).records[0]);
    } else mock.publish(fixture('fail', 3));
    await reconcileQA({ ...options(mock), ...fixture() });
    assert.equal(mock.issues.length, 1);
    assert.equal(mock.issues[0].state, 'open');
  }
});

test('stale historical results cannot suppress a QA failure', async () => {
  const mock = github();
  const input = fixture('pass', 2);
  input.records[0].digest = 'stale';
  mock.publish(input);
  await assert.rejects(reconcileQA({ ...options(mock), ...fixture() }), /result/i);
  assert.equal(mock.writes.length, 0);
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
