// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

test('post-merge QA serializes reporters without replacing the pending reporter', () => {
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const concurrency = workflow.split('  post-merge-qa:')[1].match(/    concurrency:\n([\s\S]*?)    steps:/)[1];
  assert.match(concurrency, /^      group: post-merge-qa-\$\{\{ github.repository \}\}-\$\{\{ github.ref \}\}$/m);
  assert.match(concurrency, /^      cancel-in-progress: false$/m);
  assert.match(concurrency, /^      queue: max$/m);
});

test('the CI sandbox profile compiles without loading it into the host kernel', { skip: process.platform !== 'linux' }, () => {
  const result = spawnSync('apparmor_parser', ['--skip-kernel-load', '--skip-cache',
    fileURLToPath(new URL('./bwrap.apparmor', import.meta.url))], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr ?? result.error?.message);
});
