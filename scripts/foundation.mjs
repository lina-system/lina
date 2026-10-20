#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Foundation verification in one process: selection (path classification and the whitespace check),
// then the docs and automation checks in parallel, then the gate. A passing run on a clean
// checkout of the exact candidate commit leaves its evidence as a Git note in refs/notes/foundation.
//
//   node scripts/foundation.mjs [--range <base>..<head>] [--full] [--no-note]
//
// --range  measures the change from <base> to <head>. The default measures the checked-out commit
//          from the commit it was developed on: the merge base with dev, or the first parent when
//          the checkout is dev itself (the previous dev head).
// --full   runs every check regardless of the changed paths and checks the whitespace of HEAD.
// --no-note  skips writing the evidence note.
import { readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import {
  cleanEnv, git, gitOut, identityEnv, resolveCommit, runAsync, stripGitEnv, toplevel,
} from './git.mjs';
import { evaluateGate } from './verify/gate.mjs';
import { scopeFor } from './verify/scope.mjs';

stripGitEnv();

const scriptsDir = dirname(fileURLToPath(import.meta.url));
export const notesRef = 'refs/notes/foundation';

function sourceFiles(directory, pattern) {
  return readdirSync(directory, { withFileTypes: true })
    .filter((entry) => entry.isFile() && pattern.test(entry.name)).map((entry) => join(directory, entry.name)).sort();
}

function automationFiles() {
  const directories = [scriptsDir, join(scriptsDir, 'verify')];
  return {
    scripts: directories.flatMap((directory) => sourceFiles(directory, /\.mjs$/)),
    tests: directories.flatMap((directory) => sourceFiles(directory, /\.test\.mjs$/)),
  };
}

// The commit the change is measured from when no --range is given.
function defaultBase(root, head) {
  const dev = resolveCommit(root, 'refs/heads/dev');
  if (dev && dev !== head) {
    const mergeBase = git(root, ['merge-base', dev, head]).stdout.trim();
    if (mergeBase && mergeBase !== head) return mergeBase;
  }
  return resolveCommit(root, `${head}^1`) ?? '';
}

function parseRange(range) {
  if (!range) return { base: undefined, head: 'HEAD' };
  const split = range.indexOf('..');
  if (split <= 0 || range[split + 2] === '.') throw new Error(`--range must look like <base>..<head>: ${range}`);
  return { base: range.slice(0, split), head: range.slice(split + 2) || 'HEAD' };
}

function whitespaceCheck(root, scope) {
  const result = scope.base
    ? git(root, ['diff', '--check', scope.base, scope.head])
    : git(root, ['show', '--format=', '--check', scope.head]);
  return { result: result.status === 0 ? 'success' : 'failure', output: `${result.stdout}${result.stderr}`.trim() };
}

const seconds = (since) => Math.round((Date.now() - since) / 100) / 10;

// Every check is bounded: a check that hangs fails instead of holding the run.
const timeouts = { docs: 120_000, automation: 600_000 };

async function runChecks(root, scope, log) {
  const env = cleanEnv();
  const timed = async (name, work) => {
    const started = Date.now();
    const outcome = await work();
    return { name, ...outcome, seconds: seconds(started) };
  };
  const docs = scope.docs ? timed('docs', () => runAsync(process.execPath,
    [join(scriptsDir, 'verify/check-docs.mjs')], { cwd: root, env, timeoutMs: timeouts.docs })) : null;
  const automation = scope.automation ? timed('automation', async () => {
    const { scripts, tests } = automationFiles();
    let output = '';
    for (const file of scripts) {
      const syntax = await runAsync(process.execPath, ['--check', file], { cwd: root, env, timeoutMs: timeouts.automation });
      output += syntax.output;
      if (syntax.status !== 0) return { status: 1, output };
    }
    if (!tests.length) return { status: 0, output: `${output}no test files\n` };
    const suite = await runAsync(process.execPath, ['--test', ...tests], { cwd: root, env, timeoutMs: timeouts.automation });
    return { status: suite.status, output: output + suite.output };
  }) : null;
  const finished = await Promise.all([docs, automation]);
  const results = {};
  for (const [name, run] of [['docs', finished[0]], ['automation', finished[1]]]) {
    if (!run) results[name] = { result: 'skipped' };
    else {
      results[name] = { result: run.status === 0 ? 'success' : 'failure', seconds: run.seconds };
      if (run.status !== 0) log(`${name} failed:\n${run.output.trim()}`);
    }
  }
  return results;
}

/**
 * Runs foundation for the repository that contains `cwd` and returns the evidence object. The
 * evidence note is written only when the work tree is clean and the candidate is the checked-out
 * commit, because only then do the checks speak for that exact commit.
 */
export async function foundation({ cwd = process.cwd(), range, full = false, note = true, log = console.error } = {}) {
  const started = Date.now();
  const root = toplevel(cwd);
  if (full && range) throw new Error('--full and --range cannot be combined');
  const { base: requestedBase, head: requestedHead } = parseRange(range);
  const resolvedHead = resolveCommit(root, requestedHead);

  const results = {};
  let scope = {};
  let selectionOutput = '';
  try {
    if (!resolvedHead) throw new Error(`Unknown candidate revision: ${requestedHead}`);
    const base = full ? '' : requestedBase ?? defaultBase(root, resolvedHead);
    scope = scopeFor({ base, head: resolvedHead, full, cwd: root });
    const whitespace = whitespaceCheck(root, scope);
    results.selection = { result: whitespace.result, seconds: seconds(started) };
    selectionOutput = whitespace.output;
  } catch (error) {
    results.selection = { result: 'failure', seconds: seconds(started) };
    selectionOutput = error.message;
  }
  if (results.selection.result === 'success') {
    Object.assign(results, await runChecks(root, scope, log));
  } else {
    log(`selection failed:\n${selectionOutput}`);
  }

  const gate = evaluateGate(scope, results);
  const evidence = {
    schema: 1, result: gate.result, mode: scope.mode ?? null, reason: scope.reason ?? null,
    base: scope.base ?? null, head: scope.head ?? resolvedHead ?? null,
    tree: resolvedHead ? gitOut(root, ['rev-parse', `${resolvedHead}^{tree}`]) : null,
    checks: results, failures: gate.failures,
    tools: { node: process.version, git: git(root, ['--version']).stdout.trim().replace(/^git version /, '') },
    finished: new Date().toISOString(), seconds: Math.round((Date.now() - started) / 100) / 10,
  };

  const clean = git(root, ['status', '--porcelain', '--untracked-files=normal']).stdout.trim() === '';
  const checkedOut = resolvedHead !== null && resolveCommit(root, 'HEAD') === resolvedHead;
  evidence.note = 'not written';
  if (gate.result === 'success' && note) {
    if (clean && checkedOut) {
      const written = git(root, ['notes', `--ref=${notesRef}`, 'add', '-f', '-m', JSON.stringify({ ...evidence, note: undefined }), evidence.head],
        { env: identityEnv(root) });
      if (written.status !== 0) throw new Error(`Could not write the evidence note: ${written.stderr.trim()}`);
      evidence.note = 'written';
    } else {
      evidence.note = 'not written: the work tree is not a clean checkout of the candidate commit';
    }
  }
  return evidence;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const { values } = parseArgs({ options: {
      range: { type: 'string' }, full: { type: 'boolean', default: false }, 'no-note': { type: 'boolean', default: false },
    } });
    const evidence = await foundation({ range: values.range, full: values.full, note: !values['no-note'] });
    console.log(JSON.stringify(evidence));
    if (evidence.result !== 'success') {
      console.error(`foundation: failure\n${evidence.failures.join('\n')}`);
      process.exitCode = 1;
    }
  } catch (error) {
    console.error(`foundation: ${error.message}`);
    process.exitCode = 1;
  }
}
