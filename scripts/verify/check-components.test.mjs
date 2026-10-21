// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { sandbox } from '../test-fixture.mjs';
import { checkComponents, execute } from './check-components.mjs';

const source = fileURLToPath(new URL('../../', import.meta.url));
function fixture(t) {
  const sb = sandbox(t);
  for (const name of ['go.mod', 'go.sum', 'go.work', 'kit/go.mod']) {
    sb.write(sb.dir, name, readFileSync(join(source, name)));
  }
  sb.write(sb.dir, 'doc.go', 'package lina\n');
  sb.write(sb.dir, 'kit/doc.go', 'package kit\n');
  return sb;
}
const kit = { name: 'kit', module: 'kit', packages: ['./...'] };

test('a progressing test command can outlive the old per-command limit', t => {
  const sb = fixture(t);
  // Scale the legacy 120 s limit to 1 s; progress continues for 2 s.
  // All other commands are deterministic stand-ins, leaving the real runner
  // to exercise the selected test command's budget.
  const args = ['-e', `
    const progress = setInterval(() => console.log('package ok'), 50);
    setTimeout(() => { clearInterval(progress); console.log('finished'); }, 2000);
  `];
  const result = checkComponents({ cwd: sb.dir, components: [kit], execute(command, goArgs, options) {
    if (goArgs[0] === 'test') return execute(process.execPath, args, {
      ...options, timeoutMs: Math.ceil((options.timeoutMs ?? 120_000) / 120),
    });
    return { status: 0, stdout: goArgs[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
  } });
  assert.equal(result.status, 0, result.output);
  assert.match(result.output, /package ok[\s\S]*finished/);
});

test('a hung command fails with its name and elapsed time', t => {
  const sb = sandbox(t);
  sb.write(sb.dir, 'hung.mjs', `// SPDX-License-Identifier: Apache-2.0
setInterval(() => {}, 1000);
`);
  const result = execute(process.execPath, ['hung.mjs'], { cwd: sb.dir, env: sb.env, timeoutMs: 200 });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /command timed out after [\d.]+ s.*hung\.mjs/);
});

test('commands across modules share a deadline instead of resetting the budget', t => {
  const sb = fixture(t);
  let elapsed = 0;
  t.mock.method(performance, 'now', () => elapsed);
  const budgets = [];
  const result = checkComponents({ cwd: sb.dir, components: [kit, { name: 'lina', module: '.', packages: ['.'] }],
    timeoutMs: 20_000, execute(command, args, options) {
      budgets.push(options.timeoutMs);
      elapsed += 1000;
      return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
    } });
  assert.equal(result.status, 0, result.output);
  assert.equal(budgets.length, 11);
  assert.deepEqual(budgets, Array.from({ length: 11 }, (_, i) => 20_000 - i * 1000));
});

test('an exhausted components budget stops before starting another command', t => {
  const sb = fixture(t);
  let elapsed = 0;
  t.mock.method(performance, 'now', () => elapsed);
  const calls = [];
  const result = checkComponents({ cwd: sb.dir, components: [kit], timeoutMs: 1000,
    execute(command, args) {
      calls.push(args);
      elapsed = 1000;
      return { status: 0, stdout: 'go1.27.3\n', stderr: '' };
    } });
  assert.equal(result.status, 1);
  assert.deepEqual(calls, [['env', 'GOVERSION']]);
  assert.match(result.output, /components budget exhausted after 1\.000 s before: go mod verify/);
});

test('a hung test command is capped by the remaining components budget', t => {
  const sb = fixture(t);
  sb.write(sb.dir, 'hung.mjs', `// SPDX-License-Identifier: Apache-2.0
process.on('SIGTERM', () => {});
setInterval(() => {}, 1000);
`);
  const result = checkComponents({ cwd: sb.dir, components: [kit], timeoutMs: 500,
    execute(command, args, options) {
      if (args[0] === 'test') return execute(process.execPath, ['hung.mjs'], { ...options, cwd: sb.dir });
      return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
    } });
  assert.equal(result.status, 1);
  assert.match(result.output, /\$ go test -count=1 -mod=readonly \.\/\.\.\./);
  assert.match(result.output, /command timed out after [\d.]+ s \(budget 0\.\d+ s\): .*hung\.mjs/);
});

test('components use the pinned toolchain, readonly commands, module verification and uncached tests', t => {
  const sb = fixture(t);
  const calls = [];
  const result = checkComponents({ cwd: sb.dir, components: [kit], execute(command, args, options) {
    calls.push({ command, args, ...options });
    return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
  } });
  assert.equal(result.status, 0, result.output);
  const commands = calls.map(call => call.args);
  assert.deepEqual(commands, [
    ['env', 'GOVERSION'], ['mod', 'verify'], ['mod', 'verify'],
    ['build', '-mod=readonly', './...'],
    ['vet', '-mod=readonly', './...'], ['tool', 'exhaustive', './...'],
    ['test', '-count=1', '-mod=readonly', './...'],
  ]);
  for (const call of calls) {
    assert.equal(call.env.GOTOOLCHAIN, 'go1.27.3');
    assert.equal(call.env.GOFLAGS, '-mod=readonly');
    assert.equal(call.env.CGO_ENABLED, '0');
    assert.equal(call.env.GH_TOKEN, undefined);
  }
  assert.equal(calls[1].cwd, sb.dir);
  for (const call of calls.slice(2)) assert.equal(call.cwd, join(sb.dir, 'kit'));
});

test('a failed Go command stops verification and propagates its diagnostics', t => {
  const sb = fixture(t);
  const calls = [];
  const result = checkComponents({ cwd: sb.dir, components: [kit], execute(command, args) {
    calls.push(args[0]);
    return { status: args[0] === 'vet' ? 1 : 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: args[0] === 'vet' ? 'invalid format\n' : '' };
  } });
  assert.equal(result.status, 1);
  assert.match(result.output, /invalid format/);
  assert.equal(calls.at(-1), 'vet');
});

test('mismatched module or workspace versions fail before any build', t => {
  const sb = fixture(t);
  sb.write(sb.dir, 'kit/go.mod', 'module example.invalid/kit\ngo 1.26\ntoolchain go1.26.1\n');
  assert.throws(() => checkComponents({ cwd: sb.dir, components: [kit] }), /same Go and toolchain/);
});

test('the real pinned analyzer rejects an incomplete enum switch and accepts the fixed package', t => {
  const sb = fixture(t);
  const code = `package kit
 type State int
 const ( Ready State = iota; Stopped )
 func valid(s State) bool { switch s { case Ready: return true; CASE } ; return false }
 `;
  sb.write(sb.dir, 'kit/doc.go', code.replace('CASE', ''));
  const red = checkComponents({ cwd: sb.dir, components: [kit] });
  assert.equal(red.status, 1, red.output);
  assert.match(red.output, /missing cases.*Stopped/s);
  sb.write(sb.dir, 'kit/doc.go', code.replace('CASE', 'case Stopped: return false;'));
  const green = checkComponents({ cwd: sb.dir, components: [kit] });
  assert.equal(green.status, 0, green.output);
});

test('token drift and raw TUI literals stop component verification before builds', t => {
  const sb = fixture(t);
  sb.write(sb.dir, 'design/tokens/light.json', '{}');
  sb.write(sb.dir, 'scripts/tokens.mjs', '// fixture');
  const calls = [];
  const result = checkComponents({ cwd: sb.dir, components: [{name:'lina', module:'.', packages:['.']}], execute(command, args) {
    calls.push({command, args});
    if (args.includes('--check')) return { status:1, stdout:'', stderr:'token violation' };
    return { status:0, stdout:args[0]==='env' ? 'go1.27.3\n' : '', stderr:'' };
  } });
  assert.equal(result.status, 1);
  assert.ok(calls.some(call => call.command === process.execPath && call.args.includes('--check')));
  assert.ok(!calls.some(call => call.args[0]==='build'));
});
