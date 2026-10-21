// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { git as runGit } from '../git.mjs';
import { componentRegistry, kind, scopeFor } from './scope.mjs';
import { evaluateGate } from './gate.mjs';

test('accounts are registered with the lina Go checks', t => {
  const r = repository(t);
  const base = r.git('rev-parse', 'HEAD');
  mkdirSync(join(r.cwd, 'internal/core/accounts'), { recursive: true });
  writeFileSync(join(r.cwd, 'internal/core/accounts/signin.go'), 'package accounts\n');
  const scope = r.classify(r.commit(), { base, full: true });
  assert.deepEqual(scope.unmapped, []);
  assert.ok(scope.selectedComponents.includes('lina'));
  assert.ok(componentRegistry.find(component => component.name === 'lina').packages.includes('./internal/core/accounts/...'));
});

test('waiting and running lists are covered by the lina Go checks', t => {
  const r = repository(t);
  const base = r.git('rev-parse', 'HEAD');
  for (const directory of ['waiting', 'runs']) {
    mkdirSync(join(r.cwd, `internal/core/${directory}`), { recursive: true });
    writeFileSync(join(r.cwd, `internal/core/${directory}/list.go`), `package ${directory}\n`);
  }
  const scope = r.classify(r.commit(), { base, full: true });
  assert.deepEqual(scope.unmapped, []);
  assert.ok(scope.selectedComponents.includes('lina'));
  for (const directory of ['waiting', 'runs']) {
    assert.ok(componentRegistry.find(component => component.name === 'lina').packages.includes(`./internal/core/${directory}/...`));
  }
});

test('independent Core package registrations merge without conflicts', t => {
  const r = repository(t);
  const source = readFileSync(new URL('./scope.mjs', import.meta.url), 'utf8');
  const addPackage = (text, name, before) => {
    const entry = new RegExp(`(\\s*)'internal/core/${before}/',`);
    assert.match(text, entry);
    return text.replace(entry, `$1'internal/core/${name}/',$&`);
  };
  const ours = addPackage(source, 'attachments', 'authority');
  const theirs = addPackage(source, 'scheduler', 'secrets');
  for (const [name, text] of Object.entries({ base: source, ours, theirs })) {
    writeFileSync(join(r.cwd, `${name}.mjs`), text);
  }
  const merged = runGit(r.cwd, ['merge-file', '-p', 'ours.mjs', 'base.mjs', 'theirs.mjs']);
  assert.equal(merged.status, 0, merged.stdout || merged.stderr);
  assert.equal(merged.stdout, addPackage(ours, 'scheduler', 'secrets'));
});

