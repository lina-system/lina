// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { existsSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
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
  const vulnerabilities = new Map();
  const packages = new Map();
  const licenses = new Map();
  const toolRequirements = [
    { Path: 'golang.org/x/vuln', Version: 'v1.9.0' },
    { Path: 'github.com/google/go-licenses/v2', Version: 'v2.0.1' },
  ];
  const toolPaths = ['golang.org/x/vuln/cmd/govulncheck', 'github.com/google/go-licenses/v2'];
  const seed = (name, fields = {}) => {
    const data = { Go: '1.27', Toolchain: 'go1.27.3', Require: [], Replace: [], ...fields };
    if (name !== 'go.work') data.Module = { Path: `example.invalid/${name === 'go.mod' ? 'lina' : 'kit'}` };
    if (name === 'go.mod') {
      data.Tool ??= toolPaths.map(Path => ({ Path }));
      data.Require.push(...toolRequirements.filter(tool => !data.Require.some(entry => entry.Path === tool.Path)));
    }
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
  sb.write(sb.dir, 'scripts/verify/dependency-exceptions.json', '[]\n');
  sb.write(sb.dir, 'THIRD-PARTY-NOTICES.md', '# Notices\n\n## example.invalid/dep\n\nMIT fixture notice.\n');
  for (const directory of [sb.dir, join(sb.dir, 'kit')]) {
    vulnerabilities.set(directory, [{ config: { protocol_version: 'v1.0.0' } }]);
    packages.set(directory, [
      { ImportPath: directory === sb.dir ? 'example.invalid/lina' : 'example.invalid/kit',
        Module: { Path: directory === sb.dir ? 'example.invalid/lina' : 'example.invalid/kit', Dir: directory, Main: true } },
      { ImportPath: 'example.invalid/dep/subpackage', Module: { Path: 'example.invalid/dep', Version: 'v1.2.3' } },
      { ImportPath: 'fmt', Standard: true },
    ]);
    licenses.set(directory, [{ name: 'example.invalid/dep/subpackage', license: 'MIT' }]);
  }
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
    } else if (args.join(' ') === 'tool govulncheck -json ./...') {
      stdout = vulnerabilities.get(options.cwd).map(message => JSON.stringify(message, null, 2)).join('\n');
    } else if (args.join(' ') === 'list -deps -json ./...') {
      stdout = packages.get(options.cwd).map(pkg => JSON.stringify(pkg, null, 2)).join('\n');
    } else if (args.slice(0, 4).join(' ') === 'tool go-licenses report --template') {
      assert.ok(existsSync(args[4]), 'report template exists for scanner');
      assert.equal(args[5], './...');
      stdout = JSON.stringify(licenses.get(options.cwd));
    } else assert.fail(`Unexpected Go command: ${args.join(' ')}`);
    return { status: 0, stdout, stderr: '' };
  };
  return { ...sb, seed, calls, execute, vulnerabilities, packages, licenses, check: (runner = execute) => checkDependencies({ cwd: sb.dir, execute: runner }) };
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

const finding = (fields = {}) => ({ finding: {
  osv: 'GO-2026-0001', trace: [{ module: 'example.invalid/dep', version: 'v1.2.3' }], ...fields,
} });
const exception = (fields = {}) => ({
  kind: 'vulnerability', name: 'example.invalid/dep', version: 'v1.2.3', id: 'GO-2026-0001',
  reason: 'The vulnerable functionality is not used.', ...fields,
});
const setExceptions = (f, entries) => f.write(f.dir, 'scripts/verify/dependency-exceptions.json', JSON.stringify(entries));

for (const module of ['.', 'kit']) {
  test(`${module}: a JSON finding fails even when govulncheck exits successfully`, t => {
    const f = fixture(t);
    f.vulnerabilities.get(join(f.dir, module)).push(finding());
    const result = f.check();
    assert.equal(result.status, 1);
    assert.match(result.output, /unexcepted vulnerability.*GO-2026-0001.*example.invalid\/dep.*v1.2.3/);
  });
}

