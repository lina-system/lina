// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

test('the CI sandbox profile compiles without loading it into the host kernel', { skip: process.platform !== 'linux' }, () => {
  const result = spawnSync('apparmor_parser', ['--skip-kernel-load', '--skip-cache',
    fileURLToPath(new URL('./bwrap.apparmor', import.meta.url))], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr ?? result.error?.message);
});
