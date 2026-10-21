// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { sandbox } from '../test-fixture.mjs';
import { checkComponents } from './check-components.mjs';

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
    ['test', '-count=1', '-timeout=4m', '-mod=readonly', './...'],
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

test('module tests retain a diagnostic deadline inside their process budget', t => {
  const sb = fixture(t);
  const calls = [];
  const result = checkComponents({ cwd: sb.dir, components: [kit], execute(command, args, options) {
    calls.push({ command, args, ...options });
    return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
  } });
  assert.equal(result.status, 0, result.output);
  const suite = calls.find(call => call.args[0] === 'test');
  assert.ok(suite.args.includes('-timeout=4m'), 'Go must emit stuck-test stacks before the process guard');
  assert.equal(suite.timeoutMs, 300_000, 'the whole module includes compilation and subprocess acceptance tests');
  assert.ok(calls.filter(call => call.args[0] !== 'test').every(call => call.timeoutMs === 120_000));
});
