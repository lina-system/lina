#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Integrates a branch into dev. This is the only way a change enters dev.
//
//   node scripts/integrate.mjs <branch> [--message-file <file>] [--remote <name>]
//                              [--wait <seconds>] [--attempts <n>]
//
// One integration runs at a time (a lock in the Git common directory). In a clean work tree the
// script creates the merge commit of the branch into the current dev head, runs the foundation
// of that merge commit, and only when it passes advances dev and pushes it. A failed foundation
// leaves dev untouched. When the push is rejected because dev moved, the script merges the branch
// into the new dev head and runs foundation again. It never force-pushes.
//
// The merge commit message carries the change record: the problem and approach, the checks and
// their gaps, and compatibility and recovery.
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { hostname, tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import {
  commonDir, git, gitOut, identityEnv, isAncestor, pushRef, resolveCommit, run, stripGitEnv, toplevel,
} from './git.mjs';

stripGitEnv();

const notesRef = 'refs/notes/foundation';
const remoteNotesRef = 'refs/notes/remote/foundation';
const zero = '0'.repeat(40);

const sleep = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

function processAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error.code === 'EPERM';
  }
}

/** Takes the integration lock of the repository, waiting up to `waitSeconds`; returns a release function. */
export async function acquireLock(cwd, { waitSeconds = 300, log = () => {} } = {}) {
  const lock = join(commonDir(cwd), 'lina-integrate.lock');
  const deadline = Date.now() + waitSeconds * 1000;
  for (;;) {
    try {
      mkdirSync(lock);
      writeFileSync(join(lock, 'owner.json'), JSON.stringify({ pid: process.pid, host: hostname(), started: new Date().toISOString() }));
      break;
    } catch (error) {
      if (error.code !== 'EEXIST') throw error;
    }
    let owner = null;
    try {
      owner = JSON.parse(readFileSync(join(lock, 'owner.json'), 'utf8'));
    } catch {
      // An owner file that is missing or unreadable belongs to a lock that is being taken or was abandoned.
    }
    if (owner && owner.host === hostname() && !processAlive(owner.pid)) {
      log(`Removing the integration lock of the finished process ${owner.pid}.`);
      rmSync(lock, { recursive: true, force: true });
      continue;
    }
    if (Date.now() >= deadline) {
      throw new Error(`Another integration holds the lock (${owner ? `pid ${owner.pid} since ${owner.started}` : 'owner unknown'}).`);
    }
    await sleep(500);
  }
  let released = false;
  const release = () => {
    if (released) return;
    released = true;
    rmSync(lock, { recursive: true, force: true });
  };
  return release;
}

function remoteExists(root, remote) {
  return git(root, ['remote', 'get-url', remote]).status === 0;
}

// Fetches the remote dev into its tracking ref. Returns the commit, or null when the remote has no dev.
function fetchDev(root, remote) {
  const listed = git(root, ['ls-remote', '--exit-code', '--heads', remote, 'refs/heads/dev']);
  if (listed.status === 2) return null;
  if (listed.status !== 0) throw new Error(`Cannot reach ${remote}: ${listed.stderr.trim()}`);
  gitOut(root, ['fetch', '--no-tags', remote, `+refs/heads/dev:refs/remotes/${remote}/dev`]);
  return resolveCommit(root, `refs/remotes/${remote}/dev`);
}

// The dev head to build on: the local dev when it contains the remote dev (earlier verified
// commits that are not pushed yet), the remote dev when the local one is behind.
function devHead(root, localDev, remoteDev) {
  if (localDev && remoteDev) {
    if (isAncestor(root, remoteDev, localDev)) return localDev;
    if (isAncestor(root, localDev, remoteDev)) return remoteDev;
    throw new Error('The local dev and the remote dev have diverged. Resolve this before integrating.');
  }
  const head = localDev ?? remoteDev;
  if (!head) throw new Error('There is no dev branch to integrate into.');
  return head;
}

