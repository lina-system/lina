// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, symlinkSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { sandbox } from './test-fixture.mjs';
import { createGoContext } from './verify/check-components.mjs';
import { build } from './build.mjs';

const root = dirname(dirname(fileURLToPath(import.meta.url)));

function fixture(t) {
  const sb = sandbox(t);
  const cwd = join(sb.dir, 'source');
  mkdirSync(cwd);
  for (const name of ['cmd/lina', 'cmd/lina-node', 'internal/buildinfo', 'kit', 'go.mod', 'go.sum', 'go.work',
    'internal/core', 'internal/clientapi', 'internal/tui', 'scripts/build.mjs', 'scripts/git.mjs', 'scripts/verify/check-components.mjs']) {
    if (!existsSync(join(root, name))) continue;
    mkdirSync(dirname(join(cwd, name)), { recursive: true });
    cpSync(join(root, name), join(cwd, name), { recursive: true });
  }
  sb.g(cwd, 'init', '-q', '--initial-branch=dev');
  const revision = sb.commit(cwd, 'test: build source');
  const context = createGoContext(cwd);
  t.after(context.close);
  const out = join(sb.dir, 'artifacts with spaces');
  const run = (args = ['--out', out]) => spawnSync(process.execPath, [join(cwd, 'scripts/build.mjs'), ...args], {
    cwd, env: context.env, encoding: 'utf8', timeout: 120_000,
  });
  const build = () => {
    const result = run();
    assert.equal(result.status, 0, result.stderr || result.error?.message);
    return result;
  };
  return { ...sb, cwd, out, revision, run, build, env: context.env };
}

test('requires an output directory and rejects unknown arguments', t => {
  const f = fixture(t);
  for (const args of [[], ['--out'], ['--out', ''], ['--unknown'], ['unexpected']]) {
    const result = f.run(args);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /usage:/i);
  }
});

test('build commands and metadata readers do not import the provider SDK', t => {
  const f = fixture(t);
  const result = spawnSync('go', ['list', '-deps', './cmd/lina', './cmd/lina-node', './internal/core/manifest/...'], {
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
    const module = JSON.parse(spawnSync('go', ['list', '-m', '-json', 'github.com/ncruces/go-sqlite3'], {
      cwd: f.cwd, env: f.env, encoding: 'utf8',
    }).stdout);
    assert.equal(manifest.sqlite.module, module.Path);
    assert.equal(manifest.sqlite.version, module.Version);
    assert.equal(manifest.sqlite.moduleSum, module.Sum);
    assert.equal(manifest.sqlite.wasmSHA256, createHash('sha256').update(readFileSync(join(module.Dir, 'embed/sqlite3.wasm'))).digest('hex'));
    assert.match(manifest.sqlite.sqliteVersion, /^3\.\d+\.\d+$/);
    assert.equal(manifest.sqlite.execution, 'wazero');
    assert.equal(manifest.sqlite.sqliteVec, null);
    const providerConfig = JSON.parse(readFileSync(join(f.cwd, 'kit/provider/responses/manifest.json'), 'utf8'));
    assert.deepEqual(manifest.providers, providerConfig);
    assert.equal(manifest.providers.chatgpt.adapter, 'responses');
    assert.equal(manifest.providers.chatgpt.retryCount, 0);
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
      assert.equal(command, 'go');
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
