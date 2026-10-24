// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { join } from 'node:path';
import { sandbox } from '../test-fixture.mjs';

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

test('push skips QA and PR keeps its successful scope-only job without reporting', t => {
  const sb = sandbox(t);
  const workflow = readFileSync(new URL('../../.github/workflows/ci.yml', import.meta.url), 'utf8');
  const reporter = workflow.split('  post-merge-qa:')[1];
  // Evaluate the job guard for each event rather than pinning documentation.
  const guard = reporter.match(/^    if: (.+)$/m)[1];
  for (const event of ['pull_request', 'push', 'schedule', 'workflow_dispatch']) {
    const expression = guard.replaceAll('always()', 'true').replaceAll('github.event_name', JSON.stringify(event));
    assert.equal(Function('return (' + expression + ')')(), event !== 'push', event);
    const script = reporter.split('        run: |\n')[1].split('      - name:')[0]
      .split('\n').map(line => line.slice(10)).join('\n');
    const output = join(sb.dir, event);
    const result = spawnSync('bash', ['-c', script], { encoding: 'utf8', env: {
      PATH: process.env.PATH, EVENT_NAME: event, REF_NAME: 'dev', SELECTION_RESULT: 'success', GITHUB_OUTPUT: output,
    } });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(readFileSync(output, 'utf8').trim(), 'enabled=' + ['schedule', 'workflow_dispatch'].includes(event));
  }
});
