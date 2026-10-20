// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';
import { spawnSync } from 'node:child_process';
import { hostname } from 'node:os';
import { git, pushRef, run } from './git.mjs';
import { acquireLock, integrate } from './integrate.mjs';
import { cloneOf, developmentRepository, sandbox, scriptsDir } from './test-fixture.mjs';

// The functions under test read the user's Git configuration from HOME. Point it at the fixture.
function isolate(t, sb) {
  const saved = { HOME: process.env.HOME, XDG_CONFIG_HOME: process.env.XDG_CONFIG_HOME };
  process.env.HOME = sb.dir;
  process.env.XDG_CONFIG_HOME = sb.dir;
  t.after(() => {
    for (const [name, value] of Object.entries(saved)) {
      if (value === undefined) delete process.env[name];
      else process.env[name] = value;
    }
  });
}

function setup(t) {
  const sb = sandbox(t);
  isolate(t, sb);
  const repository = developmentRepository(sb);
  const logs = [];
  const log = (line) => logs.push(line);
  const branch = (name, files, from = 'dev') => {
    sb.g(repository.work, 'checkout', '-q', '-b', name, from);
    const head = sb.commit(repository.work, `docs: ${name}`, files);
    sb.g(repository.work, 'checkout', '-q', 'dev');
    return head;
  };
  const worktrees = () => sb.g(repository.work, 'worktree', 'list', '--porcelain').split('\n').filter((line) => line.startsWith('worktree ')).length;
  return { sb, ...repository, logs, log, branch, worktrees };
}

const parents = (s, commit) => s.sb.g(s.work, 'rev-list', '--parents', '-n', '1', commit).split(' ').slice(1);
const remoteDev = (s) => s.sb.g(s.remote, 'rev-parse', 'refs/heads/dev');

test('a verified branch becomes a merge commit on dev, locally and on the remote', async (t) => {
  const s = setup(t);
  const previous = s.sb.g(s.work, 'rev-parse', 'dev');
  const feature = s.branch('feature', { 'docs/design/guide.md': '# Guide\n\nMore.\n' });
  const result = await integrate({ cwd: s.work, branch: 'feature', log: s.log });
  assert.equal(result.outcome, 'integrated');
  assert.deepEqual(parents(s, result.merge), [previous, feature]);
  assert.equal(s.sb.g(s.work, 'rev-parse', 'dev'), result.merge);
  assert.equal(remoteDev(s), result.merge);
  assert.equal(s.sb.g(s.work, 'log', '-1', '--format=%s', 'dev'), 'Merge feature into dev');
  assert.equal(readFileSync(join(s.work, 'docs/design/guide.md'), 'utf8'), '# Guide\n\nMore.\n');
  const note = JSON.parse(s.sb.g(s.work, 'notes', '--ref=refs/notes/foundation', 'show', result.merge));
  assert.deepEqual([note.result, note.head, note.base], ['success', result.merge, previous]);
  assert.equal(s.sb.g(s.remote, 'notes', '--ref=refs/notes/foundation', 'show', result.merge), JSON.stringify(note));
  assert.equal(s.worktrees(), 1);
  assert.equal(existsSync(join(s.work, '.git/lina-integrate.lock')), false);
});

test('the merge commit message comes from --message-file', async (t) => {
  const s = setup(t);
  s.branch('feature', { 'docs/design/guide.md': '# Guide\n\nMore.\n' });
  const messageFile = join(s.sb.dir, 'message.txt');
  writeFileSync(messageFile, 'docs: extend the guide\n\nProblem and approach.\n');
  const result = await integrate({ cwd: s.work, branch: 'feature', messageFile, log: s.log });
  assert.equal(s.sb.g(s.work, 'log', '-1', '--format=%B', result.merge), 'docs: extend the guide\n\nProblem and approach.');
});

test('a failing foundation leaves dev and the remote unchanged and removes the candidate work tree', async (t) => {
  const s = setup(t);
  const previous = s.sb.g(s.work, 'rev-parse', 'dev');
  s.branch('broken', { 'README.md': '[gone](docs/design/gone.md)\n' });
  await assert.rejects(integrate({ cwd: s.work, branch: 'broken', log: s.log }), /Foundation failed/);
  assert.equal(s.sb.g(s.work, 'rev-parse', 'dev'), previous);
  assert.equal(remoteDev(s), previous);
  assert.equal(s.worktrees(), 1);
  assert.equal(existsSync(join(s.work, '.git/lina-integrate.lock')), false);
  assert.match(s.logs.join('\n'), /broken target docs\/design\/gone\.md/);
});

