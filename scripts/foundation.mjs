#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// One entry point for local verification and CI selection, shard execution and aggregation.
import { parseArgs } from 'node:util';
import { pathToFileURL } from 'node:url';
import { git, gitOut, identityEnv, resolveCommit, stripGitEnv, toplevel } from './git.mjs';
import { evaluateGate } from './verify/gate.mjs';
import { scopeFor } from './verify/scope.mjs';
import { aggregate, executeTask, makePlan, readPlan, readResults, runPipeline, sealPlan, writeJSON } from './verify/pipeline.mjs';

stripGitEnv();
export const notesRef = 'refs/notes/foundation';
const seconds = since => Math.round((Date.now() - since) / 100) / 10;
const cleanTree = root => git(root, ['status', '--porcelain', '--untracked-files=normal']).stdout.trim() === '';

function defaultBase(root, head) {
  const dev = resolveCommit(root, 'refs/heads/dev');
  if (dev && dev !== head) {
    const mergeBase = git(root, ['merge-base', dev, head]).stdout.trim();
    if (mergeBase && mergeBase !== head) return mergeBase;
  }
  return resolveCommit(root, head + '^1') ?? '';
}

function parseRange(range) {
  if (!range) return { base: undefined, head: 'HEAD' };
  const split = range.indexOf('..');
  if (split <= 0 || range[split + 2] === '.') throw new Error('--range must look like <base>..<head>: ' + range);
  return { base: range.slice(0, split), head: range.slice(split + 2) || 'HEAD' };
}

export function selectFoundation({ cwd = process.cwd(), range, full = false, log = console.error } = {}) {
  const started = Date.now();
  const root = toplevel(cwd);
  const clean = cleanTree(root);
  if (full && range) throw new Error('--full and --range cannot be combined');
  const requested = parseRange(range);
  const head = resolveCommit(root, requested.head);
  let scope = { head };
  try {
    if (!head) throw new Error('Unknown candidate revision: ' + requested.head);
    if (head !== resolveCommit(root, 'HEAD')) throw new Error('Candidate ' + head + ' is not the checked-out HEAD; check out the candidate first.');
    scope = scopeFor({ base: full ? '' : requested.base ?? defaultBase(root, head), head, full, cwd: root });
    const whitespace = scope.base ? git(root, ['diff', '--check', scope.base, scope.head])
      : git(root, ['show', '--format=', '--check', scope.head]);
    if (whitespace.status !== 0) throw new Error((whitespace.stdout + whitespace.stderr).trim());
    const plan = makePlan({ root, scope, selection: { result: 'success' }, started, clean });
    return { root, plan: sealPlan({ ...plan, selection: { result: 'success', seconds: seconds(started) } }) };
  } catch (error) {
    log('selection failed:\n' + error.message);
    return { root, plan: makePlan({ root, scope, selection: { result: 'failure', seconds: seconds(started) }, started, clean }) };
  }
}

export function finishFoundation(root, plan, outcomes, { note = true } = {}) {
  const gate = evaluateGate(plan.scope, outcomes.checks);
  const failures = [...gate.failures, ...outcomes.failures];
  if (plan.selection.result === 'success' && (resolveCommit(root, 'HEAD') !== plan.head
      || gitOut(root, ['rev-parse', 'HEAD^{tree}']) !== plan.tree)) {
    failures.push('The checked-out candidate changed during verification');
  }
  const evidence = {
    schema: 1, result: failures.length ? 'failure' : 'success', mode: plan.scope.mode ?? null,
    reason: plan.scope.reason ?? null, base: plan.scope.base ?? null, head: plan.head, tree: plan.tree,
    selectedComponents: plan.scope.selectedComponents ?? [], selectedShards: plan.scope.componentShards ?? [],
    e2eReason: plan.e2eReason, selectedScenarios: [...new Set(plan.scenarios.flatMap(scenario => scenario.files ?? [scenario.file]))].sort(),
    checks: outcomes.checks, failures,
    tools: { node: process.version, git: git(root, ['--version']).stdout.trim().replace(/^git version /, ''),
      go: outcomes.checks.components?.toolchain ?? outcomes.checks.dependencies?.toolchain },
    finished: new Date().toISOString(), seconds: seconds(plan.started), note: 'not written',
  };
  if (evidence.result === 'success' && note) {
    if (plan.clean && cleanTree(root) && resolveCommit(root, 'HEAD') === plan.head
        && gitOut(root, ['rev-parse', 'HEAD^{tree}']) === plan.tree) {
      const written = git(root, ['notes', '--ref=' + notesRef, 'add', '-f', '-m',
        JSON.stringify({ ...evidence, note: undefined }), plan.head], { env: identityEnv(root) });
      if (written.status !== 0) throw new Error('Could not write the evidence note: ' + written.stderr.trim());
      evidence.note = 'written';
    } else evidence.note = 'not written: the work tree is not a clean checkout of the candidate commit';
  }
  return evidence;
}

export async function foundation({ cwd = process.cwd(), range, full = false, note = true, jobs, log = console.error } = {}) {
  const { root, plan } = selectFoundation({ cwd, range, full, log });
  return finishFoundation(root, plan, await runPipeline(root, plan, { jobs, log }), { note });
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const { values } = parseArgs({ options: {
      range: { type: 'string' }, full: { type: 'boolean', default: false }, 'no-note': { type: 'boolean', default: false },
      select: { type: 'string' }, plan: { type: 'string' }, shard: { type: 'string' }, out: { type: 'string' },
      results: { type: 'string' }, upstream: { type: 'string' }, jobs: { type: 'string' },
    } });
    let result;
    if (values.plan) {
      if (values.range || values.full || values.select || values.jobs) throw new Error('A saved plan cannot be reselected');
      const root = toplevel(process.cwd());
      const plan = readPlan(values.plan, root);
      if (values.shard) {
        if (!values.out || values.results || values.upstream) throw new Error('--shard requires --out and cannot aggregate results');
        const record = await executeTask(root, plan, values.shard);
        writeJSON(values.out, record);
        result = { id: record.id, result: record.result, seconds: record.seconds };
      } else {
        if (!values.results || values.out) throw new Error('Aggregation requires --results');
        result = finishFoundation(root, plan, aggregate(plan, readResults(values.results), values.upstream ?? 'success'), { note: !values['no-note'] });
      }
    } else if (values.select) {
      if (values.shard || values.results || values.out || values.upstream || values.jobs) throw new Error('Selection cannot execute or aggregate shards');
      const { plan } = selectFoundation({ range: values.range, full: values.full });
      writeJSON(values.select, plan);
      result = { head: plan.head, result: plan.selection.result, tasks: plan.tasks.map(task => ({ id: task.id, kind: task.kind })) };
    } else {
      if (values.shard || values.results || values.out || values.upstream) throw new Error('Shard options require --plan');
      result = await foundation({ range: values.range, full: values.full, note: !values['no-note'], jobs: values.jobs === undefined ? undefined : Number(values.jobs) });
    }
    console.log(JSON.stringify(result));
    if (result.result !== 'success') {
      console.error('foundation: failure\n' + (result.failures ?? []).join('\n'));
      process.exitCode = 1;
    }
  } catch (error) {
    console.error('foundation: ' + error.message);
    process.exitCode = 1;
  }
}
