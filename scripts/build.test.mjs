// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { sandbox } from './test-fixture.mjs';
import { git } from './git.mjs';
import { createGoContext, execute } from './verify/check-components.mjs';
import { build, buildTimeoutMs } from './build.mjs';

const root = dirname(dirname(fileURLToPath(import.meta.url)));

test('build records the verified CRW pin and refuses a failed corpus gate', t => {
  const f = fixture(t);
  const pin = { version: 1, repository: 'https://github.com/thisisjun786/codex-relay-workflow', commit: 'a'.repeat(40),
    domains: ['record-schema'], schemas: [{ path: 'schema/relationship.json', source: 'contract/schema/relationship.json', sha256: 'b'.repeat(64) }],
    fixtures: [{ path: 'fixtures/records.json.gz', source: 'contract/fixtures/records.json.gz', sha256: 'c'.repeat(64), domain: 'record-schema' }] };
  let failPin = false;
  const run = (command, args, options) => {
    if (args[0] === 'env') return { status: 0, stdout: options.env.GOTOOLCHAIN, stderr: '' };
    if (args[0] === 'build') {
      const binary = args[args.indexOf('-o') + 1];
      f.write(dirname(binary), binary.split(/[\\/]/).at(-1), 'fixture executable');
    }
    if (args.includes('./internal/core/work/pin/crwinfo')) {
      return failPin ? { status: 1, stderr: 'CRW pin: changed fixture hash' }
        : { status: 0, stdout: JSON.stringify(pin), stderr: '' };
    }
    return { status: 0, stdout: args[0] === 'run'
      ? (args.includes('./internal/core/manifest/clientinfo') ? '[]' : '{}') : '', stderr: '' };
  };
  const result = build({ cwd: f.cwd, out: f.out, execute: run });
  assert.deepEqual(result.crwPin, pin);
  assert.deepEqual(JSON.parse(readFileSync(join(f.out, 'manifest.json'), 'utf8')).crwPin, pin);
  failPin = true;
  assert.throws(() => build({ cwd: f.cwd, out: f.out, execute: run }), /changed fixture hash/);
  assert.equal(existsSync(join(f.out, 'manifest.json')), false);
});

test('the pinned SQLite rebuild script has valid shell syntax', () => {
  const result = spawnSync('bash', ['-n', join(root, 'packaging/sqlite-vec/build.sh')], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
});

function fixture(t, { timeoutScale = 1, executeCommand = execute } = {}) {
  const sb = sandbox(t);
  const cwd = join(sb.dir, 'source');
  mkdirSync(cwd);
  // Export the source tree, including new packages and embedded assets, without
  // repository metadata or ignored build/runtime files. Package allowlists drift
  // whenever a command acquires another local dependency.
  const sources = git(root, ['ls-files', '-z', '--cached', '--others', '--exclude-standard']);
  assert.equal(sources.status, 0, sources.stderr);
  for (const name of sources.stdout.split('\0').filter(Boolean)) {

    if (!existsSync(join(root, name))) continue;
    mkdirSync(dirname(join(cwd, name)), { recursive: true });
    cpSync(join(root, name), join(cwd, name), { recursive: true });
  }
  sb.g(cwd, 'init', '-q', '--initial-branch=dev');
  const revision = sb.commit(cwd, 'test: build source');
  const context = createGoContext(cwd);
  t.after(context.close);
  const out = join(sb.dir, 'artifacts with spaces');
  // A full cross-build contains multiple Go commands. Share the same bounded
  // allowance across targets, inside foundation's 600 s automation watchdog.
  const deadline = performance.now() + buildTimeoutMs * timeoutScale;
  const run = (args = ['--out', out]) => {
    const timeoutMs = Math.max(1, Math.floor(deadline - performance.now()));
    return executeCommand(process.execPath,
      [join(cwd, 'scripts/build.mjs'), ...args, '--timeout-ms', String(Math.max(1, timeoutMs - 1000))],
      { cwd, env: context.env, timeoutMs });
  };
  const build = () => {
    const result = run();
    assert.equal(result.status, 0, result.stderr || result.error?.message);
    return result;
  };
  return { ...sb, cwd, out, revision, run, build, env: context.env };
}

test('a progressing cross-build can outlive the old per-command limit', t => {
  let elapsed = 0;
  t.mock.method(performance, 'now', () => elapsed);
  const budgets = [];
  const f = fixture(t, { executeCommand(command, args, options) {
    const innerBudget = Number(args[args.indexOf('--timeout-ms') + 1]);
    budgets.push([options.timeoutMs, innerBudget]);
    if (options.timeoutMs < 150_000 || innerBudget < 150_000) return { status: 1, stdout: 'compiling\n', stderr: 'build timed out\n' };
    elapsed += 150_000;
    return { status: 0, stdout: 'compiling\nfinished\n', stderr: '' };
  } });
  for (const target of ['linux/amd64', 'darwin/arm64']) {
    const result = f.run(['--out', f.out, '--target', target]);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stdout, /compiling[\s\S]*finished/);
  }
  const initial = buildTimeoutMs;
  assert.deepEqual(budgets, [[initial, initial - 1000], [initial - 150_000, initial - 151_000]]);
});

