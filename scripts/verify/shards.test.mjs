// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';
import { sandbox } from '../test-fixture.mjs';
import { checkComponents } from './check-components.mjs';
import { evaluateGate } from './gate.mjs';

function fixture(t) {
  const sb = sandbox(t);
  for (const name of ['go.mod', 'go.sum', 'go.work', 'kit/go.mod']) {
    sb.write(sb.dir, name, readFileSync(new URL(`../../${name}`, import.meta.url)));
  }
  sb.write(sb.dir, 'doc.go', 'package lina\n');
  sb.write(sb.dir, 'kit/doc.go', 'package kit\n');
  return sb;
}

for (const phase of ['build', 'unit']) {
  test(`${phase} shard executes only its assigned Go work`, t => {
    const sb = fixture(t);
    const calls = [];
    const result = checkComponents({
      cwd: sb.dir, phase,
      components: [{ name: 'kit', module: 'kit', packages: ['./...'] }],
      execute(command, args, options) {
        calls.push({ command, args, cwd: options.cwd });
        return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
      },
    });
    assert.equal(result.status, 0, result.output);
    const work = calls.filter(call => !['env', 'mod'].includes(call.args[0]));
    assert.deepEqual(work.map(call => call.args[0]), phase === 'build' ? ['build', 'vet', 'tool'] : ['test']);
    assert.ok(work.every(call => call.cwd === join(sb.dir, 'kit')));
    if (phase === 'unit') assert.ok(work[0].args.includes('-count=1'));
  });
}

test('an e2e shard executes its exact top-level tests once without enumeration or builds', t => {
  const sb = fixture(t);
  const calls = [];
  const pattern = '^(TestOne|ExampleTwo|FuzzThree)$';
  const result = checkComponents({
    cwd: sb.dir, phase: 'e2e', testPattern: pattern,
    components: [{ name: 'lina', module: '.', packages: ['example.invalid/e2e'] }],
    execute(command, args) {
      calls.push(args);
      return { status: 0, stdout: args[0] === 'env' ? 'go1.27.3\n' : '', stderr: '' };
    },
  });
  assert.equal(result.status, 0, result.output);
  const work = calls.filter(args => !['env', 'mod'].includes(args[0]));
  assert.equal(work.length, 1);
  assert.equal(work[0][0], 'test');
  assert.ok(work[0].includes('-count=1'));
  assert.ok(work[0].includes('-parallel=4'));
  assert.equal(work[0][work[0].indexOf('-run') + 1], pattern);
  assert.equal(work[0].at(-1), 'example.invalid/e2e');
});

const scope = {
  mode: 'full', head: '1'.repeat(40), docs: true, automation: false, dependencies: false,
  components: true, selectedComponents: ['kit'], unmapped: [],
  componentShards: ['build', 'unit-1', 'e2e-m1-1'],
};
const results = shards => ({
  selection: { result: 'success' }, docs: { result: 'success' },
  automation: { result: 'skipped' }, dependencies: { result: 'skipped' },
  components: { result: 'success', shards },
});

test('foundation requires every selected component shard, even with a successful aggregate', () => {
  const green = Object.fromEntries(scope.componentShards.map(id => [id, { result: 'success' }]));
  assert.equal(evaluateGate(scope, results(green)).result, 'success');
  for (const id of scope.componentShards) {
    for (const result of [undefined, 'failure', 'cancelled', 'skipped', 'pending']) {
      const shards = structuredClone(green);
      if (result === undefined) delete shards[id];
      else shards[id] = { result };
      assert.equal(evaluateGate(scope, results(shards)).result, 'failure', `${id}: ${result ?? 'missing'}`);
    }
  }
  assert.equal(evaluateGate(scope, results({ ...green, unexpected: { result: 'success' } })).result, 'failure');
  assert.equal(evaluateGate(scope, results(undefined)).result, 'failure');
});
