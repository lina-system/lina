// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const installed = existsSync(new URL('./node_modules/agent-messenger/package.json', import.meta.url));
test('the pinned SDK imports without optional packages or lifecycle scripts', { skip: !installed }, async () => {
  for (const platform of ['discord', 'kakaotalk', 'whatsapp', 'line']) {
    const sdk = await import('agent-messenger/' + platform);
    assert.ok(Object.keys(sdk).length > 0);
  }
  for (const dependency of ['koffi', 'tdl', 'prebuilt-tdlib']) {
    assert.equal(existsSync(new URL('./node_modules/' + dependency, import.meta.url)), false, dependency);
  }
});

test('the bundled LevelDB prebuild works without an install script', { skip: !installed }, async () => {
  const { ClassicLevel } = await import('classic-level');
  const dir = mkdtempSync(join(tmpdir(), 'messenger-level-'));
  const db = new ClassicLevel(join(dir, 'db'));
  try {
    await db.put('synthetic', 'fixture');
    assert.equal(await db.get('synthetic'), 'fixture');
  } finally {
    await db.close();
    rmSync(dir, { recursive: true, force: true });
  }
});
