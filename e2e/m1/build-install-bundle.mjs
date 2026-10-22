// SPDX-License-Identifier: Apache-2.0
// Build the current sources, including uncommitted fixes, with the real writer.
import { cpSync, existsSync, mkdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from '../../scripts/build.mjs';
import { gitOut } from '../../scripts/git.mjs';
import { sandbox } from '../../scripts/test-fixture.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const cleanup = [];
const sb = sandbox({ after: callback => cleanup.push(callback) });
try {
  const source = join(sb.dir, 'source');
  mkdirSync(source);
  // A private commit lets the build's normal clean-source check run even during
  // a red/green iteration. Never synthesize or patch the resulting manifest.
  for (const name of gitOut(root, ['ls-files', '-z', '--cached', '--others', '--exclude-standard']).split('\0').filter(Boolean)) {
    if (!existsSync(join(root, name))) continue;
    mkdirSync(dirname(join(source, name)), { recursive: true });
    cpSync(join(root, name), join(source, name), { recursive: true });
  }
  sb.g(source, 'init', '-q', '--initial-branch=dev');
  sb.commit(source, 'test: snapshot install sources');
  if (!build({ cwd: source, out: process.argv[2] })) throw new Error('Build did not produce a manifest');
} finally {
  for (const close of cleanup.reverse()) close();
}
