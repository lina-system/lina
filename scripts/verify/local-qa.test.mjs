// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { once } from 'node:events';
import { existsSync, linkSync, mkdirSync, readFileSync, symlinkSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { developmentRepository, sandbox } from '../test-fixture.mjs';
import { executeLocalRun, readLocalHistory, readSandboxJSON, runLocalQA, runLocalShards } from './local-qa.mjs';

function fixture(t) {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  sb.commit(work, 'test: ready', { 'docs/design/ready.md': '# Ready\n' });
  sb.g(work, 'push', '-q', 'origin', 'dev');
  const stateFile = join(sb.dir, 'qa/state.json');
  const executed = [];
  const reported = [];
  let time = 1_800_000;
  const options = { cwd: work, stateFile, repository: 'owner/repo', milestone: 1,
    now: () => time, execute: async input => { executed.push(input); return { result: 'success' }; },
    report: async input => { reported.push(input); return []; } };
  const advance = () => {
    const head = sb.commit(work, 'test: advance dev', { 'README.md': '# Head ' + time + '\n' });
    sb.g(work, 'push', '-q', 'origin', 'dev');
    return head;
  };
  return { sb, work, stateFile, executed, reported, options, advance, tick: ms => { time += ms; } };
}

test('unchanged head never executes again even after the interval', async t => {
  const f = fixture(t);
  assert.equal((await runLocalQA(f.options)).status, 'tested');
  f.tick(1_800_000);
  assert.equal((await runLocalQA(f.options)).status, 'unchanged');
  assert.equal(f.executed.length, 1);
  assert.equal(f.reported.length, 1);
  assert.equal(JSON.parse(readFileSync(f.stateFile)).lastHead, f.executed[0].localRun.head);
});

test('new heads within the interval wait and only the newest merge runs', async t => {
  const f = fixture(t);
  await runLocalQA(f.options);
  f.tick(1000);
  const intermediate = f.advance();
  assert.equal((await runLocalQA(f.options)).status, 'interval');
  f.tick(1000);
  const newest = f.advance();
  assert.equal((await runLocalQA(f.options)).status, 'interval');
  f.tick(1_798_000);
  await runLocalQA(f.options);
  assert.deepEqual(f.executed.map(item => item.localRun.head), [f.executed[0].localRun.head, newest]);
  assert.ok(!f.executed.some(item => item.localRun.head === intermediate));
});

test('execution uses a clean exact-head worktree and cleans it without touching caller changes', async t => {
  const f = fixture(t);
  f.sb.write(f.work, 'private.txt', 'caller data');
  await runLocalQA({ ...f.options, execute: async input => {
    f.executed.push(input);
    assert.equal(f.sb.g(input.worktree, 'status', '--porcelain'), '');
    assert.equal(f.sb.g(input.worktree, 'rev-parse', 'HEAD'), input.localRun.head);
    assert.ok(!existsSync(join(input.worktree, 'private.txt')));
    return { result: 'success' };
  } });
  assert.ok(!existsSync(f.executed[0].worktree));
  assert.equal(readFileSync(join(f.work, 'private.txt'), 'utf8'), 'caller data');
});

test('a reporting retry never reruns shards and an interrupted attempt consumes its head', async t => {
  const f = fixture(t);
  await assert.rejects(runLocalQA({ ...f.options, report: async () => { throw new Error('offline'); } }), /offline/);
  await runLocalQA(f.options);
  assert.equal(f.executed.length, 1);
  assert.equal(f.reported.length, 1);
  f.tick(1_800_000);
  f.advance();
  await assert.rejects(runLocalQA({ ...f.options, execute: async () => { throw new Error('interrupted'); } }), /interrupted/);
  f.tick(1_800_000);
  assert.equal((await runLocalQA(f.options)).status, 'unchanged');
});

test('a killed process leaves a consumed head and never supplies incomplete history', async t => {
  const f = fixture(t);
  await runLocalQA(f.options);
  const state = JSON.parse(readFileSync(f.stateFile));
  state.pending = state.lastHead;
  const manifestFile = join(f.sb.dir, 'qa/runs', state.lastHead, 'run.json');
  const manifest = JSON.parse(readFileSync(manifestFile));
  writeFileSync(manifestFile, JSON.stringify({ ...manifest, phase: 'running' }));
  writeFileSync(f.stateFile, JSON.stringify(state));
  f.tick(1_800_000);
  assert.equal((await runLocalQA(f.options)).status, 'unchanged');
  assert.equal(f.executed.length, 1);
  assert.equal(f.reported.length, 1);
  assert.deepEqual(readLocalHistory(join(f.sb.dir, 'qa/runs'), f.work), []);
  assert.equal(JSON.parse(readFileSync(f.stateFile)).pending, undefined);
});

test('the CLI exits busy while another invocation holds the state lock', { skip: process.platform !== 'linux' }, async t => {
  const f = fixture(t);
  mkdirSync(join(f.sb.dir, 'qa'));
  const holder = spawn('flock', [f.stateFile + '.lock', process.execPath, '-e',
    "process.stdout.write('locked'); process.stdin.resume()"], { stdio: ['pipe', 'pipe', 'pipe'] });
  t.after(() => holder.stdin.end());
  await once(holder.stdout, 'data');
  const child = spawn(process.execPath, [fileURLToPath(new URL('./local-qa.mjs', import.meta.url)), '--state', f.stateFile], {
    cwd: f.work, stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  child.stdout.on('data', data => { output += data; });
  assert.deepEqual(await once(child, 'close'), [0, null]);
  assert.equal(JSON.parse(output).status, 'busy');
  assert.ok(!existsSync(f.stateFile));
  holder.stdin.end();
  await once(holder, 'close');
});

test('all shards execute with bounded parallelism even after a failure', async () => {
  const plan = { tasks: ['docs', 'unit-1', 'e2e-m1-1', 'e2e-m2-1'].map(id => ({ id })) };
  const calls = [];
  let active = 0;
  let peak = 0;
  const releases = [];
  let changed;
  const run = runLocalShards(plan, 2, async (args, id) => {
    active++;
    peak = Math.max(peak, active);
    calls.push({ args, id });
    changed?.();
    await new Promise(resolve => releases.push(resolve));
    active--;
    return id === 'docs' ? 1 : 0;
  });
  while (calls.length < plan.tasks.length) {
    const next = new Promise(resolve => { changed = resolve; });
    releases.shift()();
    await next;
  }
  for (const release of releases) release();
  assert.equal(await run, 'failure');
  assert.equal(peak, 2);
  assert.deepEqual(calls.map(call => call.id), plan.tasks.map(task => task.id));
  for (const { args, id } of calls) assert.deepEqual(args,
    ['--plan', '/qa/plan.json', '--shard', id, '--out', '/output/' + id + '.json']);
});

test('only bounded regular sandbox output is imported, without following links or blocking on FIFOs', { skip: process.platform !== 'linux' }, t => {
  const sb = sandbox(t);
  const target = join(sb.dir, 'target.json');
  writeFileSync(target, '{"valid":true}');
  assert.deepEqual(readSandboxJSON(target), { valid: true });
  const symbolic = join(sb.dir, 'symbolic.json');
  symlinkSync(target, symbolic);
  assert.throws(() => readSandboxJSON(symbolic), { code: 'ELOOP' });
  const hard = join(sb.dir, 'hard.json');
  linkSync(target, hard);
  assert.throws(() => readSandboxJSON(hard), /regular file/);
  const fifo = join(sb.dir, 'fifo.json');
  assert.equal(spawnSync('mkfifo', [fifo]).status, 0);
  assert.throws(() => readSandboxJSON(fifo), /regular file/);
});

test('a rejected shard output still waits for every independent worker before cleanup', async () => {
  const plan = { tasks: ['one', 'two', 'three'].map(id => ({ id })) };
  const executed = [];
  await assert.rejects(runLocalShards(plan, 2, async (_args, id) => {
    executed.push(id);
    if (id === 'one') throw new Error('unsafe output');
    return 0;
  }), /unsafe output/);
  assert.deepEqual(executed.sort(), ['one', 'three', 'two']);
});

test('the real sandbox selects and runs a clean synthetic candidate without host credentials', { skip: process.platform !== 'linux' }, async t => {
  const f = fixture(t);
  const secret = join(f.sb.dir, 'operator-secret');
  f.sb.write(f.sb.dir, 'operator-secret', 'private fixture');
  f.sb.commit(f.work, 'test: sandbox boundary', { 'scripts/boundary.test.mjs': `// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
assert.equal(process.env.GH_TOKEN, undefined);
assert.equal(existsSync(${JSON.stringify(secret)}), false);
assert.equal(process.env.HOME, '/home/qa');
const profile = spawnSync('apparmor_parser', ['--skip-kernel-load', '--skip-cache',
  'scripts/verify/bwrap.apparmor'], { encoding: 'utf8' });
assert.equal(profile.status, 0, profile.stderr);
` });
  f.sb.g(f.work, 'push', '-q', 'origin', 'dev');
  const result = await runLocalQA({ ...f.options, execute: executeLocalRun, report: async ({ root, runDirectory }) => {
    const history = readLocalHistory(join(runDirectory, '..'), root);
    assert.equal(history.length, 1);
    assert.equal(history[0].plan.scope.event, 'schedule');
    assert.equal(history[0].plan.scope.reason, 'manual-full');
    assert.equal(history[0].localRun.started, history[0].plan.started);
    assert.deepEqual(history[0].records.map(record => record.id).sort(), history[0].plan.tasks.map(task => task.id).sort());
    assert.ok(history[0].records.every(record => record.result === 'success'),
      readFileSync(join(runDirectory, 'automation.log'), 'utf8'));
    return [];
  } });
  assert.equal(result.result, 'success', readFileSync(result.logPath, 'utf8'));
  assert.equal(readFileSync(secret, 'utf8'), 'private fixture');
});

test('sandbox-created links cannot redirect host metadata writes', { skip: process.platform !== 'linux' }, async t => {
  const f = fixture(t);
  const victim = join(f.sb.dir, 'operator-file');
  writeFileSync(victim, 'unchanged operator data');
  f.sb.commit(f.work, 'test: hostile output links', { 'scripts/links.test.mjs': `// SPDX-License-Identifier: Apache-2.0
import { existsSync, symlinkSync } from 'node:fs';
const output = existsSync('/output') ? '/output' : '/qa';
for (const name of ['run.json.tmp', 'foundation.json.tmp', 'run.log']) {
  symlinkSync(${JSON.stringify(victim)}, output + '/' + name);
}
` });
  f.sb.g(f.work, 'push', '-q', 'origin', 'dev');
  await runLocalQA({ ...f.options, execute: executeLocalRun });
  assert.equal(readFileSync(victim, 'utf8'), 'unchanged operator data');
});
