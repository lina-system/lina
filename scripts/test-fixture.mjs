// SPDX-License-Identifier: Apache-2.0
// Helpers for the script tests. Every test runs against repositories in a temporary directory,
// with the GIT_* variables of the test process removed and with no access to the real user or
// system Git configuration.
import assert from 'node:assert/strict';
import { cpSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { git } from './git.mjs';

export const scriptsDir = dirname(fileURLToPath(import.meta.url));

// The scripts a fixture repository needs to run foundation. The tests themselves stay out, so the
// fixture's automation check does not run the suite again.
const foundationFiles = [
  'git.mjs', 'foundation.mjs',
  'verify/scope.mjs', 'verify/gate.mjs', 'verify/check-docs.mjs',
];

/** A temporary directory with an isolated Git environment, removed when the test ends. */
export function sandbox(t) {
  const dir = mkdtempSync(join(tmpdir(), 'lina-script-test-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const env = {
    HOME: dir, XDG_CONFIG_HOME: dir,
    GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0',
  };
  const identity = {
    GIT_AUTHOR_NAME: 'Fixture', GIT_AUTHOR_EMAIL: 'fixture@example.invalid',
    GIT_COMMITTER_NAME: 'Fixture', GIT_COMMITTER_EMAIL: 'fixture@example.invalid',
  };
  /** Runs git in the fixture setup and asserts success. Setup commits carry a fixture identity. */
  const g = (cwd, ...args) => {
    const result = git(cwd, args, { env: { ...env, ...identity } });
    assert.equal(result.status, 0, `git ${args.join(' ')}: ${result.stderr}`);
    return result.stdout.trim();
  };
  const write = (repo, name, content) => {
    mkdirSync(dirname(join(repo, name)), { recursive: true });
    writeFileSync(join(repo, name), content);
  };
  const commit = (repo, message, files = {}) => {
    for (const [name, content] of Object.entries(files)) write(repo, name, content);
    g(repo, 'add', '-A');
    g(repo, 'commit', '-q', '-m', message);
    return g(repo, 'rev-parse', 'HEAD');
  };
  return { dir, env, identity, g, write, commit };
}

/** Copies the foundation scripts into a fixture repository work tree. */
export function installScripts(repo, extra = []) {
  for (const name of [...foundationFiles, ...extra]) {
    mkdirSync(dirname(join(repo, 'scripts', name)), { recursive: true });
    cpSync(join(scriptsDir, name), join(repo, 'scripts', name));
  }
}

/**
 * A fixture with a bare remote and a first clone on `dev` that holds a README and the foundation
 * scripts, pushed to the remote. Returns the remote path and the clone path.
 */
export function developmentRepository(sb, name = 'work') {
  const remote = join(sb.dir, 'remote.git');
  sb.g(sb.dir, 'init', '-q', '--bare', '--initial-branch=dev', remote);
  const work = join(sb.dir, name);
  sb.g(sb.dir, 'clone', '-q', remote, work);
  sb.g(work, 'checkout', '-q', '-B', 'dev');
  installScripts(work);
  sb.commit(work, 'chore: start', { 'README.md': '# Fixture\n\n[guide](docs/design/guide.md)\n', 'docs/design/guide.md': '# Guide\n' });
  sb.g(work, 'push', '-q', 'origin', 'dev');
  return { remote, work };
}

/** A second clone of the same remote, for a competing integrator. */
export function cloneOf(sb, remote, name) {
  const dir = join(sb.dir, name);
  sb.g(sb.dir, 'clone', '-q', remote, dir);
  sb.g(dir, 'checkout', '-q', '-B', 'dev', 'origin/dev');
  return dir;
}
