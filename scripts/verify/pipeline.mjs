// SPDX-License-Identifier: Apache-2.0
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { availableParallelism, tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { cleanEnv, gitOut, resolveCommit, runAsync } from '../git.mjs';
import { checkComponents, componentsTimeoutMs } from './check-components.mjs';
import { planComponents } from './shard-plan.mjs';
import { registryFor } from './scope.mjs';

const basic = ['docs', 'automation', 'dependencies'];
const goKinds = ['build', 'unit', 'e2e'];
const timeouts = { docs: 120_000, automation: 600_000, dependencies: 600_000,
  build: componentsTimeoutMs, unit: componentsTimeoutMs, e2e: componentsTimeoutMs };
const digestOf = plan => createHash('sha256').update(JSON.stringify({ ...plan, digest: undefined })).digest('hex');
export const sealPlan = plan => ({ ...plan, digest: digestOf(plan) });

export function makePlan({ root, scope, selection, started, clean }) {
  const componentPlan = selection.result === 'success' ? planComponents({ cwd: root, scope }) : { tasks: [], selectedComponents: [] };
  const selected = selection.result === 'success'
    ? { ...scope, selectedComponents: componentPlan.selectedComponents, componentShards: componentPlan.tasks.map(task => task.id) }
    : scope;
  return sealPlan({
    schema: 2, head: scope.head ?? null, tree: scope.head ? gitOut(root, ['rev-parse', scope.head + '^{tree}']) : null,
    started, clean, selection, scope: selected,
    e2eReason: componentPlan.e2eReason, scenarios: componentPlan.scenarios ?? [],
    tasks: selection.result === 'success'
      ? [...basic.filter(name => scope[name]).map(name => ({ id: name, kind: name })), ...componentPlan.tasks] : [],
  });
}

export function writeJSON(path, value) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, JSON.stringify(value) + '\n');
}

export function validatePlan(plan, root) {
  if (!plan || plan.schema !== 2 || plan.digest !== digestOf(plan) || !Array.isArray(plan.tasks)
      || plan.selection?.result !== 'success' || plan.scope?.head !== plan.head
      || !Number.isFinite(plan.started) || typeof plan.clean !== 'boolean') throw new Error('Invalid foundation plan');
  if (resolveCommit(root, 'HEAD') !== plan.head || gitOut(root, ['rev-parse', 'HEAD^{tree}']) !== plan.tree) {
    throw new Error('Foundation plan does not describe the checked-out candidate');
  }
  const expected = [...basic.filter(name => plan.scope[name]), ...(plan.scope.componentShards ?? [])].sort();
  const actual = plan.tasks.map(task => task.id).sort();
  if (new Set(actual).size !== actual.length || JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error('Foundation plan has missing, duplicate or unexpected tasks');
  }
  if (plan.tasks.filter(task => task.kind === 'build').length !== (plan.scope.components ? 1 : 0)) {
    throw new Error('Foundation plan must include exactly one build check when components are selected');
  }
  const registry = registryFor(root);
  for (const task of plan.tasks) {
    if (!/^[a-z][a-z0-9-]*$/.test(task.id) || ![...basic, ...goKinds].includes(task.kind)) throw new Error('Invalid foundation task');
    if (basic.includes(task.kind)) {
      if (task.id !== task.kind) throw new Error('Invalid basic check identity');
      continue;
    }
    if (!Array.isArray(task.components) || !task.components.length
        || (task.kind === 'build' ? task.id !== 'build' : !task.id.startsWith(task.kind + '-'))) {
      throw new Error('Invalid component task');
    }
    for (const component of task.components) {
      const registered = registry.find(item => item.name === component.name);
      if (!registered || component.module !== registered.module || component.binary !== registered.binary
          || !Array.isArray(component.packages) || !component.packages.length
          || component.packages.some(pkg => typeof pkg !== 'string'
            || !/^(?:\.|\.\/[A-Za-z0-9_./-]+|[A-Za-z0-9_][A-Za-z0-9_./-]*)$/.test(pkg)
            || pkg.split('/').includes('..'))) {
        throw new Error('Invalid component task packages');
      }
    }
    if (task.kind === 'e2e') {
      if (!Array.isArray(task.tests) || task.tests.some(name => typeof name !== 'string' || !/^(Test|Example|Fuzz)[\p{L}\p{N}_]*$/u.test(name))
          || new Set(task.tests).size !== task.tests.length
          || task.testPattern !== (task.tests.length ? '^(' + task.tests.join('|') + ')$' : '^$')) {
        throw new Error('Invalid e2e task selection');
      }
    }
  }
  return plan;
}

