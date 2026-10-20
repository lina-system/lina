// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { existsSync, readFileSync, symlinkSync } from 'node:fs';
import { join, relative } from 'node:path';
import test from 'node:test';
import { sandbox } from '../test-fixture.mjs';
import { checkDependencies } from './check-dependencies.mjs';

// Seed both source files and the Go parser responses. Every subprocess goes through this stub;
// even cache discovery and toolchain inspection must never invoke Go or use the network.
function fixture(t) {
  const sb = sandbox(t);
  const manifests = new Map();
  const calls = [];
  const seed = (name, fields = {}) => {
    const data = { Go: '1.27', Toolchain: 'go1.27.3', Require: [], Replace: [], ...fields };
    manifests.set(name, data);
    const lines = name === 'go.work' ? [] : [`module example.invalid/${name === 'go.mod' ? 'lina' : 'kit'}`];
    lines.push(`go ${data.Go}`, `toolchain ${data.Toolchain}`);
    for (const entry of data.Require) lines.push(`require ${entry.Path} ${entry.Version}`);
    for (const { Old, New } of data.Replace) lines.push(`replace ${Old.Path} ${Old.Version ?? ''} => ${New.Path} ${New.Version ?? ''}`);
    if (name === 'go.work') lines.push('use (', '.', './kit', ')');
    sb.write(sb.dir, name, `${lines.join('\n')}\n`);
  };
  seed('go.mod', { Require: [{ Path: 'example.invalid/dep', Version: 'v1.2.3' }] });
  seed('kit/go.mod');
  seed('go.work');
  sb.write(sb.dir, 'go.sum', 'verified fixture sums\n');
  const execute = (command, args, options) => {
    assert.equal(command, 'go');
    calls.push({ args, ...options });
    let stdout = '';
    if (args.join(' ') === 'env GOCACHE GOMODCACHE') stdout = `${join(sb.dir, 'cache')}\n${join(sb.dir, 'modules')}\n`;
    else if (args.join(' ') === 'env GOVERSION') stdout = 'go1.27.3\n';
    else if (args.join(' ') === 'mod edit -json' || args.join(' ') === 'work edit -json') {
      const name = args[0] === 'work' ? 'go.work' : relative(sb.dir, join(options.cwd, 'go.mod'));
      stdout = JSON.stringify(manifests.get(name));
    } else if (args.join(' ') === 'mod verify') {
      const sums = join(options.cwd, 'go.sum');
      if (existsSync(sums) && readFileSync(sums, 'utf8').includes('CORRUPT')) {
        return { status: 1, stdout: '', stderr: 'checksum mismatch in seeded module\n' };
      }
      stdout = 'all modules verified\n';
    } else assert.fail(`Unexpected Go command: ${args.join(' ')}`);
    return { status: 0, stdout, stderr: '' };
  };
  return { ...sb, seed, calls, execute, check: (runner = execute) => checkDependencies({ cwd: sb.dir, execute: runner }) };
}

test('exact releases, prereleases, pseudo-versions and local module replacements pass', t => {
  const f = fixture(t);
  f.seed('go.mod', {
    Require: ['v1.2.3', 'v2.0.0+incompatible', 'v1.2.3-rc.1', 'v0.0.0-20260101000000-abcdef123456']
      .map((Version, i) => ({ Path: `example.invalid/dep${i}`, Version })),
    Replace: [{ Old: { Path: 'example.invalid/kit' }, New: { Path: './kit' } },
      { Old: { Path: 'example.invalid/dep0', Version: 'v1.2.3' }, New: { Path: 'example.invalid/fork', Version: 'v1.2.4' } }],
  });
  f.seed('kit/go.mod', { Replace: [{ Old: { Path: 'example.invalid/lina' }, New: { Path: '..' } }] });
  const result = f.check();
  assert.equal(result.status, 0, result.output);
  assert.equal(result.toolchain, 'go1.27.3');
  assert.deepEqual(f.calls.filter(call => call.args.join(' ') === 'mod verify').map(call => call.cwd), [f.dir, join(f.dir, 'kit')]);
  for (const call of f.calls.slice(1)) {
    assert.equal(call.env.GOTOOLCHAIN, 'go1.27.3');
    assert.equal(call.env.GOFLAGS, '-mod=readonly');
    assert.equal(call.env.GOENV, 'off');
    assert.equal(call.env.GIT_CONFIG_GLOBAL, '/dev/null');
    for (const name of ['GH_TOKEN', 'GITHUB_TOKEN', 'GOPROXY', 'GOSUMDB', 'GOPRIVATE', 'GONOSUMDB', 'GOINSECURE']) {
      assert.equal(call.env[name], undefined, name);
    }
  }
  assert.equal(existsSync(f.calls.at(-1).env.HOME), false, 'temporary home removed');
});