test('a hung cross-build fails with the command, target and elapsed time', t => {
  const f = fixture(t, { timeoutScale: 1 / 3000 });
  f.write(f.cwd, 'scripts/build.mjs', `// SPDX-License-Identifier: Apache-2.0
setInterval(() => {}, 1000);
`);
  const result = f.run(['--out', f.out, '--target', 'darwin/arm64']);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /command timed out after [\d.]+ s.*build\.mjs.*--target darwin\/arm64/);
});

test('cold inner Go compilations share the build deadline and can exceed 120 seconds', t => {
  const f = fixture(t);
  let elapsed = 0;
  t.mock.method(performance, 'now', () => elapsed);
  const budgets = [];
  const manifest = build({ cwd: f.cwd, out: f.out, target: 'darwin/arm64',
    execute(command, args, options) {
      if (args[0] === 'env') return { status: 0, stdout: options.env.GOTOOLCHAIN, stderr: '' };
      if (args[0] === 'build') {
        const budget = options.timeoutMs ?? 120_000;
        budgets.push(budget);
        // A controlled clock models each cold compilation needing 150 s.
        // The fake command fails at its assigned deadline, just as execute does.
        if (budget < 150_000) return { status: 1, stdout: 'compiling\n', stderr: 'cold compilation timed out at its command budget\n' };
        elapsed += 150_000;
        const binary = args[args.indexOf('-o') + 1];
        f.write(dirname(binary), binary.split(/[\\/]/).at(-1), 'fixture executable');
      }
      return { status: 0, stdout: args[0] === 'run'
        ? (args.includes('./internal/core/manifest/clientinfo') ? '[]' : '{}') : '', stderr: '' };
    } });
  assert.equal(manifest.target, 'darwin/arm64');
  assert.deepEqual(budgets, [buildTimeoutMs, buildTimeoutMs - 150_000]);
});

test('an exhausted build deadline stops before another Go command starts', t => {
  const f = fixture(t);
  let elapsed = 0;
  let builds = 0;
  t.mock.method(performance, 'now', () => elapsed);
  assert.throws(() => build({ cwd: f.cwd, out: f.out, timeoutMs: 10_000,
    execute(command, args, options) {
      if (args[0] === 'build') { builds++; elapsed = 10_000; }
      return { status: 0, stdout: args[0] === 'env' ? options.env.GOTOOLCHAIN : '', stderr: '' };
    } }), /build budget exhausted after 10\.000 s before: go build/);
  assert.equal(builds, 1);
});

