// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { join } from 'node:path';
import { existsSync } from 'node:fs';
import { sandbox } from '../test-fixture.mjs';
import { checkNpm } from './check-npm.mjs';

function fixture(t) {
  const f = sandbox(t), calls = [];
  const manifest = { name: 'fixture', version: '1.0.0', dependencies: { example: '1.2.3' }, scripts: { test: 'node --test' } };
  const lock = { lockfileVersion: 3, packages: { '': manifest, 'node_modules/example': { version: '1.2.3', resolved: 'https://registry.npmjs.org/example/-/example-1.2.3.tgz', integrity: 'sha512-' + Buffer.alloc(64).toString('base64') } } };
  f.write(f.dir, '.npmrc', 'registry=https://registry.npmjs.org/\nsave-exact=true\nignore-scripts=true\n');
  f.write(f.dir, '.node-version', process.versions.node + '\n');
  const save = () => {
    f.write(f.dir, 'adapters/claude/package.json', JSON.stringify(manifest));
    f.write(f.dir, 'adapters/claude/package-lock.json', JSON.stringify(lock));
  };
  const run = (command, args, options) => { calls.push({ command, args, ...options }); return { status: 0, stdout: '', stderr: '' }; };
  const check = (execute = run, exceptions = []) => { save(); return checkNpm({ cwd: f.dir, execute, exceptions }); };
  return { ...f, calls, manifest, lock, check, run };
}

test('install, signature audit and adapter tests run in an isolated copy without credentials', t => {
  const f = fixture(t), result = f.check();
  assert.equal(result.status, 0, result.output);
  assert.deepEqual(f.calls.map(c => c.args), [['ci', '--ignore-scripts'], ['audit', 'signatures'], ['test']]);
  for (const c of f.calls) {
    assert.equal(c.command, 'npm');
    assert.notEqual(c.cwd, join(f.dir, 'adapters/claude'));
    assert.equal(c.env.NPM_TOKEN, undefined);
    assert.equal(c.env.NODE_OPTIONS, undefined);
    assert.equal(existsSync(c.cwd), false);
  }
});
for (const change of [
  f => { f.manifest.dependencies.example = '^1.2.3'; },
  f => { f.manifest.scripts.pretest = 'touch bad'; },
  f => { f.lock.packages['node_modules/example'].resolved = 'https://evil.invalid/a.tgz'; },
  f => { delete f.lock.packages['node_modules/example'].integrity; },
  f => { f.lock.packages['node_modules/example'].hasInstallScript = true; },
  f => { f.lock.packages['node_modules/example'].link = true; },
  f => { f.manifest.overrides = { example: '^1.2.3' }; },
]) test('unsafe manifest or lock fails before install', t => {
  const f = fixture(t); change(f);
  assert.equal(f.check().status, 1);
  assert.equal(f.calls.length, 0);
});
test('install-script exception is exact', t => {
  const f = fixture(t); f.lock.packages['node_modules/example'].hasInstallScript = true;
  assert.equal(f.check(f.run, [{ kind: 'install-script', name: 'example', version: '1.2.3', reason: 'prebuilt' }]).status, 0);
  assert.equal(f.check(f.run, [{ kind: 'install-script', name: 'example', version: '1.2.4', reason: 'prebuilt' }]).status, 1);
});
for (const stage of ['ci', 'audit', 'test']) test(`${stage} failure fails the gate and cleans resources`, t => {
  const f = fixture(t);
  assert.equal(f.check((command, args, options) => { const r = f.run(command, args, options); return args[0] === stage ? { ...r, status: 1, stderr: 'fixture failure' } : r; }).status, 1);
  assert.equal(existsSync(f.calls[0].cwd), false);
});

test('Claude ACP has its own verified install without optional packages or npm test', t => {
  const f = fixture(t);
  const manifest = {...f.manifest, scripts: undefined};
  f.write(f.dir, 'adapters/claude-acp/package.json', JSON.stringify(manifest));
  f.write(f.dir, 'adapters/claude-acp/package-lock.json', JSON.stringify({...f.lock, packages: {...f.lock.packages, '': manifest}}));
  const result = f.check();
  assert.equal(result.status, 0, result.output);
  const calls = f.calls.slice(3);
  assert.deepEqual(calls.map(c => c.args), [['ci', '--ignore-scripts', '--omit=dev', '--omit=optional'], ['audit', 'signatures']]);
});
