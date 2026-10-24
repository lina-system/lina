// SPDX-License-Identifier: Apache-2.0
// The worker evaluation records one seed commit. setup.sh must reproduce it
// from the published fixture, including when the caller has its own Git identity.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { cleanEnv } from './git.mjs';

const root = fileURLToPath(new URL('..', import.meta.url));
const setup = join(root, 'testdata/worker-eval/setup.sh');

function recordedSeed() {
  const record = readFileSync(join(root, 'evidence/worker-evaluation.md'), 'utf8');
  const pinned = /seed commit `([0-9a-f]{40})`/.exec(record);
  assert.ok(pinned, 'evidence/worker-evaluation.md records a seed commit');
  return pinned[1];
}

function seed(dir, env) {
  const result = spawnSync(setup, ['seed', dir], { encoding: 'utf8', env });
  assert.equal(result.status, 0, result.stderr);
  return result.stdout.trim();
}

test('published fixture reproduces the recorded worker-evaluation seed', () => {
  const pinned = recordedSeed();
  const dir = mkdtempSync(join(tmpdir(), 'worker-eval-seed-'));
  const other = mkdtempSync(join(tmpdir(), 'worker-eval-seed-'));
  const runParent = mkdtempSync(join(tmpdir(), 'worker-eval-run-'));
  const worktree = join(runParent, 'worktree');
  try {
    assert.equal(seed(dir, cleanEnv()), pinned);
    const polluted = cleanEnv();
    polluted.GIT_AUTHOR_NAME = 'Someone Else';
    polluted.GIT_AUTHOR_EMAIL = 'someone@example.invalid';
    polluted.GIT_COMMITTER_NAME = 'Someone Else';
    polluted.GIT_COMMITTER_EMAIL = 'someone@example.invalid';
    assert.equal(seed(other, polluted), pinned);
    const run = spawnSync(setup, ['run', dir, worktree, 'eval-run'], { encoding: 'utf8', env: cleanEnv() });
    assert.equal(run.status, 0, run.stderr);
    const head = spawnSync('git', ['-C', worktree, 'rev-parse', 'HEAD'], { encoding: 'utf8', env: cleanEnv() });
    assert.equal(head.status, 0, head.stderr);
    assert.equal(head.stdout.trim(), pinned);
  } finally {
    rmSync(runParent, { recursive: true, force: true });
    rmSync(dir, { recursive: true, force: true });
    rmSync(other, { recursive: true, force: true });
  }
});