test('a hung inner compilation reports its command and cleans up its Go context', t => {
  const f = fixture(t);
  f.write(f.dir, 'hung.mjs', '// SPDX-License-Identifier: Apache-2.0\nsetInterval(() => {}, 1000);\n');
  let contextHome;
  assert.throws(() => build({ cwd: f.cwd, out: f.out,
    execute(command, args, options) {
      contextHome = options.env.HOME;
      if (args[0] === 'build') return execute(process.execPath, [join(f.dir, 'hung.mjs')], {
        ...options, timeoutMs: Math.ceil(options.timeoutMs / 3000),
      });
      return { status: 0, stdout: args[0] === 'env' ? options.env.GOTOOLCHAIN : '', stderr: '' };
    } }), /go build failed: command timed out after [\d.]+ s.*hung\.mjs/);
  assert.equal(existsSync(contextHome), false);
});

test('invalid build budgets fail before creating artifacts', t => {
  const f = fixture(t);
  for (const timeoutMs of [0, -1, NaN, Infinity, 1.5]) {
    assert.throws(() => build({ cwd: f.cwd, out: f.out, timeoutMs }), /budget must be a positive integer/);
  }
  assert.equal(existsSync(f.out), false);
});

test('requires an output directory and rejects unknown arguments', t => {
  const f = fixture(t);
  for (const args of [[], ['--out'], ['--out', ''], ['--unknown'], ['unexpected']]) {
    const result = f.run(args);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /usage:/i);
  }
});

test('cross-builds Linux and macOS binaries with target metadata and an explicit release version', t => {
  const f = fixture(t);
  for (const target of ['linux/amd64', 'darwin/arm64']) {
    const out = join(f.dir, target);
    const result = f.run(['--out', out, '--target', target, '--version', '0.0.1-alpha.20261021.1']);
    assert.equal(result.status, 0, `${target}: ${result.stderr ?? ''}${result.error?.message ?? ''}`);
    const manifest = JSON.parse(readFileSync(join(out, 'manifest.json'), 'utf8'));
    assert.equal(manifest.target, target);
    assert.equal(manifest.supportedCombinations.length, 1);
    assert.equal(manifest.supportedCombinations[0].platform, target);
    assert.equal(manifest.version, '0.0.1-alpha.20261021.1');
    for (const binary of manifest.binaries) {
      const path = join(out, binary.name);
      const bytes = readFileSync(path);
      assert.equal(binary.sha256, createHash('sha256').update(bytes).digest('hex'));
      const info = spawnSync('go', ['version', '-m', path], { env: f.env, encoding: 'utf8' });
      assert.equal(info.status, 0, info.stderr);
      assert.match(info.stdout, new RegExp(`GOOS=${target.split('/')[0]}`));
      assert.match(info.stdout, new RegExp(`GOARCH=${target.split('/')[1]}`));
      assert.match(info.stdout, /CGO_ENABLED=0/);
      if (binary.name === 'lina') {
        assert.ok(info.stdout.includes(`${manifest.sqlite.module}\t${manifest.sqlite.version}\t${manifest.sqlite.moduleSum}`),
          'Core must link the binding recorded in its manifest');
      }
      assert.equal(bytes.subarray(0, 4).toString('hex'), target.startsWith('linux') ? '7f454c46' : 'cffaedfe');
    }
  }
});

test('Node and metadata readers do not import the model adapter', t => {
  const f = fixture(t);
  const result = spawnSync('go', ['list', '-deps', './cmd/lina-node', './internal/core/manifest/...'], {
    cwd: f.cwd, env: f.env, encoding: 'utf8', timeout: 120_000,
  });
  assert.equal(result.status, 0, result.stderr || result.error?.message);
  const dependencies = result.stdout.trim().split('\n');
  assert.deepEqual(dependencies.filter(name => name.startsWith('github.com/openai/')
    || name === 'github.com/thisisjun786/lina/kit/provider/responses'), []);
});

