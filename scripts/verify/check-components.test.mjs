// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { sandbox } from '../test-fixture.mjs';
import { checkComponents, execute, componentsTimeoutMs } from './check-components.mjs';

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

// Feed both go-test and compiled-test invocations to the same simulated clock.
function testingArgs(args) {
  if (args.includes('-c')) return ['compile', ...args.slice(1)];
  if (args.some(arg => arg.startsWith('-test.'))) return ['test', ...args.map(arg => arg.replace(/^-test\./, '-'))];
  return args;
}
const acceptanceListing = sb => `example.invalid/e2e\t${join(sb.dir, 'e2e')}\ttests\n`;

test('cold compilation leaves enough shared budget for the module test suite', t => {
  const sb = fixture(t);
  let elapsed = 0;
  t.mock.method(performance, 'now', () => elapsed);
  const result = checkComponents({ cwd: sb.dir, components: [kit], execute(command, goArgs, options) {
    // CI spent about 200 s preparing the suite before its 395 s remainder
    // expired. Include that preparation in the same deterministic deadline.
    if (goArgs[0] === 'build') elapsed += 200_000;
    if (goArgs[0] === 'test') {
      // Model progress past the old deadline without racing wall-clock timers.
      const deadline = elapsed + (options.timeoutMs ?? 120_000);
      let stdout = '';
      for (let i = 0; i < 3; i++) {
        elapsed += 150_000;
        if (elapsed >= deadline) return { status: 1, stdout, stderr: 'test command timed out\n' };
        stdout += 'package ok\n';
      }
      return { status: 0, stdout: `${stdout}finished\n`, stderr: '' };
    }
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

test('a progressing acceptance package can finish beyond four minutes', t => {
  const sb = fixture(t);
  let elapsed = 0;
  t.mock.method(performance, 'now', () => elapsed);
  const result = checkComponents({ cwd: sb.dir, components: [kit], execute(command, args, options) {
    if (args[0] === 'build') elapsed += 250_000;
    if (args[0] === 'test') {
      const limit = args.find(arg => arg.startsWith('-timeout='));
      const minutes = Number(limit?.match(/^\-timeout=(\d+)m$/)?.[1]);
      const duration = 300_000;
      if (!Number.isFinite(minutes) || minutes * 60_000 <= duration || options.timeoutMs <= duration) {
        return { status: 1, stdout: 'acceptance scenarios progressing\n', stderr: 'acceptance package deadline expired\n' };
      }
      elapsed += duration;
      return { status: 0, stdout: 'acceptance package finished\n', stderr: '' };
    }
    return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
  } });
  assert.equal(result.status, 0, result.output);
  assert.match(result.output, /acceptance package finished/);
  assert.ok(elapsed < componentsTimeoutMs, 'the suite stays inside the shared components guard');
});

test('the growing e2e suite can finish beyond six minutes within the component deadline', t => {
  const sb = fixture(t);
  let elapsed = 0;
  const names = ['TestFirst', 'TestSecond'];
  const ran = [];
  t.mock.method(performance, 'now', () => elapsed);
  const result = checkComponents({ cwd: sb.dir, components: [{ name: 'lina', module: '.', packages: ['./e2e/...'] }], execute(command, args, options) {
    args = testingArgs(args);
    if (args[0] === 'build') elapsed += 200_000;
    if (args[0] === 'list') return { status: 0, stdout: acceptanceListing(sb), stderr: '' };
    if (args[0] === 'test' && args.includes('-list')) return { status: 0, stdout: names.join('\n')+'\n', stderr: '' };
    if (args[0] === 'test') {
      const minutes = Number(args.find(arg => arg.startsWith('-timeout='))?.match(/^\-timeout=(\d+)m$/)?.[1]);
      const index = args.indexOf('-run');
      const selected = index < 0 ? names : names.filter(name => new RegExp(args[index+1]).test(name));
      const duration = 195_000 * selected.length;
      if (!Number.isFinite(minutes) || minutes * 60_000 <= duration || options.timeoutMs <= duration) {
        return { status: 1, stdout: 'e2e scenarios progressing\n', stderr: 'e2e package deadline expired\n' };
      }
      ran.push(...selected);
      elapsed += duration;
      return { status: 0, stdout: 'complete e2e batch finished\n', stderr: '' };
    }
    return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
  } });
  assert.equal(result.status, 0, result.output);
  assert.deepEqual(ran.sort(), names, 'every scenario executes exactly once');
  assert.equal(elapsed, 590_000, 'setup plus the full 390-second corpus must execute');
  assert.ok(elapsed < componentsTimeoutMs, 'e2e remains within the shared component guard');
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
  assert.match(result.output, /\$ go test -count=1 -timeout=6m -mod=readonly \.\/\.\.\./);
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
    ['test', '-count=1', '-timeout=6m', '-mod=readonly', './...'],
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
  assert.ok(suite.args.includes('-timeout=6m'), 'Go must emit stuck-test stacks before the process guard');
  assert.ok(suite.timeoutMs > 360_000, 'the shared budget leaves time for Go timeout diagnostics');
  assert.ok(suite.timeoutMs <= 895_000, 'the test command retains the shared components deadline');
  assert.ok(calls.every(call => call.timeoutMs > 0 && call.timeoutMs <= componentsTimeoutMs));
});

test('acceptance batches run every enumerated test once within the shared budget', t => {
  const sb = fixture(t);
  sb.write(sb.dir, 'e2e/fixture_test.go', '// SPDX-License-Identifier: Apache-2.0\npackage e2e\n');
  const names = ['TestOne', 'TestTwo', 'TestThree', 'ExampleFlow', 'FuzzInput'];
  const runs = [];
  let elapsed = 0;
  t.mock.method(performance, 'now', () => elapsed);
  const result = checkComponents({ cwd: sb.dir, components: [{ name: 'lina', module: '.', packages: ['.', './e2e/...'] }],
    execute(command, args, options) {
      args = testingArgs(args);
      if (args[0] === 'list') return { status: 0, stdout: acceptanceListing(sb), stderr: '' };
      if (args[0] === 'test' && args.includes('-list')) return { status: 0, stdout: `${names.join('\n')}\nok example.invalid/e2e 0.01s\n`, stderr: '' };
      if (args[0] === 'test') {
        assert.ok(args.includes('-count=1'));
        assert.ok(args.includes('-timeout=6m'));
        const index = args.indexOf('-run');
        const selected = index < 0 ? (args.includes('./e2e/...') ? names : []) : names.filter(name => new RegExp(args[index + 1]).test(name));
        runs.push({ selected, budget: options.timeoutMs });
        // The growing package exceeds six minutes; either half finishes well
        // inside that bound and both still share the outer components deadline.
        const duration = selected.length * 80_000;
        elapsed += duration;
        return { status: duration >= 360_000 || duration >= options.timeoutMs ? 1 : 0, stdout: '', stderr: '' };
      }
      return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
    } });
  assert.equal(result.status, 0, result.output);
  assert.deepEqual(runs.flatMap(run => run.selected).sort(), [...names].sort());
  const batches = runs.filter(run => run.selected.length > 0);
  assert.equal(batches.length, 2);
  assert.ok(batches[1].budget < batches[0].budget);
  assert.ok(elapsed < componentsTimeoutMs);
});

test('an acceptance batch failure stops the gate without running later batches', t => {
  const sb = fixture(t);
  sb.write(sb.dir, 'e2e/fixture_test.go', '// SPDX-License-Identifier: Apache-2.0\npackage e2e\n');
  let batches = 0;
  const result = checkComponents({ cwd: sb.dir, components: [{ name: 'lina', module: '.', packages: ['./e2e/...'] }],
    execute(command, args) {
      args = testingArgs(args);
      if (args[0] === 'list') return { status: 0, stdout: acceptanceListing(sb), stderr: '' };
      if (args[0] === 'test' && args.includes('-list')) return { status: 0, stdout: 'TestOne\nTestTwo\n', stderr: '' };
      if (args[0] === 'test' && args.includes('-run')) {
        batches++;
        return { status: 1, stdout: '', stderr: 'fixture assertion failed\n' };
      }
      return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
    } });
  assert.equal(result.status, 1, result.output);
  assert.equal(batches, 1);
  assert.match(result.output, /fixture assertion failed/);
});

test('real acceptance batches preserve examples, fuzz seeds and ordinary tests', t => {
  const sb = fixture(t);
  sb.write(sb.dir, 'e2e/support/doc.go', '// SPDX-License-Identifier: Apache-2.0\npackage support\n');
  sb.write(sb.dir, 'ordinary_test.go', `// SPDX-License-Identifier: Apache-2.0
package lina
import ("os"; "testing")
func TestOrdinary(t *testing.T) { if err:=os.WriteFile("ordinary-ran",[]byte("yes"),0600);err!=nil{t.Fatal(err)} }
`);
  sb.write(sb.dir, 'e2e/batches_test.go', `// SPDX-License-Identifier: Apache-2.0
package e2e
import ("fmt"; "os"; "testing")
func mark(name string) { f,err:=os.OpenFile("ran",os.O_APPEND|os.O_CREATE|os.O_WRONLY,0600);if err!=nil{panic(err)};defer f.Close();if _,err=f.WriteString(name+"\\n");err!=nil{panic(err)} }
func TestOne(t *testing.T) { mark("one");t.Run("child",func(t *testing.T){mark("child")}) }
func TestTwo(t *testing.T) { mark("two") }
func Example() { mark("example");fmt.Println("ok")
// Output: ok
}
func FuzzInput(f *testing.F) { f.Add("seed");f.Fuzz(func(t *testing.T,s string){mark("fuzz:"+s)}) }
`);
  const acceptanceBuilds = [];
  const result = checkComponents({ cwd: sb.dir, components: [{ name: 'lina', module: '.', packages: ['.', './e2e/...'] }],
    execute(command, args, options) {
      if (command === 'go' && args[0] === 'test' && args.at(-1).endsWith('/e2e')) acceptanceBuilds.push(args);
      return execute(command, args, options);
    } });
  assert.equal(result.status, 0, result.output);
  assert.equal(readFileSync(join(sb.dir, 'ordinary-ran'), 'utf8'), 'yes');
  assert.deepEqual(readFileSync(join(sb.dir, 'e2e/ran'), 'utf8').trim().split('\n').sort(), ['child', 'example', 'fuzz:seed', 'one', 'two']);
  assert.equal(acceptanceBuilds.length, 1, 'compile acceptance once for enumeration and both batches');
  assert.ok(acceptanceBuilds[0].includes('-c'));
});

test('failed acceptance compilation stops before enumeration or execution', t => {
  const sb = fixture(t);
  const result = checkComponents({ cwd: sb.dir, components: [{ name: 'lina', module: '.', packages: ['./e2e/...'] }],
    execute(command, args) {
      assert.equal(command, 'go', 'a failed build cannot run a test binary');
      if (args[0] === 'list') return { status: 0, stdout: acceptanceListing(sb), stderr: '' };
      if (args.includes('-c')) return { status: 1, stdout: '', stderr: 'acceptance compilation failed\n' };
      return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
    } });
  assert.equal(result.status, 1);
  assert.match(result.output, /acceptance compilation failed/);
});

test('failed acceptance enumeration never becomes an empty successful suite', t => {
  const sb = fixture(t);
  for (const failing of ['list', 'tests']) {
    const result = checkComponents({ cwd: sb.dir, components: [{ name: 'lina', module: '.', packages: ['./e2e/...'] }],
      execute(command, args) {
        args = testingArgs(args);
        if (args[0] === 'list') return { status: failing === 'list' ? 1 : 0, stdout: acceptanceListing(sb), stderr: failing === 'list' ? 'listing failed\n' : '' };
        if (args.includes('-list')) return { status: 1, stdout: 'TestOne\n', stderr: 'enumeration failed\n' };
        assert.ok(!args.includes('-run'));
        return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
      } });
    assert.equal(result.status, 1);
    assert.match(result.output, /listing failed|enumeration failed/);
  }
});
