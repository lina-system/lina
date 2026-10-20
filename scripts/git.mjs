// SPDX-License-Identifier: Apache-2.0
// Shared Git helpers for the repository scripts. Every Git child process runs with the GIT_*
// variables of the caller removed, so a hook, a worktree wrapper, or a test that exports GIT_DIR
// never redirects a script to another repository.
import { spawn, spawnSync } from 'node:child_process';

const credentialVariables = ['GH_TOKEN', 'GITHUB_TOKEN', 'GH_ENTERPRISE_TOKEN', 'GITHUB_ENTERPRISE_TOKEN', 'RELEASE_TOKEN'];

/**
 * A copy of the environment without GIT_* variables and without the context of an enclosing
 * `node --test` run, which would turn a nested test run into a child of the outer one.
 */
export function cleanEnv(env = process.env) {
  return Object.fromEntries(Object.entries(env)
    .filter(([name]) => !name.startsWith('GIT_') && name !== 'NODE_TEST_CONTEXT'));
}

/** A copy of the environment without GIT_* variables and without account credentials. */
export function credentialFreeEnv(env = process.env) {
  const clean = cleanEnv(env);
  for (const name of credentialVariables) delete clean[name];
  return clean;
}

/** Removes the GIT_* variables from this process. Scripts call it first. */
export function stripGitEnv() {
  for (const name of Object.keys(process.env)) if (name.startsWith('GIT_')) delete process.env[name];
}

/** Runs a command and returns { status, stdout, stderr }; it never throws on a non-zero status. */
export function run(command, args, { cwd, env, input } = {}) {
  const result = spawnSync(command, args, {
    cwd, input, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024,
    env: { ...cleanEnv(), ...env },
  });
  if (result.error) throw result.error;
  return { status: result.status ?? 1, stdout: result.stdout, stderr: result.stderr };
}

/**
 * Runs a command asynchronously and resolves { status, output } with stdout and stderr combined.
 * A command that runs longer than `timeoutMs` is killed and fails.
 */
export function runAsync(command, args, { cwd, env, timeoutMs } = {}) {
  return new Promise((resolve) => {
    const child = spawn(command, args, { cwd, env: { ...cleanEnv(), ...env }, stdio: ['ignore', 'pipe', 'pipe'] });
    let output = '';
    let timedOut = false;
    const timer = timeoutMs ? setTimeout(() => { timedOut = true; child.kill('SIGKILL'); }, timeoutMs) : null;
    child.stdout.on('data', (chunk) => { output += chunk; });
    child.stderr.on('data', (chunk) => { output += chunk; });
    child.on('error', (error) => {
      clearTimeout(timer);
      resolve({ status: 1, output: `${output}${error.message}\n` });
    });
    child.on('close', (status) => {
      clearTimeout(timer);
      resolve(timedOut
        ? { status: 1, output: `${output}timed out after ${timeoutMs / 1000} s\n` }
        : { status: status ?? 1, output });
    });
  });
}

/** Runs git and returns the result; a non-zero status is the caller's to inspect. */
export function git(cwd, args, options = {}) {
  return run('git', args, { cwd, ...options });
}

/** Runs git and returns trimmed stdout; a non-zero status throws with git's message. */
export function gitOut(cwd, args, options = {}) {
  const result = git(cwd, args, options);
  if (result.status !== 0) {
    throw new Error(`git ${args.join(' ')} failed (${result.status}): ${(result.stderr || result.stdout).trim()}`);
  }
  return result.stdout.trim();
}

/** True when `ancestor` is reachable from `descendant` (a commit is its own ancestor). */
export function isAncestor(cwd, ancestor, descendant) {
  return git(cwd, ['merge-base', '--is-ancestor', ancestor, descendant]).status === 0;
}

/** Resolves a revision to a commit id, or null when it does not name a commit. */
export function resolveCommit(cwd, revision) {
  const result = git(cwd, ['rev-parse', '--verify', '--quiet', '--end-of-options', `${revision}^{commit}`]);
  return result.status === 0 ? result.stdout.trim() : null;
}

/** The author and committer identity for commands that create commits when none is configured. */
export function identityEnv(cwd) {
  const name = git(cwd, ['config', 'user.name']).stdout.trim();
  const email = git(cwd, ['config', 'user.email']).stdout.trim();
  if (name && email) return {};
  const fallbackName = name || 'LINA scripts';
  const fallbackEmail = email || 'scripts@lina.invalid';
  return {
    GIT_AUTHOR_NAME: fallbackName, GIT_AUTHOR_EMAIL: fallbackEmail,
    GIT_COMMITTER_NAME: fallbackName, GIT_COMMITTER_EMAIL: fallbackEmail,
  };
}

/** The absolute top level of the work tree that contains `cwd`. */
export function toplevel(cwd) {
  return gitOut(cwd, ['rev-parse', '--show-toplevel']);
}

/** The absolute Git common directory, shared by every work tree of the repository. */
export function commonDir(cwd) {
  return gitOut(cwd, ['rev-parse', '--path-format=absolute', '--git-common-dir']);
}

/**
 * Pushes without ever rewriting remote history. A refspec that forces (a leading `+`) is refused,
 * and the command never passes a force option. Returns { ok, rejected, stderr }.
 */
export function pushRef(cwd, remote, refspec) {
  if (refspec.startsWith('+')) throw new Error(`Refusing to force-push: ${refspec}`);
  const result = git(cwd, ['push', '--porcelain', remote, refspec]);
  const text = `${result.stdout}\n${result.stderr}`;
  const rejected = result.status !== 0 && /\[rejected\]|non-fast-forward|fetch first|stale info/.test(text);
  return { ok: result.status === 0, rejected, stderr: text.trim() };
}