test('Responses adapter does not compile a provider SDK', t => {
  const f = fixture(t);
  const result = spawnSync('go', ['list', '-deps', './kit/provider/responses/...'], {
    cwd: f.cwd, env: f.env, encoding: 'utf8', timeout: 120_000,
  });
  assert.equal(result.status, 0, result.stderr || result.error?.message);
  assert.deepEqual(result.stdout.trim().split('\n').filter(name => name.startsWith('github.com/openai/')), []);
});

test('clean builds record hashes and the same commit and metadata as both version commands', t => {
  const f = fixture(t);
  for (const version of ['0.0.1-alpha.dev', '0.0.1-alpha.20261020.2']) {
    if (version !== '0.0.1-alpha.dev') {
      // Multiple releases can tag one commit; use the highest valid release tag.
      for (const tag of ['v0.0.1-alpha.20261020.1', `v${version}`, 'v0.0.1-alpha.20269999.3', 'unrelated']) {
        f.g(f.cwd, 'tag', tag);
      }
    }
    f.build();
    const manifest = JSON.parse(readFileSync(join(f.out, 'manifest.json'), 'utf8'));
    assert.equal(manifest.version, version);
    assert.equal(manifest.commit, f.revision);
    assert.match(manifest.date, /^\d{4}-\d{2}-\d{2}$/);
    assert.equal(manifest.goToolchain, f.env.GOTOOLCHAIN);
    assert.equal(manifest.kitVersion, Number(readFileSync(join(f.cwd, 'kit/VERSION'), 'utf8')));
    assert.deepEqual(manifest.verifiedWorkerVersions, { codex: ['0.163.0'] });
    const platform = spawnSync('go', ['env', 'GOOS', 'GOARCH'], { cwd: f.cwd, env: f.env, encoding: 'utf8' }).stdout.trim().split(/\r?\n/).join('/');
    assert.deepEqual(manifest.supportedCombinations, [{ connection: 'client', peer: 'lina-tui', platform,
      protocolVersion: '1.0', capabilities: [{ name: 'planning', version: '1.0' }, { name: 'progress', version: '1.0' }, { name: 'worker_lanes', version: '1.0' }, { name: 'web', version: '1.0' }, { name: 'github_merge', version: '1.0' }, { name: 'github', version: '1.0' }, { name: 'agents', version: '1.0' }, { name: 'memory', version: '1.0' }, { name: 'exchanges', version: '1.0' }, { name: 'channels', version: '1.0' }, { name: 'accounts', version: '1.0' }, { name: 'sign_in_relay', version: '1.0' }, { name: 'lifecycle', version: '1.0' }, { name: 'conversation', version: '1.0' }, { name: 'turn_activity', version: '1.0' }, { name: 'replay', version: '1.0' }, { name: 'growth', version: '1.0' }, { name: 'waiting', version: '1.0' }, { name: 'running', version: '1.0' }], evidence: ['go test ./e2e/m1 -run TestM3PlanDrafting', 'go test ./e2e/m1 -run TestM2ProgressWaitingInstalledCore', 'go test ./e2e/m1 -run TestM3WorkerLanes', 'go test ./e2e/m1 -run TestM1Web', 'go test ./e2e/m1 -run TestM2GitHubMergeInstalledCore', 'go test ./e2e/m1 -run TestM2GitHubCIInstalledCore', 'go test ./kit/protocol/conformance', 'go test ./e2e/m1 -run TestPlannedRestart', 'go test ./e2e/m1 -run TestM1TUIConversationInstalledCore', 'go test ./e2e/m1 -run TestM1InstalledToolActivity', 'go test ./e2e/m1 -run TestM1TUIAccountsInstalledCore', 'go test ./e2e/m1 -run TestM1TUIChannel', 'go test ./e2e/m1 -run TestM1TUITalkLogInstalledCore', 'go test ./e2e/m1 -run TestM1TUIMemoryAgentsInstalledCore', 'go test ./e2e/m1 -run TestM1ReplayExportInstalledCore', 'go test ./e2e/m1 -run TestM2InstalledPersonaGrowth', 'go test ./e2e/m1 -run TestM1TUIApprovalsInstalledCore', 'go test ./e2e/m1 -run TestM1TurnBudgetHandoffInstalledCore'] }]);
    const module = JSON.parse(spawnSync('go', ['list', '-m', '-json', 'github.com/ncruces/go-sqlite3'], {
      cwd: f.cwd, env: f.env, encoding: 'utf8',
    }).stdout);
    assert.equal(manifest.sqlite.module, module.Path);
    assert.equal(manifest.sqlite.version, module.Version);
    assert.equal(manifest.sqlite.moduleSum, module.Sum);
    assert.equal(manifest.sqlite.wasmSHA256, createHash('sha256').update(readFileSync(join(f.cwd, 'internal/core/canon/sqlite-vec.wasm'))).digest('hex'));
    assert.match(manifest.sqlite.sqliteVersion, /^3\.\d+\.\d+$/);
    assert.equal(manifest.sqlite.execution, 'wazero');
    assert.deepEqual(manifest.sqlite.sqliteVec, { version: 'v0.1.6', sha256: manifest.sqlite.wasmSHA256 });
    const providerConfig = JSON.parse(readFileSync(join(f.cwd, 'kit/provider/responses/manifest.json'), 'utf8'));
    assert.deepEqual(manifest.providers, providerConfig);
    assert.equal(manifest.providers.chatgpt.adapter, 'responses');
    assert.equal(manifest.providers.chatgpt.retryCount, 0);
    assert.deepEqual(manifest.crwPin, JSON.parse(readFileSync(join(f.cwd, 'internal/core/work/pin/pin.json'), 'utf8')));
    assert.deepEqual(manifest.binaries.map(binary => binary.name), ['lina', 'lina-node'].map(name => name + (process.platform === 'win32' ? '.exe' : '')));
    for (const binary of manifest.binaries) {
      const path = join(f.out, binary.name);
      const bytes = readFileSync(path);
      assert.equal(binary.size, bytes.length);
      assert.equal(binary.sha256, createHash('sha256').update(bytes).digest('hex'));
      const result = spawnSync(path, ['version'], { cwd: f.out, env: { ...f.env, PATH: '' }, encoding: 'utf8' });
      assert.equal(result.status, 0, result.stderr);
      assert.equal(result.stdout, `${manifest.version} (commit ${manifest.commit.slice(0, 7)}, built ${manifest.date})\n`);
      // Resolve the displayed abbreviation to prove full commit identity as well.
      const printedCommit = result.stdout.match(/commit ([a-f0-9]+)/)[1];
      assert.equal(f.g(f.cwd, 'rev-parse', printedCommit), manifest.commit);
    }
  }
  // Read the actual writer's output through the Go API in a separate process.
  f.write(f.cwd, 'check/main.go', `// SPDX-License-Identifier: Apache-2.0
package main
import ("os"; "fmt"; "github.com/thisisjun786/lina/internal/core/manifest")
func main() { m, err := manifest.Read(os.Args[1]); if err != nil { panic(err) }; fmt.Print(m.Commit) }
`);
  const result = spawnSync('go', ['run', './check', join(f.out, 'manifest.json')], { cwd: f.cwd, env: f.env, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, f.revision);
});

test('an unignored in-tree output directory does not make generated artifacts dirty source', t => {
  const f = fixture(t);
  const out = join(f.cwd, 'artifacts');
  for (let attempt = 0; attempt < 2; attempt++) {
    const result = f.run(['--out', out]);
    assert.equal(result.status, 0, result.stderr);
    assert.ok(existsSync(join(out, 'manifest.json')));
  }
  f.write(out, 'local-source.txt', 'uncommitted input\n');
  assert.equal(f.run(['--out', out]).status, 0);
  assert.equal(existsSync(join(out, 'manifest.json')), false);
});

test('builds through a symlinked ancestor exclude only generated artifacts from source checks', t => {
  const f = fixture(t);
  const alias = join(f.dir, 'source alias');
  symlinkSync(f.cwd, alias, process.platform === 'win32' ? 'junction' : 'dir');
  const out = join(alias, 'new', 'artifacts');
  for (let attempt = 0; attempt < 2; attempt++) {
    const result = f.run(['--out', out]);
    assert.equal(result.status, 0, result.stderr);
    assert.ok(existsSync(join(out, 'manifest.json')));
  }
  f.write(out, 'local-source.txt', 'uncommitted input\n');
  assert.equal(f.run(['--out', out]).status, 0);
  assert.equal(existsSync(join(out, 'manifest.json')), false);
});

test('a failed build removes an old manifest', t => {
  const f = fixture(t);
  f.write(f.out, 'manifest.json', '{}\n');
  f.write(f.cwd, 'cmd/lina/main.go', 'this is invalid Go\n');
  const result = f.run();
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /go build failed/);
  assert.equal(existsSync(join(f.out, 'manifest.json')), false);
});

