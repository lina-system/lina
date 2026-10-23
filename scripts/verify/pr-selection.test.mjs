// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';
import { selectFoundation } from '../foundation.mjs';
import { developmentRepository, sandbox } from '../test-fixture.mjs';
import { run } from '../git.mjs';

function fixture(t) {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const toolchain = 'go 1.27\ntoolchain go1.27.3\n';
  const base = sb.commit(work, 'test: add Go fixture', {
    'go.mod': 'module example.invalid/lina\n' + toolchain,
    'kit/go.mod': 'module example.invalid/lina/kit\n' + toolchain,
    'go.work': toolchain + 'use (\n.\n./kit\n)\n',
    'doc.go': 'package lina\n', 'kit/doc.go': 'package kit\n',
    'internal/core/records/value.go': 'package records\nconst Value = 1\n',
    'cmd/lina/main.go': 'package main\nfunc main() {}\n',
    'e2e/m1/one_test.go': 'package acceptance_test\nimport ("testing"; "example.invalid/lina/internal/core/records")\nfunc TestOne(t *testing.T) { _ = records.Value }\n',
    'e2e/m1/two_test.go': 'package acceptance_test\nimport "testing"\nfunc TestTwo(t *testing.T) {}\n',
  });
  sb.commit(work, 'test: change Go code', { 'internal/core/records/value.go': 'package records\nconst Value = 2\n' });
  return { sb, work, base };
}

test('a Go-only pull request keeps build and units without any acceptance tasks', t => {
  const { work, base } = fixture(t);
  const { plan } = selectFoundation({ cwd: work, range: base + '..HEAD', event: 'pull_request' });
  assert.equal(plan.selection.result, 'success');
  assert.equal(plan.tasks.filter(task => task.kind === 'e2e').length, 0);
  assert.deepEqual(plan.scenarios, []);
  assert.ok(plan.tasks.some(task => task.kind === 'build'));
  assert.ok(plan.tasks.some(task => task.kind === 'unit'));
});

test('the e2e opt-in selects reached scenarios, while local and full post-merge runs retain coverage', t => {
  const { work, base } = fixture(t);
  for (const options of [{ event: 'pull_request', e2e: true }, {}]) {
    const { plan } = selectFoundation({ cwd: work, range: base + '..HEAD', ...options });
    assert.equal(plan.selection.result, 'success');
    assert.deepEqual(plan.tasks.filter(task => task.kind === 'e2e').flatMap(task => task.tests), ['TestOne']);
  }
  for (const event of ['push', 'schedule', 'workflow_dispatch']) {
    const { plan } = selectFoundation({ cwd: work, full: true, event });
    assert.equal(plan.selection.result, 'success');
    assert.deepEqual(plan.tasks.filter(task => task.kind === 'e2e').flatMap(task => task.tests).sort(), ['TestOne', 'TestTwo']);
  }
});

test('the CLI carries PR selection into a saved plan', t => {
  const { sb, work, base } = fixture(t);
  const path = join(sb.dir, 'plan.json');
  const result = run(process.execPath, [join(work, 'scripts/foundation.mjs'), '--range', base + '..HEAD',
    '--event', 'pull_request', '--select', path, '--no-note'], { cwd: work, env: sb.env });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(readFileSync(path)).tasks.filter(task => task.kind === 'e2e').length, 0);
});
