// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { delimiter, join } from 'node:path';
import { createServer } from 'node:net';
import test from 'node:test';
import { createHash } from 'node:crypto';
import { git, run } from './git.mjs';
import { release } from './release.mjs';
import { developmentRepository, installBinaryBuildFixture, sandbox, scriptsDir } from './test-fixture.mjs';

// Binary release assembly requires Linux user namespaces and bubblewrap.
const releaseTest = process.platform === 'linux' ? test : test.skip;

test('source archives exclude the one-time takeover tool', t => {
  const sb = sandbox(t);
  const { work } = developmentRepository(sb);
  const commit = sb.commit(work, 'test: operator tool', {
    '.gitattributes': readFileSync(new URL('../.gitattributes', import.meta.url), 'utf8'),
    'tools/crw-takeover/main.go': 'package main\nfunc main() {}\n',
    'tools/crw-takeover/testdata/snapshot.json': '{}\n',
    'e2e/m1/crw_takeover_test.go': 'package e2e_test\n',
    'e2e/m3/crw-takeover.json': '{}\n',
    'e2e/m1/operational_switch_test.go': 'package e2e_test\n',
    'e2e/m3/operational-switch.json': '{}\n',
    'cmd/lina/main.go': 'package main\nfunc main() {}\n',
  });
  const archive = join(sb.dir, 'source.tar');
  sb.g(work, 'archive', '--format=tar', '-o', archive, commit);
  const contents = run('tar', ['-tf', archive], { cwd: work });
  assert.equal(contents.status, 0, contents.stderr);
  assert.ok(contents.stdout.includes('cmd/lina/main.go'));
  assert.equal(contents.stdout.includes('tools/crw-takeover'), false, contents.stdout);
  assert.equal(contents.stdout.includes('crw_takeover_test.go'), false, contents.stdout);
  assert.equal(contents.stdout.includes('crw-takeover.json'), false, contents.stdout);
  assert.equal(contents.stdout.includes('operational_switch_test.go'), false, contents.stdout);
  assert.equal(contents.stdout.includes('operational-switch.json'), false, contents.stdout);
});

