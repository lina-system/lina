// SPDX-License-Identifier: Apache-2.0
// Verify external Node programs in disposable, credential-free installations.
import { cpSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, join } from 'node:path';
import { execute } from './check-components.mjs';

const npmrc = 'registry=https://registry.npmjs.org/\nsave-exact=true\nignore-scripts=true\n';
const exact = v => typeof v === 'string' && /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[A-Za-z0-9.-]+)?$/.test(v);
export function validateNpm(manifest, lock, exceptions = []) {
  for (const field of ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies']) {
    for (const [name, version] of Object.entries(manifest[field] ?? {})) {
      if (!exact(version)) throw new Error(`${name}: ${field} needs an exact version`);
    }
  }
  function overrides(value) {
    for (const pin of Object.values(value)) {
      if (pin && typeof pin === 'object') overrides(pin);
      else if (!exact(pin)) throw new Error('overrides need exact versions');
    }
  }
  overrides(manifest.overrides ?? {});
  if (Object.keys(manifest.scripts ?? {}).some(name => /^(pre|post)/.test(name) || name === 'install')) throw new Error('lifecycle hooks are forbidden');
  if (lock.lockfileVersion !== 3 || !lock.packages?.['']) throw new Error('expected npm v3 lockfile');
  for (const [path, entry] of Object.entries(lock.packages)) {
    if (!path) continue;
    const name = path.split('node_modules/').at(-1);
    if (!path.startsWith('node_modules/') || entry.link || !exact(entry.version)) throw new Error(`invalid lock entry: ${path}`);
    const source = new URL(entry.resolved);
    if (source.origin !== 'https://registry.npmjs.org' || source.username || source.password || source.hash || source.search
        || !/^sha512-[A-Za-z0-9+/]{86}==$/.test(entry.integrity ?? '')) throw new Error(`untrusted lock source or integrity: ${path}`);
    if (entry.hasInstallScript && !exceptions.some(e => e.kind === 'install-script' && e.name === name && e.version === entry.version)) throw new Error(`install script: ${name}@${entry.version}`);
  }
}
export function checkNpm({ cwd = process.cwd(), execute: run = execute, exceptions = [] } = {}) {
  let temporary, output = '';
  try {
    const directories = ['.', 'adapters/claude', 'adapters/claude-acp'].filter(dir => existsSync(join(cwd, dir, 'package.json')) || existsSync(join(cwd, dir, 'package-lock.json')));
    if (!directories.length) return { status: 0, output: '' };
    if (readFileSync(join(cwd, '.npmrc'), 'utf8') !== npmrc) throw new Error('root .npmrc must use the public registry, exact pins and ignore-scripts');
    const nodeVersion = readFileSync(join(cwd, '.node-version'), 'utf8').trim();
    if (!exact(nodeVersion) || process.versions.node !== nodeVersion) throw new Error(`expected Node ${nodeVersion}`);
    for (const dir of directories) {
      validateNpm(JSON.parse(readFileSync(join(cwd, dir, 'package.json'), 'utf8')), JSON.parse(readFileSync(join(cwd, dir, 'package-lock.json'), 'utf8')), exceptions);
      if (existsSync(join(cwd, dir, '.npmrc')) && readFileSync(join(cwd, dir, '.npmrc'), 'utf8') !== npmrc) throw new Error(`${dir}/.npmrc differs from root`);
    }
    temporary = mkdtempSync(join(tmpdir(), 'lina-npm-'));
    const env = Object.fromEntries(['PATH', 'SystemRoot', 'SYSTEMROOT', 'WINDIR', 'TMPDIR', 'TMP', 'TEMP'].filter(k => process.env[k] !== undefined).map(k => [k, process.env[k]]));
    Object.assign(env, { HOME: temporary, USERPROFILE: temporary, XDG_CONFIG_HOME: temporary });
    for (const [index, dir] of directories.entries()) {
      const target = join(temporary, `package-${index}`);
      cpSync(join(cwd, dir), target, { recursive: true, filter: file => !['node_modules', '.git', '.env'].includes(basename(file)) });
      writeFileSync(join(target, '.npmrc'), npmrc);
      const commands = dir === 'adapters/claude-acp'
        ? [['ci', '--ignore-scripts', '--omit=dev', '--omit=optional'], ['audit', 'signatures']]
        : [['ci', '--ignore-scripts'], ['audit', 'signatures'], ['test']];
      for (const args of commands) {
        const result = run('npm', args, { cwd: target, env, timeoutMs: 180_000 });
        output += `$ npm ${args.join(' ')} (${dir})\n${result.stdout}${result.stderr}`;
        if (result.status !== 0) throw new Error(`npm ${args.join(' ')} failed`);
      }
    }
    return { status: 0, output };
  } catch (error) {
    return { status: 1, output: `${output}npm dependencies: ${error.message}\n` };
  } finally {
    if (temporary) rmSync(temporary, { recursive: true, force: true });
  }
}
