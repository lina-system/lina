#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Compile and run the portable cores of the locked-Mac native parts: the unlock
// plug-in's decision core and entry points, and the lock guard with its install,
// uninstall and rollback. Security, socket, launchd and clock calls use interfaces
// or fakes, so these checks run on Linux with a C compiler and no macOS SDK.
import { spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { toplevel } from '../git.mjs';

const targets = [
  {
    name: 'unlock plug-in',
    directory: 'native/macos/unlock-plugin',
    sources: [
      'src/lina_unlock.c',
      'src/mechanism.c',
      'src/engine_from_callbacks.c',
      'src/plugin_darwin.c',
      'tests/fake_host.c',
      'tests/fake_log.c',
      'tests/test_decide.c',
      'tests/test_deadline.c',
      'tests/test_mechanism.c',
      'tests/test_plugin.c',
      'tests/main.c',
    ],
  },
  {
    // The guard's core: install, uninstall, the guard pass, the rollback
    // command and the fake authorization database they are tested against.
    name: 'lock guard',
    directory: 'native/macos/lock-guard',
    sources: [
      'src/rule.c',
      'src/sha256.c',
      'src/state.c',
      'src/install.c',
      'src/guard.c',
      'src/restore.c',
      'tests/fake_host.c',
      'tests/test_rule.c',
      'tests/test_sha256.c',
      'tests/test_state.c',
      'tests/test_install.c',
      'tests/test_uninstall.c',
      'tests/test_guard.c',
      'tests/test_restore.c',
      'tests/main.c',
    ],
  },
];

const compilers = process.env.CC ? [process.env.CC] : ['cc', 'clang', 'gcc'];

function compiler() {
  for (const candidate of compilers) {
    const probe = spawnSync(candidate, ['--version'], { encoding: 'utf8' });
    if (probe.status === 0) return candidate;
  }
  return undefined;
}

export function compileArgs(root, binary, target = targets[0]) {
  const directory = join(root, target.directory);
  return [
    '-std=c11', '-Wall', '-Wextra', '-Werror', '-pedantic', '-O1', '-g',
    '-I', join(directory, 'src'), '-I', join(directory, 'tests'), '-I', join(directory, 'tests/fake'),
    ...target.sources.map(name => join(directory, name)),
    '-o', binary,
  ];
}

export function check({ root = toplevel(process.cwd()), cc = compiler(), bash = process.env.BASH || 'bash', log = console.log } = {}) {
  if (!cc) return { status: 1, output: 'no C compiler found: set CC or install cc' };
  const missing = targets.flatMap(target =>
    target.sources.map(name => join(root, target.directory, name)).filter(name => !existsSync(name)));
  if (missing.length) return { status: 1, output: 'missing sources:\n' + missing.join('\n') };
  // The bundle script only runs on macOS, so nothing else reads it. An
  // unterminated quote keeps it from starting at all, and a shell parse is the
  // only check the Linux foundation can make of it.
  const bundle = join(root, 'native/macos/unlock-plugin', 'build.sh');
  const parsed = spawnSync(bash, ['-n', bundle], { encoding: 'utf8', timeout: 30_000 });
  if (parsed.status !== 0) {
    return { status: 1, output: `${parsed.stdout ?? ''}${parsed.stderr ?? ''}${bash} -n exited with ${parsed.status ?? parsed.error?.message}` };
  }
  const directory = mkdtempSync(join(tmpdir(), 'lina-native-'));
  try {
    for (const target of targets) {
      const binary = join(directory, target.name.replace(/\W+/g, '-'));
      const args = compileArgs(root, binary, target);
      log(`$ ${cc} ${args.join(' ')}`);
      const compiled = spawnSync(cc, args, { encoding: 'utf8', timeout: 120_000 });
      const buildOutput = (compiled.stdout ?? '') + (compiled.stderr ?? '');
      if (compiled.status !== 0) {
        return { status: 1, output: `${buildOutput}compiler exited with ${compiled.status ?? compiled.error?.message}` };
      }
      log(`$ ${binary}`);
      const run = spawnSync(binary, [], { encoding: 'utf8', timeout: 60_000 });
      const result = (run.stdout ?? '') + (run.stderr ?? '');
      if (run.status !== 0) return { status: 1, output: buildOutput + result };
      log(result.trim());
    }
    return { status: 0, output: '' };
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const result = check();
  process.stdout.write(result.output);
  process.exit(result.status);
}