// The release functions read HOME, PATH and the gh fixture variables from the environment.
function setup(t) {
  const sb = sandbox(t);
  const bin = join(sb.dir, 'bin');
  mkdirSync(bin);
  const ghLog = join(sb.dir, 'gh.log');
  writeFileSync(ghLog, '');
  writeFileSync(join(bin, 'gh'), `#!/usr/bin/env bash
echo "$*" >> "$GH_LOG"
case "$1 $2" in
  "repo view") printf '%s\\n' "\${GH_PRIVATE:-true}" ;;
  "release create") if [[ -n "\${GH_FAIL:-}" ]]; then echo "boom" >&2; exit 1; fi ;;
  *) echo "unexpected gh call: $*" >&2; exit 90 ;;
esac
`);
  chmodSync(join(bin, 'gh'), 0o755);
  const saved = {};
  const set = (name, value) => {
    if (!(name in saved)) saved[name] = process.env[name];
    if (value === undefined) delete process.env[name];
    else process.env[name] = value;
  };
  set('HOME', sb.dir);
  set('XDG_CONFIG_HOME', sb.dir);
  set('PATH', `${bin}${delimiter}${process.env.PATH}`);
  set('GH_LOG', ghLog);
  set('GH_PRIVATE', undefined);
  set('GH_FAIL', undefined);
  t.after(() => {
    for (const [name, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  });

  const repository = developmentRepository(sb);
  const { work, remote } = repository;
  installBinaryBuildFixture(sb, work);
  // main starts at the first commit; the release is the next dev commit.
  sb.g(work, 'push', '-q', 'origin', 'dev:refs/heads/main');
  const first = sb.g(work, 'rev-parse', 'HEAD');
  const commit = sb.commit(work, 'docs: second', { 'docs/design/guide.md': '# Guide\n\nSecond.\n' });
  sb.g(work, 'push', '-q', 'origin', 'dev');
  const notesFile = join(sb.dir, 'notes.md');
  writeFileSync(notesFile, 'Release notes.\n');
  const logs = [];
  const call = (options = {}) => release({ cwd: work, commit, tag: 'v0.0.1-alpha.20261020.1', notesFile, outDir: join(sb.dir, 'out'), log: (line) => logs.push(line), ...options });
  const ghCalls = () => readFileSync(ghLog, 'utf8').split('\n').filter(Boolean);
  const remoteRef = (ref) => sb.g(remote, 'rev-parse', '--verify', '--quiet', ref);
  const remoteHas = (ref) => git(remote, ['rev-parse', '--verify', '--quiet', ref], { env: sb.env }).status === 0;
  return { sb, set, work, remote, first, commit, notesFile, logs, call, ghCalls, remoteRef, remoteHas };
}

const untouched = (s) => {
  assert.equal(s.remoteRef('refs/heads/main'), s.first);
  assert.equal(s.remoteHas('refs/tags/v0.0.1-alpha.20261020.1'), false);
  assert.equal(s.ghCalls().some((line) => line.startsWith('release create')), false);
};

releaseTest('the default dry run builds a dated alpha release and publishes nothing', (t) => {
  const s = setup(t);
  const summary = s.call();
  assert.equal(summary.mode, 'dry-run');
  assert.equal(summary.evidence, 'rerun');
  assert.match(s.logs.join('\n'), /running foundation on a clean work tree/);
  untouched(s);
  const out = join(s.sb.dir, 'out');
  assert.deepEqual(readdirSync(out).sort(), ['SHA256SUMS',
    'lina-0.0.1-alpha.20261020.1-darwin-arm64.tar.gz', 'lina-0.0.1-alpha.20261020.1-linux-amd64.tar.gz',
    'lina-0.0.1-alpha.20261020.1-source.tar.gz', 'release-manifest.json']);
  const manifest = JSON.parse(readFileSync(join(out, 'release-manifest.json'), 'utf8'));
  assert.equal(manifest.tag, 'v0.0.1-alpha.20261020.1');
  assert.equal(manifest.version, '0.0.1-alpha.20261020.1');
  assert.equal(manifest.commit, s.commit);
  assert.equal(manifest.tree, s.sb.g(s.work, 'rev-parse', `${s.commit}^{tree}`));
  assert.equal(manifest.prerelease, true);
  assert.equal(manifest.foundation.result, 'success');
  const archive = join(out, 'lina-0.0.1-alpha.20261020.1-source.tar.gz');
  assert.equal(manifest.artifacts[0].sha256, createHash('sha256').update(readFileSync(archive)).digest('hex'));
  assert.ok(readFileSync(join(out, 'SHA256SUMS'), 'utf8').includes(`${manifest.artifacts[0].sha256}  lina-0.0.1-alpha.20261020.1-source.tar.gz\n`));
  assert.equal(run('tar', ['-tzf', archive], { cwd: out }).stdout.includes('lina-0.0.1-alpha.20261020.1/README.md'), true);
  for (const target of ['linux/amd64', 'darwin/arm64']) {
    const artifact = manifest.artifacts.find(a => a.target === target);
    assert.ok(artifact, `missing ${target}`);
    const path = join(out, artifact.name);
    assert.equal(artifact.sha256, createHash('sha256').update(readFileSync(path)).digest('hex'));
    const contents = run('tar', ['-tzf', path], { cwd: out });
    assert.equal(contents.status, 0);
    for (const name of ['lina', 'lina-node', 'manifest.json', 'LICENSE', 'THIRD-PARTY-NOTICES.md', 'sqlite-vec-NOTICE.md']) {
      assert.ok(contents.stdout.split('\n').includes(name), name);
    }
    const built = JSON.parse(run('tar', ['-xOzf', path, 'manifest.json'], { cwd: out }).stdout);
    assert.equal(built.commit, s.commit);
    assert.equal(built.version, manifest.version);
    assert.equal(built.target, target);
  }
  // A dry run leaves no evidence note behind and no extra work tree.
  assert.notEqual(git(s.work, ['notes', '--ref=refs/notes/foundation', 'show', s.commit], { env: s.sb.env }).status, 0);
  assert.equal(s.sb.g(s.work, 'worktree', 'list').split('\n').length, 1);
});

releaseTest('recorded foundation evidence for the exact tree is used without running foundation again', (t) => {
  const s = setup(t);
  const verified = run(process.execPath, [join(s.work, 'scripts/foundation.mjs'), '--full'], { cwd: s.work, env: s.sb.env });
  assert.equal(verified.status, 0, verified.stderr);
  const summary = s.call();
  assert.equal(summary.evidence, 'note');
  assert.doesNotMatch(s.logs.join('\n'), /running foundation/);
  // A note for another tree is not evidence.
  const other = s.sb.commit(s.work, 'docs: third', { 'docs/design/guide.md': '# Guide\n\nThird.\n' });
  s.sb.g(s.work, 'notes', '--ref=refs/notes/foundation', 'add', '-f', '-m', JSON.stringify({ result: 'success', head: other, tree: s.commit }), other);
  s.sb.g(s.work, 'push', '-q', 'origin', 'dev');
  assert.equal(s.call({ commit: other, outDir: join(s.sb.dir, 'out-other') }).evidence, 'rerun');
});

releaseTest('publishing fast-forwards main, pushes the tag and creates the private release with its artifacts', (t) => {
  const s = setup(t);
  const summary = s.call({ publish: true });
  assert.equal(summary.mode, 'publish');
  assert.equal(s.remoteRef('refs/heads/main'), s.commit);
  assert.equal(s.remoteRef('refs/tags/v0.0.1-alpha.20261020.1'), s.commit);
  const create = s.ghCalls().find((line) => line.startsWith('release create'));
  assert.ok(create.startsWith('release create v0.0.1-alpha.20261020.1 '));
  for (const fragment of ['lina-0.0.1-alpha.20261020.1-source.tar.gz', 'lina-0.0.1-alpha.20261020.1-linux-amd64.tar.gz',
    'lina-0.0.1-alpha.20261020.1-darwin-arm64.tar.gz', 'release-manifest.json', 'SHA256SUMS', '--verify-tag', '--prerelease', `--notes-file ${s.notesFile}`]) {
    assert.ok(create.includes(fragment), fragment);
  }
  assert.equal(s.ghCalls().filter((line) => line.startsWith('repo view')).length, 1);
  // A published evidence note exists for the exact commit because the rerun wrote it.
  assert.equal(JSON.parse(s.sb.g(s.work, 'notes', '--ref=refs/notes/foundation', 'show', s.commit)).result, 'success');
  // Tags are immutable: the same release cannot run twice.
  assert.throws(() => s.call({ publish: true, outDir: join(s.sb.dir, 'again') }), /already exists/);
});

releaseTest('binary builds use the exact candidate and receive no account credentials', t => {
  const s = setup(t);
  s.set('GH_TOKEN', 'synthetic-do-not-forward');
  s.set('GITHUB_TOKEN', 'synthetic-do-not-forward');
  s.sb.write(s.work, 'scripts/build.mjs', 'throw new Error("must not build the dirty checkout");\n');
  s.call();
  untouched(s);
  assert.equal(s.sb.g(s.work, 'worktree', 'list').split('\n').length, 1);
});

releaseTest('candidate build code cannot reach publisher files, processes, network or shared Git hooks', async t => {
  const s = setup(t);
  const secret = join(s.sb.dir, 'publisher-private');
  writeFileSync(secret, 'synthetic-private-value');
  const hook = join(s.work, '.git/hooks/pre-push');
  const marker = join(s.sb.dir, 'escaped-build');
  const source = readFileSync(join(s.work, 'scripts/build.mjs'), 'utf8');
  const server = createServer();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => server.close());
  const probe = `
import { existsSync, readFileSync, writeFileSync as tryWrite } from 'node:fs';
import { connect } from 'node:net';
if (existsSync(${JSON.stringify(secret)})) throw new Error('publisher files visible');
if (existsSync('/proc/${process.pid}/environ')) throw new Error('publisher process visible');
try { tryWrite(${JSON.stringify(hook)}, 'compromised'); } catch {}
try { tryWrite(${JSON.stringify(marker)}, 'escaped'); } catch {}
await new Promise((resolve, reject) => {
  const socket = connect({ host: '127.0.0.1', port: ${server.address().port} });
  socket.setTimeout(5000, () => { socket.destroy(); reject(new Error('network probe timed out')); });
  socket.on('connect', () => { socket.destroy(); reject(new Error('publisher network reachable')); });
  socket.on('error', resolve);
});
`;
  const commit = s.sb.commit(s.work, 'test: hostile candidate build', { 'scripts/build.mjs': probe + source });
  s.sb.g(s.work, 'push', '-q', 'origin', 'dev');
  // Foundation's separately recorded evidence avoids running the intentionally
  // hostile candidate as part of the foundation test fixture.
  s.sb.g(s.work, 'notes', '--ref=refs/notes/foundation', 'add', '-m', JSON.stringify({
    result: 'success', head: commit, tree: s.sb.g(s.work, 'rev-parse', `${commit}^{tree}`),
  }), commit);
  s.call({ commit });
  assert.equal(existsSync(hook), false);
  assert.equal(existsSync(marker), false);
  assert.equal(readFileSync(secret, 'utf8'), 'synthetic-private-value');
});

releaseTest('candidate artifacts cannot use symlinks to read publisher files', t => {
  const s = setup(t);
  const privateFile = join(s.sb.dir, 'publisher-private');
  writeFileSync(privateFile, 'synthetic-private-value');
  const source = readFileSync(join(s.work, 'scripts/build.mjs'), 'utf8');
  const commit = s.sb.commit(s.work, 'test: linked candidate output', { 'scripts/build.mjs': source + `
import { rmSync, symlinkSync } from 'node:fs';
rmSync(join(out, 'lina'));
symlinkSync(${JSON.stringify(privateFile)}, join(out, 'lina'));
` });
  s.sb.g(s.work, 'push', '-q', 'origin', 'dev');
  assert.throws(() => s.call({ commit }), /regular files, not links/);
  untouched(s);
});

releaseTest('releases a fetched remote commit while the local branch remains behind', t => {
  const s = setup(t);
  const peer = join(s.sb.dir, 'peer');
  s.sb.g(s.sb.dir, 'clone', '-q', s.remote, peer);
  const commit = s.sb.commit(peer, 'docs: remote-only release', { 'docs/design/guide.md': '# New release\n' });
  s.sb.g(peer, 'push', '-q', 'origin', 'dev');
  s.sb.g(s.work, 'fetch', '-q', 'origin');
  assert.equal(s.sb.g(s.work, 'rev-parse', 'dev'), s.commit);
  assert.equal(s.call({ commit }).commit, commit);
  untouched(s);
});

releaseTest('selects the candidate toolchain before entering the offline sandbox', t => {
  const s = setup(t);
  const actual = run('go', ['env', 'GOMODCACHE', 'GOROOT', 'GOVERSION'], { cwd: s.work }).stdout.trim().split('\n');
  const commit = s.sb.commit(s.work, 'test: pinned build toolchain', {
    'go.mod': `module example.invalid/release\ngo 1.27\ntoolchain ${actual[2]}\n`,
  });
  s.sb.g(s.work, 'push', '-q', 'origin', 'dev');
  s.sb.g(s.work, 'notes', '--ref=refs/notes/foundation', 'add', '-m', JSON.stringify({
    result: 'success', head: commit, tree: s.sb.g(s.work, 'rev-parse', `${commit}^{tree}`),
  }), commit);
  const wrapper = join(s.sb.dir, 'bin/go');
  writeFileSync(wrapper, `#!/usr/bin/env node
const fs = require('node:fs');
if (!fs.existsSync('go.mod') || process.env.GOTOOLCHAIN !== ${JSON.stringify(actual[2])}) {
  console.error('toolchain query must use the candidate directory and pin'); process.exit(1);
}
console.log(${JSON.stringify(actual.join('\n'))});
`);
  chmodSync(wrapper, 0o755);
  assert.equal(s.call({ commit }).commit, commit);
  untouched(s);
});

releaseTest('a binary build failure or digest mismatch stops publication and removes the worktree', t => {
  const s = setup(t);
  const source = readFileSync(join(s.work, 'scripts/build.mjs'), 'utf8');
  for (const [name, text, failure] of [
    ['failure', 'throw new Error("synthetic build failure");\n' + source, /Binary build failed/],
    ['digest', source.replace("createHash('sha256').update(bytes).digest('hex')", "'0'.repeat(64)"), /digest mismatch/],
  ]) {
    const commit = s.sb.commit(s.work, `test: ${name}`, { 'scripts/build.mjs': text });
    s.sb.g(s.work, 'push', '-q', 'origin', 'dev');
    assert.throws(() => s.call({ commit, publish: true, outDir: join(s.sb.dir, name) }), failure);
    untouched(s);
    assert.equal(s.sb.g(s.work, 'worktree', 'list').split('\n').length, 1);
  }
});

releaseTest('every failed condition stops before anything is pushed or published', (t) => {
  const s = setup(t);
  const emptyNotes = join(s.sb.dir, 'empty.md');
  writeFileSync(emptyNotes, ' \n');
  const localOnly = (() => {
    s.sb.g(s.work, 'checkout', '-q', '-b', 'local-only', 'dev');
    const id = s.sb.commit(s.work, 'docs: local only', { 'docs/design/guide.md': '# Guide\n\nLocal.\n' });
    s.sb.g(s.work, 'checkout', '-q', 'dev');
    return id;
  })();
  const cases = [
    ['a short commit id', { commit: s.commit.slice(0, 12) }, /40-character/],
    ['an unknown commit', { commit: '1'.repeat(40) }, /not in this repository/],
    ['a bad tag', { tag: 'v1.0' }, /version such as/],
    ['a tag with build metadata', { tag: 'v0.0.1-alpha.20261020.1+abc123' }, /--tag/],
    ['missing notes', { notesFile: undefined }, /--notes-file/],
    ['empty notes', { notesFile: emptyNotes }, /notes are empty/],
    ['a commit outside dev', { commit: localOnly }, /belong to dev/],
  ];
  for (const [name, options, pattern] of cases) {
    assert.throws(() => s.call({ ...options, publish: true }), pattern, name);
    untouched(s);
  }

  // main has a commit that the release does not contain.
  const other = join(s.sb.dir, 'other');
  s.sb.g(s.sb.dir, 'clone', '-q', '-b', 'main', s.remote, other);
  s.sb.commit(other, 'docs: main only', { 'docs/design/main-only.md': '# Main only\n' });
  s.sb.g(other, 'push', '-q', 'origin', 'main');
  const moved = s.remoteRef('refs/heads/main');
  assert.throws(() => s.call({ publish: true }), /fast-forward main/);
  assert.equal(s.remoteRef('refs/heads/main'), moved);
  s.sb.g(other, 'push', '-q', '--delete', 'origin', 'main');
  s.sb.g(s.work, 'push', '-q', 'origin', `${s.first}:refs/heads/main`);

  // The tag exists on the remote, even on the same commit.
  s.sb.g(s.work, 'push', '-q', 'origin', `${s.commit}:refs/tags/v0.0.1-alpha.20261020.1`);
  assert.throws(() => s.call({ publish: true }), /already exists on origin/);
  s.sb.g(s.work, 'push', '-q', 'origin', ':refs/tags/v0.0.1-alpha.20261020.1');

  // A local tag on another commit.
  s.sb.g(s.work, 'tag', 'v0.0.1-alpha.20261020.1', s.first);
  assert.throws(() => s.call({ publish: true }), /local tag .* identifies a different commit/);
  s.sb.g(s.work, 'tag', '-d', 'v0.0.1-alpha.20261020.1');

  // A public repository.
  s.set('GH_PRIVATE', 'false');
  assert.throws(() => s.call({ publish: true }), /not private/);
  s.set('GH_PRIVATE', undefined);

  // Foundation fails on the release commit.
  const broken = s.sb.commit(s.work, 'docs: broken', { 'README.md': '[gone](docs/design/gone.md)\n' });
  s.sb.g(s.work, 'push', '-q', 'origin', 'dev');
  assert.throws(() => s.call({ publish: true, commit: broken }), /Foundation failed/);
  untouched(s);
});

releaseTest('a failed main push publishes nothing and the same release can run again', (t) => {
  const s = setup(t);
  const hook = join(s.remote, 'hooks/update');
  writeFileSync(hook, '#!/bin/sh\nif [ "$1" = refs/heads/main ] && [ -e "$(dirname "$0")/../reject-main" ]; then echo "main is locked" >&2; exit 1; fi\n');
  chmodSync(hook, 0o755);
  writeFileSync(join(s.remote, 'reject-main'), '');
  assert.throws(() => s.call({ publish: true, outDir: join(s.sb.dir, 'first') }), /Fast-forwarding main failed; nothing was published/);
  untouched(s);
  rmSync(join(s.remote, 'reject-main'));
  s.call({ publish: true, outDir: join(s.sb.dir, 'second') });
  assert.equal(s.remoteRef('refs/heads/main'), s.commit);
  assert.equal(s.remoteRef('refs/tags/v0.0.1-alpha.20261020.1'), s.commit);
});

releaseTest('a failed GitHub Release after the tag is reported with the command that completes it', (t) => {
  const s = setup(t);
  s.set('GH_FAIL', '1');
  assert.throws(() => s.call({ publish: true }), (error) => /creating the GitHub Release failed/.test(error.message) && error.message.includes('gh release create v0.0.1-alpha.20261020.1 '));
  assert.equal(s.remoteRef('refs/heads/main'), s.commit);
  assert.equal(s.remoteRef('refs/tags/v0.0.1-alpha.20261020.1'), s.commit);
});

releaseTest('a named output directory must be empty so the release never deletes what it did not create', (t) => {
  const s = setup(t);
  const out = join(s.sb.dir, 'keep');
  mkdirSync(out);
  writeFileSync(join(out, 'precious.txt'), 'keep');
  assert.throws(() => s.call({ outDir: out }), /not empty/);
  assert.equal(existsSync(join(out, 'precious.txt')), true);
});

releaseTest('the command line is a dry run by default and ignores GIT_* variables of its caller', (t) => {
  const s = setup(t);
  const decoy = join(s.sb.dir, 'decoy');
  s.sb.g(s.sb.dir, 'init', '-q', decoy);
  s.sb.commit(decoy, 'decoy', { 'README.md': '# Decoy\n' });
  const args = [join(scriptsDir, 'release.mjs'), '--commit', s.commit, '--tag', 'v0.0.1-alpha.20261020.1', '--notes-file', s.notesFile, '--out', join(s.sb.dir, 'cli-out')];
  const env = { ...s.sb.env, GIT_DIR: join(decoy, '.git'), GIT_WORK_TREE: decoy };
  const result = run(process.execPath, args, { cwd: s.work, env });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).mode, 'dry-run');
  assert.equal(JSON.parse(result.stdout).tag, 'v0.0.1-alpha.20261020.1');
  assert.equal(s.remoteHas('refs/tags/v0.0.1-alpha.20261020.1'), false);
  const failed = run(process.execPath, args.slice(0, 1), { cwd: s.work, env });
  assert.equal(failed.status, 1);
  assert.match(failed.stderr, /40-character/);
});
