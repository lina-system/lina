#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Compile and run the portable decision core of the locked-Mac unlock plug-in.
// The core keeps every Security, socket and clock call behind an interface, so
// this check runs on Linux with a C compiler and no macOS SDK.
import { spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { toplevel } from '../git.mjs';

const plugin = 'native/macos/unlock-plugin';
const sources = [
  'src/lina_unlock.c',
  'src/mechanism.c',
  'src/engine_from_callbacks.c',
  'tests/fake_host.c',
  'tests/test_decide.c',
  'tests/test_deadline.c',
  'tests/test_mechanism.c',
  'tests/main.c',
];

const compilers = process.env.CC ? [process.env.CC] : ['cc', 'clang', 'gcc'];

function compiler() {
  for (const candidate of compilers) {
    const probe = spawnSync(candidate, ['--version'], { encoding: 'utf8' });
    if (probe.status === 0) return candidate;
  }
  return undefined;
}

export function compileArgs(root, binary) {
  const directory = join(root, plugin);
  return [
    '-std=c11', '-Wall', '-Wextra', '-Werror', '-pedantic', '-O1', '-g',
    '-I', join(directory, 'src'), '-I', join(directory, 'tests'), '-I', join(directory, 'tests/fake'),
    ...sources.map(name => join(directory, name)),
    '-o', binary,
  ];
}

export function check({ root = toplevel(process.cwd()), cc = compiler(), log = console.log } = {}) {
  const missing = sources.map(name => join(root, plugin, name)).filter(name => !existsSync(name));
  if (missing.length) return { status: 1, output: 'missing sources:\n' + missing.join('\n') };
  if (!cc) return { status: 1, output: 'no C compiler found: set CC or install cc' };
  const directory = mkdtempSync(join(tmpdir(), 'lina-unlock-'));
  const binary = join(directory, 'lina-unlock-tests');
  try {
    const args = compileArgs(root, binary);
    log(`$ ${cc} ${args.join(' ')}`);
    const compiled = spawnSync(cc, args, { encoding: 'utf8', timeout: 120_000 });
    const output = (compiled.stdout ?? '') + (compiled.stderr ?? '');
    if (compiled.status !== 0) return { status: 1, output: `${output}compiler exited with ${compiled.status ?? compiled.error?.message}` };
    log(`$ ${binary}`);
    const run = spawnSync(binary, [], { encoding: 'utf8', timeout: 60_000 });
    const result = (run.stdout ?? '') + (run.stderr ?? '');
    return { status: run.status === 0 ? 0 : 1, output: output + result };
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const result = check();
  process.stdout.write(result.output);
  process.exit(result.status);
}
