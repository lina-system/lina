#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Verify registered Go packages using the workspace's exact toolchain and disposable outputs.
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

export function execute(command, args, { cwd, env }) {
  // The selected root suite includes many real command/Core subprocesses.
  // Give Go time to report its own test timeout before the process watchdog.
  const timeout = command === 'go' && args[0] === 'test' ? 300_000 : 120_000;
  const result = spawnSync(command, args, { cwd, env, encoding: 'utf8', timeout, maxBuffer: 64 * 1024 * 1024 });
  return { status: result.status ?? 1, stdout: result.stdout ?? '', stderr: result.error ? `${result.stderr ?? ''}${result.error.message}\n` : result.stderr ?? '' };
}

// No account credentials or user Go/Git configuration reach package loading, builds or tests.
// Downloads retain Go's public proxy/checksum defaults; only verified download caches are shared.
export function createGoContext(cwd) {
  const versions = ['go.mod', 'kit/go.mod', 'go.work'].map(name => {
    const source = readFileSync(join(cwd, name), 'utf8');
    return { go: source.match(/^go\s+(\S+)/m)?.[1], toolchain: source.match(/^toolchain\s+(\S+)/m)?.[1] };
  });
  const { go, toolchain } = versions[0];
  if (!/^1\.\d+(?:\.\d+)?$/.test(go ?? '') || !/^go1\.\d+\.\d+$/.test(toolchain ?? '')
      || versions.some(value => value.go !== go || value.toolchain !== toolchain)) {
    throw new Error('go.mod, kit/go.mod and go.work must name the same Go and toolchain releases');
  }
  const inherited = Object.fromEntries(['PATH', 'SystemRoot', 'SYSTEMROOT', 'WINDIR', 'TMPDIR', 'TMP', 'TEMP', 'GOCACHE', 'GOMODCACHE']
    .filter(name => process.env[name] !== undefined).map(name => [name, process.env[name]]));
  const cache = execute('go', ['env', 'GOCACHE', 'GOMODCACHE'], {
    cwd, env: { ...inherited, HOME: homedir(), GOENV: 'off', GOTOOLCHAIN: 'local' },
  });
  if (cache.status !== 0) throw new Error(`Cannot locate Go caches: ${cache.stderr}`);
  const [gocache, gomodcache] = cache.stdout.trim().split(/\r?\n/);
  if (!gocache || !gomodcache) throw new Error('Go did not return its download/build cache paths');
  const directory = mkdtempSync(join(tmpdir(), 'lina-components-'));
  return {
    directory, toolchain,
    env: {
      ...inherited, HOME: directory, USERPROFILE: directory, XDG_CONFIG_HOME: directory,
      GOENV: 'off', GOTOOLCHAIN: toolchain, GOWORK: join(cwd, 'go.work'), GOFLAGS: '-mod=readonly',
      GOCACHE: gocache, GOMODCACHE: gomodcache, CGO_ENABLED: '0',
      GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0',
    },
    close: () => rmSync(directory, { recursive: true, force: true }),
  };
}

export function checkComponents({ cwd = process.cwd(), components, execute: run = execute }) {
  if (!Array.isArray(components) || components.length === 0) throw new Error('Select at least one registered component');
  const context = createGoContext(cwd);
  let output = '';
  const invoke = (command, args, directory) => {
    const result = run(command, args, { cwd: directory, env: context.env });
    output += `$ ${command} ${args.join(' ')}\n${result.stdout}${result.stderr}`;
    return result;
  };
  try {
    const version = invoke('go', ['env', 'GOVERSION'], cwd);
    if (version.status !== 0 || version.stdout.trim() !== context.toolchain) {
      return { status: 1, output: `${output}Expected toolchain ${context.toolchain}\n` };
    }
    // The root also supplies the analyzer when only kit packages are selected.
    for (const module of new Set(['.', ...components.map(component => component.module)])) {
      if (invoke('go', ['mod', 'verify'], join(cwd, module)).status !== 0) return { status: 1, output };
    }
    // Compare regenerated types without modifying the candidate. Minimal older
    // trees and verification fixtures may not contain the protocol package yet.
    const protocol = join(cwd, 'kit/protocol');
    if (components.some(component => component.name === 'kit') && existsSync(protocol)) {
      if (invoke('go', ['run', './internal/generate', '-check'], protocol).status !== 0) return { status: 1, output };
    }
    // Token checks also run for component-only TUI changes, when automation
    // is not selected. The checker compares outputs without modifying them.
    if (components.some(component => component.name === 'lina') && existsSync(join(cwd, 'design/tokens'))) {
      if (invoke(process.execPath, [join(cwd, 'scripts/tokens.mjs'), '--check'], cwd).status !== 0) return { status: 1, output };
    }
    for (const module of new Set(components.map(component => component.module))) {
      const selected = components.filter(component => component.module === module);
      const packages = [...new Set(selected.flatMap(component => component.packages))];
      const directory = join(cwd, module);
      const binaries = join(context.directory, module === '.' ? 'root-bin' : 'kit-bin');
      mkdirSync(binaries);
      const outputArgs = selected.some(component => component.binary) ? ['-o', `${binaries}/`] : [];
      for (const args of [
        ['build', '-mod=readonly', ...outputArgs, ...packages],
        ['vet', '-mod=readonly', ...packages],
        ['tool', 'exhaustive', ...packages],
        ['test', '-timeout=4m', '-count=1', '-mod=readonly', ...packages],
      ]) {
        if (invoke('go', args, directory).status !== 0) return { status: 1, output };
      }
      for (const component of selected.filter(component => component.binary)) {
        const result = invoke(join(binaries, component.binary), [], directory);
        if (result.status !== 64 || !/usage:/i.test(result.stderr) || result.stdout !== '') {
          return { status: 1, output: `${output}${component.name}: built command failed its usage smoke check\n` };
        }
      }
    }
    return { status: 0, output, toolchain: context.toolchain };
  } finally {
    context.close();
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  import('./scope.mjs').then(({ componentRegistry }) => {
    const names = process.argv.slice(2);
    if (names.some(name => !componentRegistry.some(component => component.name === name))) throw new Error('Unknown Go component');
    const result = checkComponents({ cwd: resolve('.'), components: componentRegistry.filter(component => names.includes(component.name)) });
    process.stdout.write(result.output);
    process.exitCode = result.status;
  }).catch(error => {
    console.error(`components: ${error.message}`);
    process.exitCode = 1;
  });
}