test('invalid kit versions fail without retaining a manifest', t => {
  const f = fixture(t);
  for (const version of ['0', '-1', '1.5', 'one', '9007199254740992']) {
    f.write(f.out, 'manifest.json', '{}\n');
    f.write(f.cwd, 'kit/VERSION', version + '\n');
    const result = f.run();
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /kit\/VERSION/);
    assert.equal(existsSync(join(f.out, 'manifest.json')), false);
  }
});

test('source edits or a new clean commit during a build suppress its manifest', t => {
  const f = fixture(t);
  for (const change of ['edit', 'commit']) {
    f.write(f.out, 'manifest.json', '{}\n');
    let builds = 0;
    const manifest = build({ cwd: f.cwd, out: f.out, execute: (command, args, { env }) => {
      if (args[0] === 'env') return { status: 0, stdout: env.GOTOOLCHAIN, stderr: '' };
      if (args[0] === 'mod') return { status: 0, stdout: '', stderr: '' };
      assert.equal(command, 'go');
      if (args.includes('./persona/validate')) return { status: 0, stdout: '', stderr: '' };
      assert.equal(args[0], 'build');
      const binary = args[args.indexOf('-o') + 1];
      f.write(dirname(binary), binary.split(/[\\/]/).at(-1), 'fixture executable');
      if (++builds === 2) {
        f.write(f.cwd, 'local.txt', change);
        if (change === 'commit') f.commit(f.cwd, 'test: change source while building');
      }
      return { status: 0, stdout: '', stderr: '' };
    } });
    assert.equal(builds, 2);
    assert.equal(manifest, null);
    assert.equal(existsSync(join(f.out, 'manifest.json')), false);
    if (change === 'edit') f.commit(f.cwd, 'test: reset clean source');
  }
});