export function readPlan(path, root) {
  return validatePlan(JSON.parse(readFileSync(path, 'utf8')), root);
}

function sourceFiles(directory, pattern) {
  return readdirSync(directory, { withFileTypes: true }).filter(entry => entry.isFile() && pattern.test(entry.name))
    .map(entry => join(directory, entry.name)).sort();
}

async function automation(root, env) {
  const directories = [join(root, 'scripts'), join(root, 'scripts/verify')];
  const scripts = directories.flatMap(directory => sourceFiles(directory, /\.mjs$/));
  const tests = directories.flatMap(directory => sourceFiles(directory, /\.test\.mjs$/));
  const workflowDir = join(root, '.github/workflows');
  const workflows = existsSync(workflowDir) ? sourceFiles(workflowDir, /\.ya?ml$/) : [];
  let output = '';
  const started = Date.now();
  const invoke = async (command, args) => {
    const remaining = timeouts.automation - (Date.now() - started);
    if (remaining <= 0) return { status: 1, output: 'automation budget exhausted\n' };
    const result = await runAsync(command, args, { cwd: root, env, timeoutMs: remaining });
    output += result.output;
    return result;
  };
  if (workflows.length && (await invoke('actionlint', workflows)).status !== 0) {
    return { status: 1, output: 'actionlint failed:\n' + output };
  }
  for (const file of scripts) if ((await invoke(process.execPath, ['--check', file])).status !== 0) return { status: 1, output };
  if (!tests.length) return { status: 0, output: output + 'no test files\n' };
  const suite = await invoke(process.execPath, ['--test', ...tests]);
  return { status: suite.status, output };
}

// Keep test identity and bounded diagnostic lines for post-merge QA. Nested
// failures belong to one top-level scenario, not separate duplicate issues.
export function goTestOutcomes(output) {
  const tests = new Map();
  for (const line of output.split('\n')) {
    let event;
    try { event = JSON.parse(line); } catch { continue; }
    if (!event.Package || !event.Test) continue;
    const name = event.Test.split('/')[0];
    const key = event.Package + ':' + name;
    if (!tests.has(key)) tests.set(key, { package: event.Package, test: name, result: 'unknown', output: '' });
    const record = tests.get(key);
    if (event.Output) record.output = (record.output + event.Output).slice(-16000);
    if (event.Test === name && ['pass', 'fail', 'skip'].includes(event.Action)) record.result = event.Action;
  }
  return [...tests.values()];
}