test('a merge conflict is reported with the files and changes nothing', async (t) => {
  const s = setup(t);
  s.branch('feature', { 'docs/design/guide.md': '# Guide\n\nFrom the branch.\n' });
  const previous = s.sb.commit(s.work, 'docs: on dev', { 'docs/design/guide.md': '# Guide\n\nFrom dev.\n' });
  s.sb.g(s.work, 'push', '-q', 'origin', 'dev');
  await assert.rejects(integrate({ cwd: s.work, branch: 'feature', log: s.log }), /conflicts in:\ndocs\/design\/guide\.md/);
  assert.equal(s.sb.g(s.work, 'rev-parse', 'dev'), previous);
  assert.equal(s.worktrees(), 1);
});

test('a rejected push merges into the new dev head and runs foundation again', async (t) => {
  const s = setup(t);
  const competitor = cloneOf(s.sb, s.remote, 'competitor');
  s.branch('feature', { 'docs/design/feature.md': '# Feature\n' });
  let competing;
  const result = await integrate({
    cwd: s.work, branch: 'feature', log: s.log,
    hooks: {
      beforePush: (attempt) => {
        if (attempt !== 1) return;
        competing = s.sb.commit(competitor, 'docs: competing', { 'docs/design/other.md': '# Other\n' });
        s.sb.g(competitor, 'push', '-q', 'origin', 'dev');
      },
    },
  });
  assert.equal(result.attempts, 2);
  assert.equal(parents(s, result.merge)[0], competing);
  assert.equal(remoteDev(s), result.merge);
  assert.equal(s.sb.g(s.work, 'rev-parse', 'dev'), result.merge);
  assert.ok(existsSync(join(s.work, 'docs/design/other.md')) && existsSync(join(s.work, 'docs/design/feature.md')));
  assert.equal(s.logs.filter((line) => line.startsWith('Running foundation')).length, 2);
  assert.equal(JSON.parse(s.sb.g(s.work, 'notes', '--ref=refs/notes/foundation', 'show', result.merge)).base, competing);
});

test('gives up after the attempts allowed when dev keeps moving', async (t) => {
  const s = setup(t);
  const competitor = cloneOf(s.sb, s.remote, 'competitor');
  s.branch('feature', { 'docs/design/feature.md': '# Feature\n' });
  const previous = s.sb.g(s.work, 'rev-parse', 'dev');
  let count = 0;
  await assert.rejects(integrate({
    cwd: s.work, branch: 'feature', attempts: 2, log: s.log,
    hooks: { beforePush: () => { count += 1; s.sb.commit(competitor, `docs: competing ${count}`, { [`docs/design/c${count}.md`]: '# C\n' }); s.sb.g(competitor, 'push', '-q', 'origin', 'dev'); } },
  }), /dev moved on every one of 2 attempts/);
  assert.equal(s.sb.g(s.work, 'rev-parse', 'dev'), previous);
  assert.equal(s.worktrees(), 1);
});

test('dev advances by reference when it is not checked out, and an integrated branch is a no-op', async (t) => {
  const s = setup(t);
  s.branch('feature', { 'docs/design/guide.md': '# Guide\n\nMore.\n' });
  s.sb.g(s.work, 'checkout', '-q', 'feature');
  const result = await integrate({ cwd: s.work, branch: 'feature', log: s.log });
  assert.equal(s.sb.g(s.work, 'rev-parse', 'dev'), result.merge);
  assert.equal(s.sb.g(s.work, 'branch', '--show-current'), 'feature');
  const again = await integrate({ cwd: s.work, branch: 'feature', log: s.log });
  assert.equal(again.outcome, 'unchanged');
  assert.equal(remoteDev(s), result.merge);
});

test('an uncommitted dev checkout is refused before anything is pushed', async (t) => {
  const s = setup(t);
  const previous = s.sb.g(s.work, 'rev-parse', 'dev');
  s.branch('feature', { 'docs/design/guide.md': '# Guide\n\nMore.\n' });
  writeFileSync(join(s.work, 'notes.txt'), 'scratch\n');
  await assert.rejects(integrate({ cwd: s.work, branch: 'feature', log: s.log }), /uncommitted changes/);
  assert.equal(remoteDev(s), previous);
});