test('tracked source files cannot be overwritten by build artifacts', t => {
  const f = fixture(t);
  const out = join(f.cwd, 'tracked');
  f.write(out, 'manifest.json', 'source to preserve\n');
  f.commit(f.cwd, 'test: track source with an artifact name');
  const result = f.run(['--out', out]);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /tracked source/);
  assert.equal(readFileSync(join(out, 'manifest.json'), 'utf8'), 'source to preserve\n');
});

test('symlinked output paths cannot overwrite tracked source files', async t => {
  for (const kind of ['output directory', 'ancestor', 'missing descendants', 'source directory']) {
    await t.test(kind, t => {
      const f = fixture(t);
      const target = join(f.cwd, 'tracked', 'nested');
      const names = ['manifest.json', ...['lina', 'lina-node'].map(name => name + (process.platform === 'win32' ? '.exe' : ''))];
      for (const name of names) f.write(target, name, `tracked ${name}\n`);
      f.commit(f.cwd, 'test: track files behind an output alias');
      const alias = join(f.dir, 'directory alias');
      symlinkSync(kind === 'output directory' ? target : f.cwd, alias, process.platform === 'win32' ? 'junction' : 'dir');
      const cwd = kind === 'source directory' ? alias : f.cwd;
      const out = kind === 'output directory' ? alias : kind === 'source directory' ? target : join(alias, 'tracked', 'nested');
      if (kind === 'missing descendants') rmSync(join(f.cwd, 'tracked'), { recursive: true });

      let failure;
      try {
        build({ cwd, out, execute: () => { throw new Error('Go must not run for tracked output artifacts'); } });
      } catch (error) {
        failure = error;
      }
      if (kind === 'missing descendants') {
        assert.equal(existsSync(join(f.cwd, 'tracked')), false, 'guard must run before creating output directories');
      } else {
        for (const name of names) {
          assert.ok(existsSync(join(target, name)), `tracked ${name} must not be removed`);
          assert.equal(readFileSync(join(target, name), 'utf8'), `tracked ${name}\n`);
        }
      }
      assert.match(failure?.message ?? '', /tracked source/);
    });
  }
});