export async function executeTask(root, plan, id, log = console.error) {
  validatePlan(plan, root);
  const task = plan.tasks.find(task => task.id === id);
  if (!task) throw new Error('Task is not selected: ' + id);
  const started = Date.now();
  let outcome;
  if (goKinds.includes(task.kind)) {
    outcome = checkComponents({ cwd: root, components: task.components, phase: task.kind,
      testPattern: task.testPattern, json: true });
  } else if (task.kind === 'automation') outcome = await automation(root, cleanEnv());
  else outcome = await runAsync(process.execPath, [join(root, 'scripts/verify/check-' + task.kind + '.mjs')],
    { cwd: root, env: cleanEnv(), timeoutMs: timeouts[task.kind] });
  const finished = Date.now();
  const timings = [];
  for (const line of outcome.output.split('\n')) {
    let event;
    try { event = JSON.parse(line); } catch { /* Command headers and ordinary output are retained on failure. */ }
    if (event?.Package && ['pass', 'fail', 'skip'].includes(event.Action) && (!event.Test || !event.Test.includes('/'))) {
      timings.push({ package: event.Package, test: event.Test, seconds: event.Elapsed ?? 0, result: event.Action });
    }
    if (outcome.status === 0) {
      const text = event?.Output ?? line;
      if (/^ok\s+\S+\s+\d+\.\d+s/.test(text)) log(text.trimEnd());
    }
  }
  if (outcome.status === 0 && ['unit', 'e2e'].includes(task.kind)) {
    const completed = new Set(timings.filter(event => !event.test).map(event => event.package));
    const missingPackages = task.components.flatMap(component => component.packages).filter(pkg => !completed.has(pkg));
    const tested = new Set(timings.filter(event => event.test).map(event => event.test));
    const missingTests = (task.tests ?? []).filter(name => !tested.has(name));
    if (missingPackages.length || missingTests.length) {
      outcome.status = 1;
      outcome.output += '\nGo omitted selected work: ' + [...missingPackages, ...missingTests].join(', ') + '\n';
    }
  }
  if (outcome.status !== 0) log(id + ' failed:\n' + outcome.output.trim());
  return {
    schema: 1, id, kind: task.kind, head: plan.head, tree: plan.tree, digest: plan.digest,
    result: outcome.status === 0 ? 'success' : 'failure', started, finished, seconds: (finished - started) / 1000,
    tests: task.kind === 'e2e' ? goTestOutcomes(outcome.output) : undefined,
    toolchain: outcome.toolchain ?? outcome.output.match(/^go1\.\d+\.\d+$/m)?.[0], timings,
  };
}

export function aggregate(plan, records, upstream = 'success') {
  const failures = [];
  const valid = new Map();
  if (upstream !== 'success') failures.push('Upstream jobs did not succeed: ' + upstream);
  for (const record of records) {
    const task = plan.tasks.find(task => task.id === record?.id);
    if (!task || valid.has(record.id)) {
      failures.push('Unexpected or duplicate shard result: ' + String(record?.id));
      continue;
    }
    if (record.schema !== 1 || record.head !== plan.head || record.tree !== plan.tree || record.digest !== plan.digest
        || record.kind !== task.kind || !['success', 'failure'].includes(record.result)
        || !Number.isFinite(record.seconds) || record.seconds < 0
        || !Number.isFinite(record.started) || !Number.isFinite(record.finished) || record.finished < record.started) {
      failures.push('Stale or malformed shard result: ' + record.id);
      valid.set(record.id, { result: 'failure' });
    } else valid.set(record.id, record);
  }
  const checks = { selection: plan.selection };
  if (plan.selection.result !== 'success') return { checks, failures };
  for (const name of basic) {
    const record = valid.get(name);
    checks[name] = plan.scope[name] ? { result: record?.result, seconds: record?.seconds, toolchain: record?.toolchain } : { result: 'skipped' };
  }
  const shards = {};
  const finished = [];
  for (const task of plan.tasks.filter(task => goKinds.includes(task.kind))) {
    const record = valid.get(task.id);
    if (record) {
      shards[task.id] = { result: record.result, seconds: record.seconds, toolchain: record.toolchain };
      if (Number.isFinite(record.started)) finished.push(record);
    }
  }
  const complete = plan.scope.componentShards?.every(id => shards[id]?.result === 'success');
  checks.components = { result: plan.scope.components ? complete ? 'success' : 'failure' : 'skipped', shards };
  if (finished.length) {
    checks.components.seconds = (Math.max(...finished.map(record => record.finished)) - Math.min(...finished.map(record => record.started))) / 1000;
    checks.components.toolchain = finished.find(record => record.toolchain)?.toolchain;
  }
  return { checks, failures };
}