test('an exact exception permits repeated findings and OSV metadata alone is not a finding', t => {
  const f = fixture(t);
  f.vulnerabilities.get(f.dir).push({ osv: { id: 'GO-2026-9999' } }, finding(), finding());
  setExceptions(f, [exception({ reference: 'https://example.invalid/advisory' })]);
  const result = f.check();
  assert.equal(result.status, 0, result.output);
  assert.deepEqual(f.calls.filter(call => call.args[1] === 'govulncheck').map(call => call.cwd), [f.dir, join(f.dir, 'kit')]);
});

for (const fields of [{ name: 'example.invalid/other' }, { version: 'v1.2.4' }, { id: 'GO-2026-0002' },
  { kind: 'install-script', version: '1.2.3', id: undefined }]) {
  test(`exceptions cannot waive another finding: ${JSON.stringify(fields)}`, t => {
    const f = fixture(t);
    f.vulnerabilities.get(f.dir).push(finding());
    setExceptions(f, [exception(fields)]);
    assert.match(f.check().output, /unexcepted vulnerability/);
  });
}

test('standard library findings use the exact stdlib version', t => {
  const f = fixture(t);
  f.vulnerabilities.get(f.dir).push(finding({ trace: [{ module: 'stdlib', version: 'v1.27.3' }] }));
  assert.equal(f.check().status, 1);
  setExceptions(f, [exception({ name: 'stdlib', version: 'v1.27.3' })]);
  assert.equal(f.check().status, 0);
});

for (const entries of [{}, [null], [exception({ kind: 'license' })], [exception({ name: 'example.invalid/*' })],
  [exception({ name: 'example.invalid/dep?' })], [exception({ version: 'v1.2.*' })],
  [exception({ id: 'GO-2026-*' })], [exception({ reason: undefined })], [exception({ reason: '  ' })],
  [exception({ id: undefined })], [exception({ name: '' })], [exception({ reference: 'not a URL' })],
  [exception({ reference: 'file:///notice' })], [exception({ typo: 'value' })],
  [exception(), exception()], [exception({ kind: 'install-script', id: undefined, version: '^1.2.3' })]]) {
  test(`malformed exceptions fail before scanning: ${JSON.stringify(entries)}`, t => {
    const f = fixture(t);
    setExceptions(f, entries);
    const result = f.check();
    assert.equal(result.status, 1);
    assert.match(result.output, /dependency-exceptions/);
    assert.equal(f.calls.length, 0);
  });
}

test('missing or invalid JSON exceptions fail closed', t => {
  const f = fixture(t);
  for (const source of ['', '[']) {
    f.write(f.dir, 'scripts/verify/dependency-exceptions.json', source);
    assert.equal(f.check().status, 1);
  }
  rmSync(join(f.dir, 'scripts/verify/dependency-exceptions.json'));
  assert.equal(f.check().status, 1);
});

for (const license of ['MIT', 'Apache-2.0', 'BSD-2-Clause', 'BSD-3-Clause', 'ISC']) {
  test(`${license} is permitted for included Go modules`, t => {
    const f = fixture(t);
    f.licenses.get(f.dir)[0].license = license;
    assert.equal(f.check().status, 0);
  });
}

for (const license of ['GPL-3.0', 'LGPL-2.1', 'MPL-2.0', 'CC-BY-NC-4.0', 'BUSL-1.1', 'Unknown', '', 'MIT OR GPL-3.0']) {
  test(`${license} fails for a linked Go module`, t => {
    const f = fixture(t);
    f.licenses.get(f.dir)[0].license = license;
    const result = f.check();
    assert.equal(result.status, 1);
    assert.match(result.output, /disallowed license/);
  });
}

test('every detected license must be allowed, including nested package licenses', t => {
  const f = fixture(t);
  f.licenses.get(f.dir).push({ name: 'example.invalid/dep/subpackage', license: 'GPL-3.0' });
  assert.equal(f.check().status, 1);
});

