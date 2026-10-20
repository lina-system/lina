// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import test from 'node:test';
import { evaluateGate } from './gate.mjs';

const scope = { mode: 'full', head: '1'.repeat(40), docs: true, automation: true, components: false, selectedComponents: [], unmapped: [] };
const passed = { selection: { result: 'success' }, docs: { result: 'success' }, automation: { result: 'success' }, components: { result: 'skipped' } };

test('selected checks all succeed and intentional docs-only skipping is accepted', () => {
  const full = evaluateGate(scope, passed);
  assert.equal(full.result, 'success');
  assert.equal(full.mode, 'full');
  assert.equal(full.head, scope.head);
  assert.deepEqual(full.failures, []);
  assert.equal(evaluateGate({ ...scope, mode: 'docs', automation: false }, {
    ...passed, automation: { result: 'skipped' },
  }).result, 'success');
});

test('every selected failed, cancelled, skipped, missing or pending check blocks foundation', () => {
  for (const name of ['selection', 'docs', 'automation']) {
    for (const status of ['failure', 'cancelled', 'skipped', 'pending', undefined]) {
      const results = { ...passed, [name]: status ? { result: status } : undefined };
      assert.equal(evaluateGate(scope, results).result, 'failure', `${name}: ${status}`);
    }
  }
});

test('uncovered files, invalid selection and unexpected unselected checks cannot pass', () => {
  assert.equal(evaluateGate({ ...scope, unmapped: ['new-product.mjs'] }, passed).result, 'failure');
  assert.equal(evaluateGate({ ...scope, docs: false }, passed).result, 'failure');
  assert.equal(evaluateGate({ ...scope, automation: 'false' }, passed).result, 'failure');
  assert.equal(evaluateGate({ ...scope, automation: false }, passed).result, 'failure');
  assert.equal(evaluateGate({}, passed).result, 'failure');
});

test('a failed selection fails closed with one failure and no scope', () => {
  const result = evaluateGate({}, { selection: { result: 'failure' } });
  assert.equal(result.result, 'failure');
  assert.deepEqual(result.failures, ['selection did not succeed']);
  assert.equal(evaluateGate(scope, { selection: passed.selection }).result, 'failure');
});

test('components must succeed when selected and be skipped otherwise', () => {
  for (const result of ['failure', 'cancelled', 'skipped', undefined]) {
    assert.equal(evaluateGate({ ...scope, components: true, selectedComponents: ['kit'] }, {
      ...passed, components: result ? { result } : undefined,
    }).result, 'failure');
  }
  assert.equal(evaluateGate({ ...scope, components: true, selectedComponents: ['kit'] }, {
    ...passed, components: { result: 'success' },
  }).result, 'success');
  assert.equal(evaluateGate({ ...scope, components: false, selectedComponents: [] }, {
    ...passed, components: { result: 'skipped' },
  }).result, 'success');
  assert.equal(evaluateGate({ ...scope, components: false, selectedComponents: [] }, {
    ...passed, components: { result: 'success' },
  }).result, 'failure');
  assert.equal(evaluateGate({ ...scope, components: true, selectedComponents: [] }, passed).result, 'failure');
});