test('dirty tracked, staged and untracked sources build without retaining a manifest', t => {
  const f = fixture(t);
  for (const kind of ['tracked', 'staged', 'untracked']) {
    const file = kind === 'untracked' ? 'scratch.txt' : 'cmd/lina/version.go';
    const original = kind === 'untracked' ? '' : readFileSync(join(f.cwd, file), 'utf8');
    f.write(f.cwd, file, original + '\n// local change\n');
    if (kind === 'staged') f.g(f.cwd, 'add', file);
    // A prior successful build must not leave misleading provenance after a dirty rebuild.
    f.write(f.out, 'manifest.json', '{}\n');
    f.build();
    assert.equal(existsSync(join(f.out, 'manifest.json')), false, kind);
    for (const name of ['lina', 'lina-node']) {
      const binary = join(f.out, name + (process.platform === 'win32' ? '.exe' : ''));
      const result = spawnSync(binary, ['version'], { env: f.env, encoding: 'utf8' });
      assert.equal(result.status, 0, result.stderr);
      assert.match(result.stdout, /^0\.0\.1-alpha\.dev \(commit [a-f0-9]{7}, built \d{4}-\d{2}-\d{2}\)\n$/);
      const info = spawnSync('go', ['version', '-m', binary], { env: f.env, encoding: 'utf8' });
      assert.equal(info.status, 0, info.stderr);
      assert.match(info.stdout, /-trimpath=true/);
      assert.match(info.stdout, /CGO_ENABLED=0/);
    }
    f.commit(f.cwd, 'test: accept local change');
  }
});

test('build validates shipped persona pairs before producing release artifacts', t => {
  const f = fixture(t);
  let validated = false;
  let reject = false;
  const run = (command, args, options) => {
    if (args[0] === 'env') return { status: 0, stdout: options.env.GOTOOLCHAIN, stderr: '' };
    if (args.includes('./persona/validate')) {
      validated = true;
      if (reject) return { status: 1, stdout: '', stderr: 'persona validation failed' };
    }
    if (args[0] === 'build') {
      assert.equal(validated, true);
      const binary = args[args.indexOf('-o') + 1];
      f.write(dirname(binary), binary.split(/[\\/]/).at(-1), 'fixture executable');
    }
    return { status: 0, stdout: args[0] === 'run' ? '{}' : '', stderr: '' };
  };
  assert.ok(build({ cwd: f.cwd, out: f.out, execute: run }));
  assert.equal(validated, true);
  reject = true;
  validated = false;
  assert.throws(() => build({ cwd: f.cwd, out: f.out, execute: run }), /persona validation failed/);
  assert.equal(validated, true);
  assert.equal(existsSync(join(f.out, 'manifest.json')), false);
});