export function readResults(directory) {
  if (!existsSync(directory)) return [];
  const read = path => JSON.parse(readFileSync(path, 'utf8'));
  const records = sourceFiles(directory, /\.json$/).map(read);
  const latest = new Map();
  // download-artifact keeps each attempt in its own directory. Select by
  // numeric attempt per shard, retaining jobs that did not need a rerun.
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (!entry.isDirectory()) continue;
    const match = /^foundation-result-([a-z][a-z0-9-]*)-attempt-([1-9][0-9]*)$/.exec(entry.name);
    if (!match || !Number.isSafeInteger(Number(match[2]))) throw new Error('Invalid shard artifact: ' + entry.name);
    const [, id, number] = match;
    const attempt = Number(number);
    if (!latest.has(id) || attempt > latest.get(id).attempt) latest.set(id, { attempt, name: entry.name });
  }
  for (const [id, { name }] of latest) {
    const path = join(directory, name);
    const files = sourceFiles(path, /\.json$/);
    if (files.length !== 1 || files[0] !== join(path, id + '.json')) {
      throw new Error('Missing or duplicate shard result in artifact: ' + name);
    }
    const record = read(files[0]);
    if (record?.id !== id) throw new Error('Shard result does not match artifact: ' + name);
    records.push(record);
  }
  return records;
}

export async function runPipeline(root, plan, { jobs = Math.min(4, availableParallelism()), log = console.error, run = runAsync } = {}) {
  if (!Number.isInteger(jobs) || jobs < 1) throw new Error('Worker count must be a positive integer');
  if (plan.selection.result !== 'success') return aggregate(plan, []);
  validatePlan(plan, root);
  const temporary = mkdtempSync(join(tmpdir(), 'lina-foundation-'));
  const file = join(temporary, 'plan.json');
  const results = join(temporary, 'results');
  writeJSON(file, plan);
  // Keep the existing four-Core/PTY bound on a developer's machine. CI runs
  // each acceptance shard on its own runner; locally other checks can overlap
  // one acceptance process without multiplying its -parallel=4 fan-out.
  const queue = [...plan.tasks.filter(task => task.kind === 'e2e'), ...plan.tasks.filter(task => task.kind !== 'e2e')];
  let acceptanceActive = false;
  let waiters = [];
  const wake = () => { const pending = waiters; waiters = []; for (const resume of pending) resume(); };
  const take = async () => {
    while (queue.length) {
      const index = queue.findIndex(task => task.kind !== 'e2e' || !acceptanceActive);
      if (index >= 0) {
        const [task] = queue.splice(index, 1);
        if (task.kind === 'e2e') acceptanceActive = true;
        return task;
      }
      await new Promise(resolve => waiters.push(resolve));
    }
    return null;
  };
  let upstream = 'success';
  const deadline = Date.now() + componentsTimeoutMs;
  try {
    await Promise.all(Array.from({ length: Math.min(jobs, plan.tasks.length) }, async () => {
      for (;;) {
        const task = await take();
        if (!task) return;
        const remaining = deadline - Date.now();
        if (remaining <= 0) {
          upstream = 'failure';
          log('foundation execution budget exhausted before ' + task.id);
          if (task.kind === 'e2e') acceptanceActive = false;
          wake();
          continue;
        }
        const result = await run(process.execPath, [join(root, 'scripts/foundation.mjs'),
          '--plan', file, '--shard', task.id, '--out', join(results, task.id + '.json')],
        { cwd: root, env: { ...cleanEnv(), GOMAXPROCS: String(Math.max(1, Math.floor(availableParallelism() / jobs))) },
          timeoutMs: Math.min(timeouts[task.kind] + 5000, remaining) });
        if (result.status !== 0) upstream = 'failure';
        // Workers write machine results to files; their stdout is a short progress record.
        if (result.output.trim()) log(result.output.trim());
        if (task.kind === 'e2e') acceptanceActive = false;
        wake();
      }
    }));
    return aggregate(plan, readResults(results), upstream);
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
}