// The work tree in which dev is checked out, if any.
function devCheckout(root) {
  const entries = gitOut(root, ['worktree', 'list', '--porcelain']).split('\n\n');
  for (const entry of entries) {
    const lines = entry.split('\n');
    const path = lines.find((line) => line.startsWith('worktree '))?.slice('worktree '.length);
    if (path && lines.includes('branch refs/heads/dev')) return path;
  }
  return null;
}

/** Merges the remote foundation notes into the local ones and pushes them; returns true when pushed. */
export function syncNotes(root, remote, { log = () => {}, attempts = 3 } = {}) {
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const listed = git(root, ['ls-remote', '--exit-code', remote, notesRef]);
    if (listed.status === 0) {
      gitOut(root, ['fetch', '--no-tags', remote, `+${notesRef}:${remoteNotesRef}`]);
      if (!resolveCommit(root, notesRef)) {
        gitOut(root, ['update-ref', notesRef, resolveCommit(root, remoteNotesRef)]);
      } else {
        gitOut(root, ['notes', '--ref=foundation', 'merge', '-s', 'cat_sort_uniq', remoteNotesRef], { env: identityEnv(root) });
      }
    } else if (listed.status !== 2) {
      throw new Error(`Cannot reach ${remote}: ${listed.stderr.trim()}`);
    }
    if (!resolveCommit(root, notesRef)) return false;
    const pushed = pushRef(root, remote, `${notesRef}:${notesRef}`);
    if (pushed.ok) return true;
    if (!pushed.rejected) throw new Error(`Pushing ${notesRef} failed: ${pushed.stderr}`);
    log(`The remote ${notesRef} moved; merging again (${attempt}/${attempts}).`);
  }
  throw new Error(`Pushing ${notesRef} was rejected ${attempts} times.`);
}

/**
 * Integrates `branch` into dev. Returns { outcome, merge, attempts } where outcome is
 * 'integrated' or 'unchanged' (the branch is already part of dev). Throws when the merge
 * conflicts, foundation fails, or the push fails for a reason other than dev having moved.
 * `hooks.beforePush(attempt)` is called after foundation passes and before the push.
 */
export async function integrate({
  cwd = process.cwd(), branch, message, messageFile, remote = 'origin', waitSeconds = 300, attempts = 5,
  log = console.error, hooks = {},
}) {
  const root = toplevel(cwd);
  const release = await acquireLock(root, { waitSeconds, log });
  const onSignal = () => { release(); process.exit(130); };
  process.once('SIGINT', onSignal);
  process.once('SIGTERM', onSignal);
  try {
    const branchCommit = resolveCommit(root, branch);
    if (!branchCommit) throw new Error(`Unknown branch or commit: ${branch}`);
    if (messageFile) message = readFileSync(messageFile, 'utf8');
    message = message?.trim() ? message : `Merge ${branch} into dev`;
    const hasRemote = remoteExists(root, remote);

    for (let attempt = 1; attempt <= attempts; attempt += 1) {
      const remoteDev = hasRemote ? fetchDev(root, remote) : null;
      const localDev = resolveCommit(root, 'refs/heads/dev');
      const base = devHead(root, localDev, remoteDev);
      if (isAncestor(root, branchCommit, base)) {
        log(`${branch} is already part of dev.`);
        return { outcome: 'unchanged', merge: base, attempts: attempt - 1 };
      }
      const checkout = devCheckout(root);
      if (checkout && git(checkout, ['status', '--porcelain', '--untracked-files=normal']).stdout.trim() !== '') {
        throw new Error(`dev is checked out in ${checkout} with uncommitted changes. Commit or stash them first.`);
      }

      const merge = await mergeAndVerify(root, { base, branchCommit, branch, message, log });
      await hooks.beforePush?.(attempt);

      if (hasRemote) {
        const pushed = pushRef(root, remote, `${merge}:refs/heads/dev`);
        if (pushed.rejected) {
          log(`The push was rejected because dev moved (attempt ${attempt}/${attempts}); merging again.`);
          continue;
        }
        if (!pushed.ok) throw new Error(`Pushing dev failed: ${pushed.stderr}`);
        gitOut(root, ['update-ref', `refs/remotes/${remote}/dev`, merge]);
      }
      advanceLocalDev(root, checkout, merge, localDev);
      if (hasRemote) {
        try {
          syncNotes(root, remote, { log });
        } catch (error) {
          throw new Error(`dev is integrated at ${merge}, but the foundation notes were not pushed: ${error.message}`);
        }
      }
      return { outcome: 'integrated', merge, attempts: attempt };
    }
    throw new Error(`dev moved on every one of ${attempts} attempts; nothing was integrated.`);
  } finally {
    process.removeListener('SIGINT', onSignal);
    process.removeListener('SIGTERM', onSignal);
    release();
  }
}