for (const notices of ['# Notices\n', '## example.invalid/dep-other\nUnrelated.\n',
  '# Notices\nMention example.invalid/dep in prose.\n', '## example.invalid/dep\n\n## Other\nOther notice.\n']) {
  test(`included module needs its own nonempty notices entry: ${JSON.stringify(notices)}`, t => {
    const f = fixture(t);
    f.write(f.dir, 'THIRD-PARTY-NOTICES.md', notices);
    const result = f.check();
    assert.equal(result.status, 1);
    assert.match(result.output, /missing.*THIRD-PARTY-NOTICES/);
  });
}

test('first-party packages and unlinked tool requirements do not need third-party notices', t => {
  const f = fixture(t);
  for (const directory of [f.dir, join(f.dir, 'kit')]) {
    f.packages.set(directory, f.packages.get(directory).filter(pkg => pkg.Module?.Path !== 'example.invalid/dep'));
    f.licenses.set(directory, [{ name: directory === f.dir ? 'example.invalid/lina' : 'example.invalid/kit', license: 'Unknown' }]);
  }
  f.write(f.dir, 'THIRD-PARTY-NOTICES.md', '# Notices\n');
  assert.equal(f.check().status, 0);
});

test('a replacement remains included, and notices identify the replacement source', t => {
  const f = fixture(t);
  f.packages.get(f.dir)[1].Module.Replace = { Path: 'example.invalid/fork', Version: 'v1.2.4' };
  assert.match(f.check().output, /missing.*THIRD-PARTY-NOTICES.*example.invalid\/fork/);
  f.write(f.dir, 'THIRD-PARTY-NOTICES.md', '## example.invalid/fork\nMIT fork notice.\n## example.invalid/dep\nMIT original notice.\n');
  assert.equal(f.check().status, 0);
});

test('local third-party replacements are scanned and need notices under their module name', t => {
  const f = fixture(t);
  f.packages.get(f.dir)[1].Module.Replace = { Path: './third_party/dep/upstream', Dir: join(f.dir, 'third_party/dep/upstream') };
  f.licenses.get(f.dir)[0].license = 'GPL-3.0';
  assert.equal(f.check().status, 1);
  f.licenses.get(f.dir)[0].license = 'MIT';
  assert.equal(f.check().status, 0);
});

test('a linked module missing from the scanner report fails closed', t => {
  const f = fixture(t);
  f.licenses.set(f.dir, []);
  assert.match(f.check().output, /missing license scan.*example.invalid\/dep/);
});

test('scanner tools must be declared and pinned in the root module', t => {
  const f = fixture(t);
  f.seed('go.mod', { Tool: [] });
  assert.match(f.check().output, /module tool/);
});

for (const stage of ['tool govulncheck', 'tool go-licenses', 'list -deps']) {
  test(`${stage}: command failures, malformed output and cleanup`, t => {
    for (const response of [{ status: 1, stdout: '', stderr: 'seeded scanner failure\n' },
      { status: 0, stdout: 'not JSON', stderr: '' }, { status: 0, stdout: '{}', stderr: '' },
      { status: 0, stdout: '', stderr: '' }]) {
      const f = fixture(t);
      const result = f.check((command, args, options) => args.slice(0, 2).join(' ') === stage
        ? response : f.execute(command, args, options));
      assert.equal(result.status, 1, `${stage}: ${JSON.stringify(response)}`);
      assert.equal(existsSync(f.calls.at(-1).env.HOME), false);
    }
  });
}

test('malformed findings and unsupported vulnerability protocols cannot pass silently', t => {
  for (const messages of [[{ config: { protocol_version: 'v2.0.0' } }],
    [{ config: { protocol_version: 'v1.0.0' } }, finding({ trace: [] })],
    [{ config: { protocol_version: 'v1.0.0' } }, { finding: {} }]]) {
    const f = fixture(t);
    f.vulnerabilities.set(f.dir, messages);
    assert.equal(f.check().status, 1);
  }
});