for (const file of ['go.mod', 'kit/go.mod', 'go.work']) {
  for (const Toolchain of ['go1.26.1', 'go1.27', 'default', '']) {
    test(`${file}: missing, differing or inexact toolchain ${JSON.stringify(Toolchain)} fails before Go runs`, t => {
      const f = fixture(t);
      f.seed(file, { Toolchain });
      const result = f.check();
      assert.equal(result.status, 1);
      assert.match(result.output, /same Go and toolchain/);
      assert.equal(f.calls.length, 0);
    });
  }
}

for (const Version of ['latest', 'main', 'v1', 'v1.2', '^v1.2.3', '>=v1.2.3', 'abcdef123456', 'v01.2.3', 'v1.2.3-01', '']) {
  for (const file of ['go.mod', 'kit/go.mod']) {
    test(`${file}: require ${JSON.stringify(Version)} fails exact pins`, t => {
      const f = fixture(t);
      f.seed(file, { Require: [{ Path: 'example.invalid/dep', Version }] });
      const result = f.check();
      assert.equal(result.status, 1);
      assert.match(result.output, /require.*exact version/);
    });
  }
}

for (const file of ['go.mod', 'kit/go.mod', 'go.work']) {
  for (const New of [{ Path: 'example.invalid/fork', Version: 'main' }, { Path: 'example.invalid/fork' },
    { Path: '../outside' }, { Path: '/tmp' }, { Path: './missing' }, { Path: './data' }]) {
    test(`${file}: replace ${JSON.stringify(New)} is rejected`, t => {
      const f = fixture(t);
      f.write(f.dir, file === 'kit/go.mod' ? 'kit/data/file' : 'data/file', 'not a module');
      f.seed(file, { Replace: [{ Old: { Path: 'example.invalid/dep' }, New }] });
      const result = f.check();
      assert.equal(result.status, 1);
      assert.match(result.output, /replace/);
    });
  }
}

test('versioned replacement selectors must also be exact', t => {
  const f = fixture(t);
  f.seed('go.mod', { Replace: [{ Old: { Path: 'example.invalid/dep', Version: 'main' }, New: { Path: './kit' } }] });
  assert.match(f.check().output, /replace.*exact version/);
});

test('workspace overrides enforce the same local replacement boundary, including symlinks', t => {
  const f = fixture(t);
  const outside = sandbox(t);
  outside.write(outside.dir, 'go.mod', 'module example.invalid/outside\n');
  symlinkSync(outside.dir, join(f.dir, 'escaped'));
  f.seed('go.work', { Replace: [{ Old: { Path: 'example.invalid/dep' }, New: { Path: './escaped' } }] });
  assert.equal(f.check().status, 1);
  f.write(f.dir, 'third_party/dep/upstream/go.mod', 'module example.invalid/dep\n');
  f.seed('go.work', { Replace: [{ Old: { Path: 'example.invalid/dep' }, New: { Path: './third_party/dep/upstream' } }] });
  assert.equal(f.check().status, 0);
});

for (const file of ['go.sum', 'kit/go.sum']) {
  test(`${file}: failed go mod verify fails the check with its diagnostics`, t => {
    const f = fixture(t);
    f.write(f.dir, file, 'CORRUPT\n');
    const result = f.check();
    assert.equal(result.status, 1);
    assert.match(result.output, /checksum mismatch in seeded module/);
    assert.equal(existsSync(f.calls.at(-1).env.HOME), false);
  });
}

test('toolchain, parser and runner failures fail closed and clean temporary resources', t => {
  for (const stage of ['env GOCACHE GOMODCACHE', 'env GOVERSION', 'mod edit -json', 'work edit -json']) {
    const f = fixture(t);
    const result = f.check((command, args, options) => args.join(' ') === stage
      ? { status: 1, stdout: '', stderr: 'seeded command failure\n' } : f.execute(command, args, options));
    assert.equal(result.status, 1, stage);
    assert.match(result.output, /seeded command failure/);
  }
  for (const stdout of ['{}', 'not JSON']) {
    const f = fixture(t);
    assert.equal(f.check((command, args, options) => args.join(' ') === 'mod edit -json'
      ? { status: 0, stdout, stderr: '' } : f.execute(command, args, options)).status, 1);
  }
  const f = fixture(t);
  assert.equal(f.check((command, args, options) => args.join(' ') === 'env GOVERSION'
    ? { status: 0, stdout: 'go1.26.1', stderr: '' } : f.execute(command, args, options)).status, 1);
});