// Creates the merge commit in a clean temporary work tree and runs the foundation of that commit.
async function mergeAndVerify(root, { base, branchCommit, branch, message, log }) {
  const parent = mkdtempSync(join(tmpdir(), 'lina-integrate-'));
  const worktree = join(parent, 'candidate');
  gitOut(root, ['worktree', 'add', '--quiet', '--detach', worktree, base]);
  try {
    const merged = git(worktree, ['merge', '--no-ff', '--no-edit', '-m', message, branchCommit], { env: identityEnv(root) });
    if (merged.status !== 0) {
      const conflicts = git(worktree, ['diff', '--name-only', '--diff-filter=U']).stdout.trim();
      git(worktree, ['merge', '--abort']);
      throw new Error(`Merging ${branch} into dev conflicts${conflicts ? ` in:\n${conflicts}` : `: ${merged.stderr.trim()}`}\nMerge dev into the branch, resolve it there, and integrate again.`);
    }
    const merge = gitOut(worktree, ['rev-parse', 'HEAD']);
    const foundation = join(worktree, 'scripts/foundation.mjs');
    if (!existsSync(foundation)) throw new Error(`The merge result has no scripts/foundation.mjs, so it cannot be verified.`);
    log(`Running foundation on ${merge.slice(0, 12)} (${base.slice(0, 12)}..${merge.slice(0, 12)}).`);
    const verified = run(process.execPath, [foundation, '--range', `${base}..${merge}`], { cwd: worktree });
    if (verified.stderr) log(verified.stderr.trimEnd());
    if (verified.status !== 0) throw new Error('Foundation failed on the merge result. dev is unchanged.');
    return merge;
  } finally {
    git(root, ['worktree', 'remove', '--force', worktree]);
    rmSync(parent, { recursive: true, force: true });
  }
}

// Moves local dev to the verified merge commit: through the work tree that has dev checked out, so
// that its files follow, or by updating the ref when dev is not checked out.
function advanceLocalDev(root, checkout, merge, previous) {
  if (checkout) {
    gitOut(checkout, ['merge', '--ff-only', '--quiet', merge]);
  } else {
    gitOut(root, ['update-ref', 'refs/heads/dev', merge, previous ?? zero]);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const { values, positionals } = parseArgs({
      allowPositionals: true,
      options: {
        'message-file': { type: 'string' }, remote: { type: 'string', default: 'origin' },
        wait: { type: 'string', default: '300' }, attempts: { type: 'string', default: '5' },
      },
    });
    if (positionals.length !== 1) throw new Error('Usage: node scripts/integrate.mjs <branch> [--message-file <file>] [--remote <name>] [--wait <seconds>] [--attempts <n>]');
    const result = await integrate({
      branch: positionals[0], messageFile: values['message-file'], remote: values.remote,
      waitSeconds: Number(values.wait), attempts: Number(values.attempts),
    });
    console.log(JSON.stringify(result));
  } catch (error) {
    console.error(`integrate: ${error.message}`);
    process.exitCode = 1;
  }
}