test('foundation notes of the remote and the local clone are merged before they are pushed', async (t) => {
  const s = setup(t);
  const other = cloneOf(s.sb, s.remote, 'other');
  const remoteCommit = s.sb.g(other, 'rev-parse', 'HEAD');
  s.sb.g(other, 'notes', '--ref=refs/notes/foundation', 'add', '-m', '{"remote":true}', remoteCommit);
  s.sb.g(other, 'push', '-q', 'origin', 'refs/notes/foundation:refs/notes/foundation');
  // Both notes are on the same commit, so the merge combines their lines.
  s.sb.g(s.work, 'notes', '--ref=refs/notes/foundation', 'add', '-m', '{"local":true}', 'HEAD');
  s.branch('feature', { 'docs/design/guide.md': '# Guide\n\nMore.\n' });
  const result = await integrate({ cwd: s.work, branch: 'feature', log: s.log });
  const merged = s.sb.g(s.remote, 'notes', '--ref=refs/notes/foundation', 'show', remoteCommit).split('\n').filter(Boolean).sort();
  assert.deepEqual(merged, ['{"local":true}', '{"remote":true}']);
  assert.equal(JSON.parse(s.sb.g(s.remote, 'notes', '--ref=refs/notes/foundation', 'show', result.merge)).result, 'success');
});

test('one integration at a time: a held lock refuses, a lock of a finished process is reclaimed', async (t) => {
  const s = setup(t);
  const release = await acquireLock(s.work, { waitSeconds: 0 });
  await assert.rejects(acquireLock(s.work, { waitSeconds: 0 }), /Another integration holds the lock/);
  s.branch('feature', { 'docs/design/guide.md': '# Guide\n\nMore.\n' });
  await assert.rejects(integrate({ cwd: s.work, branch: 'feature', waitSeconds: 0, log: s.log }), /holds the lock/);
  release();
  assert.equal(existsSync(join(s.work, '.git/lina-integrate.lock')), false);

  const lock = join(s.work, '.git/lina-integrate.lock');
  mkdirSync(lock);
  const dead = spawnSync(process.execPath, ['-e', 'process.stdout.write(String(process.pid))'], { encoding: 'utf8' }).stdout;
  writeFileSync(join(lock, 'owner.json'), JSON.stringify({ pid: Number(dead), host: hostname(), started: 'earlier' }));
  const result = await integrate({ cwd: s.work, branch: 'feature', waitSeconds: 0, log: s.log });
  assert.equal(result.outcome, 'integrated');
  assert.equal(existsSync(lock), false);
});

test('pushes never force: a forced refspec is refused and the scripts contain no force option', (t) => {
  const s = setup(t);
  assert.throws(() => pushRef(s.work, 'origin', '+refs/heads/dev:refs/heads/dev'), /Refusing to force-push/);
  for (const name of ['git.mjs', 'integrate.mjs', 'release.mjs']) {
    const source = readFileSync(join(scriptsDir, name), 'utf8');
    assert.doesNotMatch(source, /--force-with-lease|--mirror|--delete|push[^\n]*--force|pushRef\([^)]*['"`]\+/, name);
  }
  // A real non-fast-forward push is reported as rejected and leaves the remote alone.
  const other = cloneOf(s.sb, s.remote, 'other');
  const published = s.sb.commit(other, 'docs: ahead', { 'docs/design/ahead.md': '# Ahead\n' });
  s.sb.g(other, 'push', '-q', 'origin', 'dev');
  s.sb.g(s.work, 'checkout', '-q', '--detach', 'dev');
  const behind = s.sb.commit(s.work, 'docs: behind', { 'docs/design/behind.md': '# Behind\n' });
  const pushed = pushRef(s.work, 'origin', `${behind}:refs/heads/dev`);
  assert.equal(pushed.ok, false);
  assert.equal(pushed.rejected, true);
  assert.equal(remoteDev(s), published);
});

test('the command line ignores GIT_* variables of its caller', (t) => {
  const s = setup(t);
  s.branch('feature', { 'docs/design/guide.md': '# Guide\n\nMore.\n' });
  const decoy = join(s.sb.dir, 'decoy');
  s.sb.g(s.sb.dir, 'init', '-q', decoy);
  s.sb.commit(decoy, 'decoy', { 'README.md': '# Decoy\n' });
  const before = s.sb.g(decoy, 'for-each-ref');
  const result = run(process.execPath, [join(scriptsDir, 'integrate.mjs'), 'feature'], {
    cwd: s.work,
    env: { ...s.sb.env, GIT_DIR: join(decoy, '.git'), GIT_WORK_TREE: decoy, GIT_INDEX_FILE: join(decoy, '.git/index') },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).outcome, 'integrated');
  assert.equal(s.sb.g(s.work, 'rev-parse', 'dev'), remoteDev(s));
  assert.equal(s.sb.g(decoy, 'for-each-ref'), before);
  assert.equal(git(s.work, ['rev-parse', '--git-dir'], { env: s.sb.env }).stdout.trim(), '.git');
});
