#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Build both commands and record provenance only for a clean, unchanged commit.
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { gitOut } from './git.mjs';
import { createGoContext, execute } from './verify/check-components.mjs';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const release = /^v0\.0\.1-alpha\.(\d{4})(\d{2})(\d{2})\.[1-9]\d*$/;

function releaseVersion(tag) {
  const match = release.exec(tag);
  if (!match) return false;
  const date = `${match[1]}-${match[2]}-${match[3]}`;
  const time = Date.parse(`${date}T00:00:00Z`);
  return Number.isFinite(time) && new Date(time).toISOString().slice(0, 10) === date;
}

export function build({ cwd = root, out, execute: run = execute }) {
  if (!out) throw new Error('An output directory is required');
  out = resolve(out);
  const names = ['lina', 'lina-node'].map(name => name + (process.platform === 'win32' ? '.exe' : ''));
  const artifacts = [...names, 'manifest.json'].map(name => relative(cwd, join(out, name)).replaceAll('\\', '/'));
  const inside = artifacts.filter(name => !name.startsWith('../'));
  // Never overwrite source files that happen to share an artifact's name.
  if (inside.length && gitOut(cwd, ['ls-files', '--', ...inside.map(name => `:(top,literal)${name}`)])) {
    throw new Error('Output artifacts must not be tracked source files');
  }
  const clean = () => gitOut(cwd, ['status', '--porcelain=v1', '--untracked-files=all', '--', '.',
    ...inside.map(name => `:(top,literal,exclude)${name}`)]) === '';
  const commit = gitOut(cwd, ['rev-parse', 'HEAD']);
  const initiallyClean = clean();
  mkdirSync(out, { recursive: true });
  const manifestPath = join(out, 'manifest.json');
  rmSync(manifestPath, { force: true });
  const kitText = readFileSync(join(cwd, 'kit/VERSION'), 'utf8').trim();
  const kitVersion = Number(kitText);
  if (!/^[1-9]\d*$/.test(kitText) || !Number.isSafeInteger(kitVersion)) throw new Error('kit/VERSION must be a positive safe integer');
  const tag = initiallyClean && gitOut(cwd, ['tag', '--points-at', commit, '--sort=-version:refname'])
    .split('\n').find(releaseVersion);
  const version = tag ? tag.slice(1) : '0.0.1-alpha.dev';
  const date = new Date().toISOString().slice(0, 10);
  const context = createGoContext(cwd);
  const invoke = args => {
    const result = run('go', args, { cwd, env: context.env });
    if (result.status !== 0) throw new Error(`go ${args[0]} failed: ${result.stderr || result.stdout}`);
    return result.stdout.trim();
  };
  try {
    const goToolchain = invoke(['env', 'GOVERSION']);
    if (goToolchain !== context.toolchain) throw new Error(`Expected Go toolchain ${context.toolchain}, got ${goToolchain}`);
    const prefix = 'github.com/thisisjun786/lina/internal/buildinfo';
    const ldflags = Object.entries({ Version: version, Commit: commit, Date: date })
      .map(([key, value]) => `-X ${prefix}.${key}=${value}`).join(' ');
    for (const name of names) {
      invoke(['build', '-mod=readonly', '-trimpath', '-ldflags', ldflags, '-o', join(out, name), `./cmd/${name.replace(/\.exe$/, '')}`]);
    }
    if (!initiallyClean || !clean() || gitOut(cwd, ['rev-parse', 'HEAD']) !== commit) return null;
    const binaries = names.map(name => {
      const bytes = readFileSync(join(out, name));
      return { name, size: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex') };
    });
    const sqlite = JSON.parse(invoke(['run', '-mod=readonly', './internal/core/manifest/sqliteinfo']));
    if (!clean() || gitOut(cwd, ['rev-parse', 'HEAD']) !== commit) return null;
    const providers = JSON.parse(readFileSync(join(cwd, 'kit/provider/responses/manifest.json'), 'utf8'));
    const sdk = JSON.parse(invoke(['list', '-m', '-json', 'github.com/openai/openai-go/v3']));
    if (providers.chatgpt.sdkModule !== sdk.Path || providers.chatgpt.sdkVersion !== sdk.Version || providers.chatgpt.retryCount !== 0) {
      throw new Error('Responses manifest does not match the pinned SDK or retry policy');
    }
    if (!clean() || gitOut(cwd, ['rev-parse', 'HEAD']) !== commit) return null;
    const manifest = { version, commit, date, goToolchain, kitVersion, binaries, sqlite, providers };
    const staging = mkdtempSync(join(out, '.manifest-'));
    try {
      const temporary = join(staging, 'manifest.json');
      writeFileSync(temporary, JSON.stringify(manifest, null, 2) + '\n');
      renameSync(temporary, manifestPath);
    } finally {
      rmSync(staging, { recursive: true, force: true });
    }
    return manifest;
  } finally {
    context.close();
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    let out;
    try {
      ({ values: { out } } = parseArgs({ options: { out: { type: 'string' } }, allowPositionals: false }));
      if (!out) throw new Error('missing output directory');
    } catch {
      throw new Error('usage: node scripts/build.mjs --out <dir>');
    }
    const manifest = build({ out });
    console.log(manifest ? `Built binaries and ${join(out, 'manifest.json')}` : 'Built binaries; source is dirty or changed during the build, so no manifest was written.');
  } catch (error) {
    console.error(`build: ${error.message}`);
    process.exitCode = 1;
  }
}
