#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Build both commands and record provenance only for a clean, unchanged commit.
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { basename, dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { gitOut } from './git.mjs';
import { createGoContext, execute } from './verify/check-components.mjs';

export const buildTimeoutMs = 595_000;

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const release = /^v0\.0\.1-alpha\.(\d{4})(\d{2})(\d{2})\.[1-9]\d*$/;

function releaseVersion(tag) {
  const match = release.exec(tag);
  if (!match) return false;
  const date = `${match[1]}-${match[2]}-${match[3]}`;
  const time = Date.parse(`${date}T00:00:00Z`);
  return Number.isFinite(time) && new Date(time).toISOString().slice(0, 10) === date;
}

// Resolve existing ancestors even when the output directory has not been created.
function outputPath(path) {
  try {
    return realpathSync(path);
  } catch (error) {
    const parent = dirname(path);
    if (error.code !== 'ENOENT' || parent === path) throw error;
    return join(outputPath(parent), basename(path));
  }
}

export function build({ cwd = root, out, target, version: requestedVersion, execute: run = execute,
  timeoutMs = buildTimeoutMs }) {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0) throw new Error('Build budget must be a positive integer in milliseconds');
  const started = performance.now();
  const deadline = started + timeoutMs;
  if (!out) throw new Error('An output directory is required');
  const hostTarget = `${process.platform === 'win32' ? 'windows' : process.platform}/${process.arch === 'x64' ? 'amd64' : process.arch}`;
  target ??= hostTarget;
  if (!['linux/amd64', 'linux/arm64', 'darwin/amd64', 'darwin/arm64', 'windows/amd64', 'windows/arm64'].includes(target)) {
    throw new Error(`Unsupported build target: ${target}`);
  }
  if (requestedVersion !== undefined && !releaseVersion(`v${requestedVersion}`)) throw new Error('Invalid release version');
  cwd = realpathSync(cwd);
  out = outputPath(resolve(out));
  const [goos, goarch] = target.split('/');
  const names = ['lina', 'lina-node'].map(name => name + (goos === 'windows' ? '.exe' : ''));
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
  const version = initiallyClean ? (requestedVersion ?? (tag ? tag.slice(1) : '0.0.1-alpha.dev')) : '0.0.1-alpha.dev';
  const date = new Date().toISOString().slice(0, 10);
  const remaining = () => {
    const budget = Math.floor(deadline - performance.now());
    if (budget <= 0) throw new Error(`build budget exhausted after ${((performance.now() - started) / 1000).toFixed(3)} s`);
    return budget;
  };
  const context = createGoContext(cwd, { timeoutMs: Math.min(30_000, remaining()) });
  const invoke = (args, env = context.env, directory = cwd) => {
    let budget;
    try { budget = remaining(); } catch (error) { throw new Error(`${error.message} before: go ${args.join(' ')}`); }
    const limit = { env: 30_000, mod: 120_000, run: 120_000 }[args[0]] ?? budget;
    const result = run('go', args, { cwd: directory, env, timeoutMs: Math.min(limit, budget) });
    if (result.status !== 0) throw new Error(`go ${args[0]} failed: ${result.stderr || result.stdout}`);
    return result.stdout.trim();
  };
  try {
    const goToolchain = invoke(['env', 'GOVERSION']);
    if (goToolchain !== context.toolchain) throw new Error(`Expected Go toolchain ${context.toolchain}, got ${goToolchain}`);
    for (const directory of [cwd, join(cwd, 'kit')]) invoke(['mod', 'verify'], context.env, directory);
    const prefix = 'github.com/thisisjun786/lina/internal/buildinfo';
    const ldflags = Object.entries({ Version: version, Commit: commit, Date: date })
      .map(([key, value]) => `-X ${prefix}.${key}=${value}`).join(' ');
    for (const name of names) {
      invoke(['build', '-mod=readonly', '-trimpath', '-ldflags', ldflags, '-o', join(out, name), `./cmd/${name.replace(/\.exe$/, '')}`],
        { ...context.env, GOOS: goos, GOARCH: goarch });
    }
    if (!initiallyClean || !clean() || gitOut(cwd, ['rev-parse', 'HEAD']) !== commit) return null;
    const binaries = names.map(name => {
      const bytes = readFileSync(join(out, name));
      return { name, size: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex') };
    });
    const sqlite = JSON.parse(invoke(['run', '-mod=readonly', './internal/core/manifest/sqliteinfo']));
    const supportedCombinations = JSON.parse(invoke(['run', '-mod=readonly', './internal/core/manifest/clientinfo', target]));
    if (!clean() || gitOut(cwd, ['rev-parse', 'HEAD']) !== commit) return null;
    const providers = JSON.parse(readFileSync(join(cwd, 'kit/provider/responses/manifest.json'), 'utf8'));
    if (providers.chatgpt.adapter !== 'responses' || providers.chatgpt.retryCount !== 0) {
      throw new Error('Responses manifest does not match the adapter or retry policy');
    }
    if (!clean() || gitOut(cwd, ['rev-parse', 'HEAD']) !== commit) return null;
    const verifiedWorkerVersions = { codex: JSON.parse(readFileSync(join(cwd, 'internal/core/worker/codex/versions.json'), 'utf8')) };
    const manifest = { version, commit, date, target, goToolchain, kitVersion, binaries, sqlite, providers, supportedCombinations, verifiedWorkerVersions };
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
    let out, target, version, timeout;
    try {
      ({ values: { out, target, version, 'timeout-ms': timeout } } = parseArgs({ options: {
        out: { type: 'string' }, target: { type: 'string' }, version: { type: 'string' }, 'timeout-ms': { type: 'string' },
      }, allowPositionals: false }));
      if (!out) throw new Error('missing output directory');
    } catch {
      throw new Error('usage: node scripts/build.mjs --out <dir> [--target <os/arch>] [--version <release-version>] [--timeout-ms <milliseconds>]');
    }
    const manifest = build({ out, target, version, timeoutMs: timeout === undefined ? undefined : Number(timeout) });
    console.log(manifest ? `Built binaries and ${join(out, 'manifest.json')}` : 'Built binaries; source is dirty or changed during the build, so no manifest was written.');
  } catch (error) {
    console.error(`build: ${error.message}`);
    process.exitCode = 1;
  }
}
