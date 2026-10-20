#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Check Go configuration, exact module pins and module sums. All Go calls are injectable.
import { existsSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { isAbsolute, join, relative, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';
import { execute } from './check-components.mjs';

const manifests = ['go.mod', 'kit/go.mod', 'go.work'];

function exactVersion(version) {
  if (typeof version !== 'string') return false;
  const match = version.match(/^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+incompatible)?$/);
  return Boolean(match) && !(match[1] ?? '').split('.').some(part => /^0\d+$/.test(part));
}

function localModule(root, directory, path) {
  if (typeof path !== 'string' || !(isAbsolute(path) || /^\.{1,2}(?:[\\/]|$)/.test(path))) return false;
  try {
    const target = realpathSync(resolve(directory, path));
    const name = relative(root, target);
    if (isAbsolute(name) || name === '..' || name.startsWith(`..${sep}`)) return false;
    return (name === '' || name === 'kit' || name.startsWith(`third_party${sep}`))
      && statSync(target).isDirectory() && existsSync(join(target, 'go.mod'));
  } catch {
    return false;
  }
}

function checkPins(data, file, root) {
  if (!data || typeof data !== 'object' || !data.Go || !data.Toolchain
      || (data.Require != null && !Array.isArray(data.Require))
      || (data.Replace != null && !Array.isArray(data.Replace))) {
    throw new Error(`${file}: malformed Go manifest response`);
  }
  for (const entry of data.Require ?? []) {
    if (!entry?.Path || !exactVersion(entry.Version)) {
      throw new Error(`${file}: require ${entry?.Path ?? ''} must name an exact version`);
    }
  }
  for (const entry of data.Replace ?? []) {
    const { Old, New } = entry ?? {};
    if (!Old?.Path || (Old.Version && !exactVersion(Old.Version))) {
      throw new Error(`${file}: replace selector must name an exact version when versioned`);
    }
    const directory = resolve(root, file, '..');
    if (!New?.Path || (New.Version ? !exactVersion(New.Version) : !localModule(root, directory, New.Path))) {
      throw new Error(`${file}: replace ${Old.Path} must name an exact version or an in-repository module directory`);
    }
  }
}

export function checkDependencies({ cwd = process.cwd(), execute: run = execute } = {}) {
  let output = '';
  let temporary;
  const invoke = (args, directory, env) => {
    const result = run('go', args, { cwd: directory, env });
    output += `$ go ${args.join(' ')}\n${result.stdout}${result.stderr}`;
    if (result.status !== 0) throw new Error(`go ${args.join(' ')} failed in ${relative(cwd, directory) || '.'}`);
    return result.stdout;
  };
  try {
    cwd = realpathSync(cwd);
    // Reject drift before Go can select or download a different toolchain.
    const versions = manifests.map(file => {
      const source = readFileSync(join(cwd, file), 'utf8');
      return { go: source.match(/^go\s+(\S+)/m)?.[1], toolchain: source.match(/^toolchain\s+(\S+)/m)?.[1] };
    });
    const { go, toolchain } = versions[0];
    if (!/^1\.\d+(?:\.\d+)?$/.test(go ?? '') || !/^go1\.\d+\.\d+$/.test(toolchain ?? '')
        || versions.some(value => value.go !== go || value.toolchain !== toolchain)) {
      throw new Error('go.mod, kit/go.mod and go.work must name the same Go and toolchain releases');
    }
    // Keep the public Go proxy/checksum defaults and share only the Go download/build caches.
    const inherited = Object.fromEntries(['PATH', 'SystemRoot', 'SYSTEMROOT', 'WINDIR', 'TMPDIR', 'TMP', 'TEMP', 'GOCACHE', 'GOMODCACHE']
      .filter(name => process.env[name] !== undefined).map(name => [name, process.env[name]]));
    const [gocache, gomodcache] = invoke(['env', 'GOCACHE', 'GOMODCACHE'], cwd,
      { ...inherited, HOME: homedir(), GOENV: 'off', GOTOOLCHAIN: 'local' }).trim().split(/\r?\n/);
    if (!gocache || !gomodcache) throw new Error('Go did not return its download/build cache paths');
    temporary = mkdtempSync(join(tmpdir(), 'lina-dependencies-'));
    const env = {
      ...inherited, HOME: temporary, USERPROFILE: temporary, XDG_CONFIG_HOME: temporary,
      GOENV: 'off', GOTOOLCHAIN: toolchain, GOWORK: join(cwd, 'go.work'), GOFLAGS: '-mod=readonly',
      GOCACHE: gocache, GOMODCACHE: gomodcache, CGO_ENABLED: '0',
      GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0',
    };
    if (invoke(['env', 'GOVERSION'], cwd, env).trim() !== toolchain) throw new Error(`Expected toolchain ${toolchain}`);
    // Go's parser handles blocks, comments and quoted paths; edit -json only reads the manifest.
    for (const file of manifests) {
      const directory = resolve(cwd, file, '..');
      const data = JSON.parse(invoke([file === 'go.work' ? 'work' : 'mod', 'edit', '-json'], directory, env));
      checkPins(data, file, cwd);
    }
    for (const directory of [cwd, join(cwd, 'kit')]) invoke(['mod', 'verify'], directory, env);
    return { status: 0, output, toolchain };
  } catch (error) {
    return { status: 1, output: `${output}dependencies: ${error.message}\n` };
  } finally {
    if (temporary) rmSync(temporary, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const result = checkDependencies();
  process.stdout.write(result.output);
  process.exitCode = result.status;
}
