#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Verify registered Go packages using the workspace's exact toolchain and disposable outputs.
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

export const componentsTimeoutMs = 1_200_000;

export function execute(command, args, { cwd, env, timeoutMs = 120_000 }) {
  const started = performance.now();
  const result = spawnSync(command, args, {
    cwd, env, encoding: 'utf8', timeout: timeoutMs, killSignal: 'SIGKILL', maxBuffer: 64 * 1024 * 1024,
  });
  const error = result.error?.code === 'ETIMEDOUT'
    ? `command timed out after ${((performance.now() - started) / 1000).toFixed(3)} s (budget ${timeoutMs / 1000} s): ${command} ${args.join(' ')}`
    : result.error?.message;
  return { status: result.error ? 1 : result.status ?? 1, stdout: result.stdout ?? '', stderr: `${result.stderr ?? ''}${error ? `${error}\n` : ''}` };
}

// No account credentials or user Go/Git configuration reach package loading, builds or tests.
// Downloads retain Go's public proxy/checksum defaults; only verified download caches are shared.
export function createGoContext(cwd, { timeoutMs = 120_000 } = {}) {
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
    cwd, timeoutMs, env: { ...inherited, HOME: homedir(), GOENV: 'off', GOTOOLCHAIN: 'local' },
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

export function checkComponents({ cwd = process.cwd(), components, execute: run = execute,
  // Leave time for diagnostics and cleanup before foundation's outer watchdog.
  timeoutMs = componentsTimeoutMs - 5000 }) {
  if (!Array.isArray(components) || components.length === 0) throw new Error('Select at least one registered component');
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) throw new Error('Components budget must be positive');
  const started = performance.now();
  const deadline = started + timeoutMs;
  const context = createGoContext(cwd, { timeoutMs: Math.min(30_000, timeoutMs) });
  let output = '';
  const invoke = (command, args, directory) => {
    const remaining = Math.floor(deadline - performance.now());
    // Package-wide work grows with the selected module. It can use the remaining
    // check budget; short setup and usage commands retain tighter limits.
    const limit = command === 'go'
      ? ({ env: 30_000, mod: 120_000, run: 120_000 }[args[0]] ?? remaining)
      : 30_000;
    const result = remaining > 0
      ? run(command, args, { cwd: directory, env: context.env, timeoutMs: Math.min(limit, remaining) })
      : { status: 1, stdout: '', stderr: `components budget exhausted after ${((performance.now() - started) / 1000).toFixed(3)} s before: ${command} ${args.join(' ')}\n` };
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
      ]) {
        if (invoke('go', args, directory).status !== 0) return { status: 1, output };
      }
      const testArgs = ['test', '-count=1', '-timeout=6m', '-mod=readonly'];
      const acceptance = module === '.' && packages.includes('./e2e/...');
      const ordinary = acceptance ? packages.filter(name => name !== './e2e/...') : packages;
      if (ordinary.length && invoke('go', [...testArgs, ...ordinary], directory).status !== 0) return { status: 1, output };
      if (acceptance) {
        // The acceptance corpus grows across milestones. Enumerate each
        // package's actual tests, examples and fuzz seeds, then run disjoint
        // halves. No test is omitted or repeated; each half retains Go's
        // diagnostic timeout and consumes the same outer components budget.
        const listed = invoke('go', ['list', '-mod=readonly', './e2e/...'], directory);
        if (listed.status !== 0) return { status: 1, output };
        const acceptancePackages = listed.stdout.trim().split(/\r?\n/).filter(Boolean);
        if (!acceptancePackages.length) return { status: 1, output: `${output}No acceptance packages enumerated\n` };
        for (const pkg of acceptancePackages) {
          const listedTests = invoke('go', [...testArgs, '-list', '.', pkg], directory);
          if (listedTests.status !== 0) return { status: 1, output };
          const names = listedTests.stdout.split(/\r?\n/).filter(name => /^(Test|Example|Fuzz)[\p{L}\p{N}_]*$/u.test(name)).sort();
          if (!names.length) {
            if (invoke('go', [...testArgs, pkg], directory).status !== 0) return { status: 1, output };
            continue;
          }
          for (let batch = 0; batch < 2; batch++) {
            const tests = names.filter((_, index) => index % 2 === batch);
            if (tests.length && invoke('go', [...testArgs, '-run', `^(${tests.join('|')})$`, pkg], directory).status !== 0) return { status: 1, output };
          }
        }
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
