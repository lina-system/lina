#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { isAbsolute, join } from 'node:path';
import { parseArgs } from 'node:util';
import { pathToFileURL } from 'node:url';
import { git, gitOut, toplevel } from '../git.mjs';
import { readPlan, readResults, sealPlan } from './pipeline.mjs';

const marker = /<!-- foundation-qa:(\{[^\n]+\}) -->/;
const sha = value => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value);
const key = state => JSON.stringify([state.branch, state.package, state.test]);
const hour = 60 * 60 * 1000;
const statusMarker = '<!-- foundation-qa-status -->';

// Read one known member without extracting paths from the downloaded archive.
export async function readArtifactJSON(artifact, name, { api, repository }) {
  const directory = mkdtempSync(join(tmpdir(), 'lina-qa-artifact-'));
  try {
    const archive = join(directory, 'artifact.zip');
    writeFileSync(archive, await api('GET', `/repos/${repository}/actions/artifacts/${artifact.id}/zip`));
    return JSON.parse(execFileSync('unzip', ['-p', archive, name], {
      encoding: 'utf8', timeout: 30000, maxBuffer: 64 * 1024 * 1024,
    }));
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
}

const failureMarkLimit = 16;

// A delayed failure can land between heads that are not yet within 48 hours
// of each other. Keep the newest heads and apply the window when the pass arrives.
function pruneFailureMarks(marks) {
  return marks.slice(-failureMarkLimit);
}

function withinFlakyWindow(times) {
  let earliest = Infinity;
  let latest = -Infinity;
  for (const time of times) {
    if (!Number.isFinite(time)) return false;
    if (time < earliest) earliest = time;
    if (time > latest) latest = time;
  }
  return latest - earliest <= 48 * hour;
}

// Retain failure heads so a pass reported later can still sit between them.
async function rememberFailure(marks, head, at, isAncestor) {
  const next = marks.map(mark => ({ ...mark }));
  const known = next.find(mark => mark.head === head);
  if (known) known.at = Math.max(known.at, at);
  else {
    let index = 0;
    while (index < next.length && await isAncestor(next[index].head, head)) index++;
    next.splice(index, 0, { head, at });
  }
  return pruneFailureMarks(next);
}

function failureMarksOf(previous) {
  if (!Array.isArray(previous?.failureMarks)) return [];
  return previous.failureMarks.flatMap(mark => sha(mark?.head) && Number.isFinite(mark.at)
    ? [{ head: mark.head, at: mark.at }] : []);
}

async function* actionsHistory({ plan, repository, branch, runId, api, readArtifact, isAncestor, wanted }) {
  const route = `/repos/${repository}/actions`;
  const current = await api('GET', `${route}/runs/${runId}`);
  if (!Number.isFinite(Date.parse(current.created_at))) throw new Error('Missing QA run creation time');
  // Reruns retain their original creation time. Include completed descendants
  // even when their reporters finished before this older run's reporter.
  for (let page = 1;; page++) {
    const query = new URLSearchParams({ branch, status: 'completed', created: '>=' + current.created_at,
      per_page: '100', page: String(page) });
    const { workflow_runs: runs } = await api('GET', `${route}/workflows/ci.yml/runs?${query}`);
    for (const run of runs) {
      if (run.id === runId || run.head_branch !== branch || run.status !== 'completed'
          || !['push', 'schedule', 'workflow_dispatch'].includes(run.event)
          || !sha(run.head_sha) || run.head_sha === plan.head || !await isAncestor(plan.head, run.head_sha)) continue;
      const artifacts = [];
      for (let artifactPage = 1;; artifactPage++) {
        const batch = await api('GET', `${route}/runs/${run.id}/artifacts?per_page=100&page=${artifactPage}`);
        artifacts.push(...batch.artifacts);
        if (batch.artifacts.length < 100) break;
      }
      const plans = artifacts.filter(item => item.name === 'foundation-plan');
      if (plans.length > 1) throw new Error('Duplicate historical QA plans');
      if (!plans.length || plans[0].expired) continue;
      const historical = await readArtifact(plans[0], 'foundation-plan.json', { api, repository });
      if (historical.schema !== 2 || historical.head !== run.head_sha || historical.scope?.head !== historical.head
          || historical.scope?.event !== run.event || !sha(historical.tree) || historical.digest !== sealPlan(historical).digest) {
        throw new Error('Invalid historical QA plan');
      }
      const records = [];
      for (const task of historical.tasks.filter(task => task.kind === 'e2e')) {
        if (!task.components.some(component => component.packages.some(pkg => task.tests.some(test => wanted.has(key({ branch, package: pkg, test })))))) continue;
        const candidates = artifacts.flatMap(artifact => {
          const match = /^foundation-result-([a-z][a-z0-9-]*)-attempt-([1-9][0-9]*)$/.exec(artifact.name);
          return match && match[1] === task.id ? [{ artifact, attempt: Number(match[2]) }] : [];
        }).sort((a, b) => b.attempt - a.attempt);
        if (!candidates.length || candidates[0].artifact.expired) continue;
        if (candidates[0].attempt === candidates[1]?.attempt) throw new Error('Duplicate historical QA shard');
        records.push(await readArtifact(candidates[0].artifact, task.id + '.json', { api, repository }));
      }
      yield { plan: historical, records };
    }
    if (runs.length < 100) break;
  }
}

function validateLocalRun(localRun, plan) {
  const text = value => typeof value === 'string' && value.length > 0 && value.length <= 4096
    && !/[\x00-\x1f\x7f`<>]/.test(value);
  if (!localRun || localRun.head !== plan.head || localRun.started !== plan.started
      || !text(localRun.machine) || !text(localRun.logPath) || !isAbsolute(localRun.logPath)
      || plan.schema !== 2 || !sha(plan.head) || !sha(plan.tree) || plan.scope?.head !== plan.head
      || plan.digest !== sealPlan(plan).digest) throw new Error('Invalid local QA run identity or plan');
}

async function* localRuns(history, { plan, repository, branch, isAncestor }) {
  for (const run of history) {
    if (run.repository !== repository || run.branch !== branch) continue;
    validateLocalRun(run.localRun, run.plan);
    if (run.plan.head === plan.head || !await isAncestor(plan.head, run.plan.head)) continue;
    yield run;
  }
}

async function descendantHistory(options) {
  const { plan, observations, branch, isAncestor, localRun, localHistory } = options;
  const wanted = new Set(observations.filter(item => item.result === 'fail').map(item => key({ branch, ...item })));
  const histories = new Map();
  const source = localRun ? localRuns(localHistory, options) : actionsHistory({ ...options, wanted });
  for await (const { plan: historical, records } of source) {
    for (const observation of collectObservations(historical, records)) {
      const id = key({ branch, ...observation });
      if (!wanted.has(id) || !['pass', 'fail', 'unknown'].includes(observation.result)) continue;
      const history = histories.get(id) ?? [];
      const previous = history.find(item => item.head === historical.head);
      const started = Number.isFinite(historical.started) ? historical.started : 0;
      if (previous) {
        if (observation.result === 'fail' || previous.result === 'unknown') {
          previous.result = observation.result;
          previous.started = started;
        }
      } else {
        let index = 0;
        while (index < history.length && await isAncestor(history[index].head, historical.head)) index++;
        history.splice(index, 0, { head: historical.head, result: observation.result, started });
      }
      histories.set(id, history);
    }
  }
  return histories;
}

export function collectObservations(plan, records) {
  if (plan.selection?.result !== 'success' || plan.scope?.reason !== 'manual-full'
      || !['push', 'schedule', 'workflow_dispatch'].includes(plan.scope.event)) {
    throw new Error('QA requires a full post-merge plan');
  }
  const seen = new Set();
  const observations = new Map();
  for (const record of records) {
    const task = plan.tasks.find(task => task.id === record.id);
    if (!task || seen.has(record.id)) throw new Error('Unknown or duplicate QA result: ' + record.id);
    seen.add(record.id);
    if (task.kind !== 'e2e') continue;
    if (record.schema !== 1 || record.kind !== 'e2e' || record.head !== plan.head
        || record.tree !== plan.tree || record.digest !== plan.digest
        || !['success', 'failure'].includes(record.result) || !Array.isArray(record.tests)) {
      throw new Error('Invalid QA result: ' + record.id);
    }
    const packages = task.components.flatMap(component => component.packages);
    const found = new Set();
    for (const observation of record.tests) {
      if (!packages.includes(observation.package) || !task.tests.includes(observation.test)
          || !['pass', 'fail', 'skip', 'unknown'].includes(observation.result) || typeof observation.output !== 'string') {
        throw new Error('QA result is not a selected test');
      }
      const id = observation.package + ':' + observation.test;
      if (observations.has(id)) throw new Error('Duplicate QA test result: ' + id);
      found.add(observation.test);
      // Recovery is per test; an unrelated failing scenario must not keep it open.
      observations.set(id, observation);
    }
    if (record.result === 'success' && task.tests.some(name => !found.has(name))) {
      throw new Error('Missing selected QA test result');
    }
  }
  return [...observations.values()];
}

function stateOf(issue) {
  const match = marker.exec(issue.body ?? '');
  if (!match) return null;
  let state;
  try { state = JSON.parse(match[1]); } catch { return null; }
  return [1, 2].includes(state.schema) && typeof state.branch === 'string' && typeof state.package === 'string'
    && typeof state.test === 'string' && sha(state.firstBase) && sha(state.firstHead) && sha(state.lastHead)
    && (['fail', 'pass'].includes(state.result) || state.schema === 2 && state.result === 'unknown') ? state : null;
}

function bodyOf(state, observation, repository, runReference, requiredPasses) {
  const range = `${state.firstBase}..${state.firstHead}`;
  const lines = observation.output.replaceAll('```', '` ` `').replace(/[\x00-\x08\x0b-\x1f\x7f]/g, '').slice(-16000);
  return `<!-- foundation-qa:${JSON.stringify(state)} -->\n`
    + `Automated post-merge e2e observation on \`${state.branch}\`.\n\n`
    + `- Test: \`${state.package}:${state.test}\`\n`
    + `- First failing commit range: [${range}](https://github.com/${repository}/compare/${range})\n`
    + `- Latest tested head: \`${state.lastHead}\`\n`
    + `- Reported head: \`${state.reportHead}\` (${state.reportResult})\n`
    + `- Result: ${{ fail: 'failed', pass: 'passed on a later head', unknown: 'unfinished; recovery not established' }[state.result]}\n`
    + `- Consecutive passing heads: ${state.passes}/${requiredPasses}\n`
    + (state.flaky ? '- Flaky: failure, pass and failure observed within 48 hours\n' : '')
    + runReference + '\n'
    + (observation.result === 'fail' ? `Failure lines:\n\n\`\`\`text\n${lines}\n\`\`\`\n`
      : state.result === 'unknown' ? 'The unfinished test resets the recovery streak without establishing a failure.\n'
        : state.passes >= requiredPasses ? 'The selected test has met the recovery threshold.\n'
        : 'The selected test passed; awaiting consecutive passing heads before closing.\n');
}

async function advanceState(previous, observation, { plan, before, identity, requiredPasses, isAncestor }) {
  const sameHead = previous?.lastHead === plan.head;
  const newer = !previous || sameHead || await isAncestor(previous.lastHead, plan.head);
  if (previous && !newer && !await isAncestor(plan.head, previous.lastHead)) return null;
  if (sameHead && observation.result === 'unknown') return null;
  const state = { schema: 2, ...identity,
    firstBase: previous?.firstBase ?? before, firstHead: previous?.firstHead ?? plan.head,
    lastHead: newer ? plan.head : previous.lastHead,
    result: newer ? observation.result : previous.result,
    reportHead: plan.head, reportResult: observation.result,
    barrierHead: previous?.barrierHead ?? previous?.lastHead ?? plan.head,
    recoveryHeads: [...(previous?.recoveryHeads ?? [])],
    lastFailureHead: previous?.lastFailureHead ?? (previous?.result === 'fail' ? previous.lastHead : null),
    lastFailureAt: previous?.lastFailureAt ?? 0, firstPassHead: previous?.firstPassHead ?? null,
    passedSinceFailure: previous?.passedSinceFailure ?? false,
    flaky: previous?.flaky ?? false, lastReopenedAt: previous?.lastReopenedAt ?? 0,
    failureMarks: failureMarksOf(previous) };
  if (observation.result === 'pass') {
    if (sameHead && previous.result === 'fail') state.result = 'fail';
    if (state.lastFailureHead === plan.head) state.passedSinceFailure = true;
    if (state.lastFailureHead && state.lastFailureHead !== plan.head
        && await isAncestor(state.lastFailureHead, plan.head)
        && (!state.firstPassHead || await isAncestor(plan.head, state.firstPassHead))) state.firstPassHead = plan.head;
    if (plan.head !== state.barrierHead && await isAncestor(state.barrierHead, plan.head)
        && !state.recoveryHeads.includes(plan.head)) {
      let index = 0;
      while (index < state.recoveryHeads.length && await isAncestor(state.recoveryHeads[index], plan.head)) index++;
      state.recoveryHeads.splice(index, 0, plan.head);
      state.recoveryHeads = state.recoveryHeads.slice(-requiredPasses);
    }
    // Commit order, not arrival order. A late pass can sit between two failures
    // that are not neighbors: an isolated mark between them must not hide the pair.
    for (let earlierIndex = 0; earlierIndex < state.failureMarks.length - 1 && !state.flaky; earlierIndex++) {
      for (let laterIndex = earlierIndex + 1; laterIndex < state.failureMarks.length && !state.flaky; laterIndex++) {
        const earlier = state.failureMarks[earlierIndex];
        const later = state.failureMarks[laterIndex];
        if (plan.head === later.head || !withinFlakyWindow([earlier.at, later.at, plan.started])) continue;
        if (await isAncestor(earlier.head, plan.head) && await isAncestor(plan.head, later.head)) state.flaky = true;
      }
    }
  } else {
    if (observation.result === 'fail' && Number.isFinite(plan.started)) {
      state.failureMarks = await rememberFailure(state.failureMarks, plan.head, plan.started, isAncestor);
    }
    if (observation.result === 'fail' && (!state.lastFailureHead
        || await isAncestor(state.lastFailureHead, plan.head))) {
      const passed = state.lastFailureHead === plan.head ? state.passedSinceFailure
        : state.firstPassHead && await isAncestor(state.firstPassHead, plan.head);
      if (passed && state.lastFailureAt && plan.started >= state.lastFailureAt
          && plan.started - state.lastFailureAt <= 48 * hour) state.flaky = true;
      state.lastFailureHead = plan.head;
      state.lastFailureAt = Math.max(state.lastFailureAt, plan.started);
      state.firstPassHead = null;
      state.passedSinceFailure = false;
    }
    if (await isAncestor(state.barrierHead, plan.head)) {
      state.barrierHead = plan.head;
      const remaining = [];
      for (const head of state.recoveryHeads) {
        if (head !== plan.head && await isAncestor(plan.head, head)) remaining.push(head);
      }
      state.recoveryHeads = remaining;
    }
    if (observation.result === 'fail' && state.lastFailureHead === plan.head) {
      state.firstPassHead = state.recoveryHeads[0] ?? null;
    }
  }
  state.passes = state.recoveryHeads.length;
  return state;
}

async function legacyState(issue, state, { api, route }) {
  const events = [];
  for (let page = 1;; page++) {
    const batch = await api('GET', `${route}/${issue.number}/timeline?per_page=100&page=${page}`);
    events.push(...batch.filter(event => event.actor?.login === 'github-actions[bot]'
      && ['closed', 'reopened'].includes(event.event) && Number.isFinite(Date.parse(event.created_at))));
    if (batch.length < 100) break;
  }
  events.sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at));
  let lastFailureAt = Date.parse(issue.created_at) || 0;
  let passed = false;
  let flaky = false;
  let lastReopenedAt = 0;
  for (const event of events) {
    const time = Date.parse(event.created_at);
    if (event.event === 'closed') passed = true;
    else {
      if (passed && lastFailureAt && time - lastFailureAt <= 48 * hour) flaky = true;
      lastFailureAt = time;
      lastReopenedAt = time;
      passed = false;
    }
  }
  // Legacy markers did not retain per-head pass counts. Never invent a streak.
  return { ...state, schema: 2, passes: 0, flaky,
    barrierHead: state.lastHead, recoveryHeads: [], firstPassHead: null,
    lastFailureHead: state.result === 'fail' ? state.lastHead : null,
    lastFailureAt: state.result === 'fail' ? Date.parse(issue.updated_at) || lastFailureAt : lastFailureAt,
    passedSinceFailure: state.result === 'pass', lastReopenedAt, failureMarks: [] };
}

async function updateStatus({ issue, state, issueState, requiredPasses, runReference, route, api }) {
  const body = `${statusMarker}\nPost-merge QA: **${issueState}**.\n\n`
    + `- Latest tested head: \`${state.lastHead}\` (${state.result})\n`
    + (state.reportHead ? `- Reported head: \`${state.reportHead}\` (${state.reportResult})\n` : '')
    + `- Consecutive passing heads: ${state.passes}/${requiredPasses}\n`
    + (state.flaky ? '- Flaky: failure, pass and failure within 48 hours.\n' : '')
    + (issueState === 'closed' && state.reportResult === 'fail' && state.passes < requiredPasses
      ? '- Reopening deferred: this finding was already reopened within the last 24 hours.\n' : '')
    + runReference;
  for (let page = 1;; page++) {
    const comments = await api('GET', `${route}/${issue.number}/comments?per_page=100&page=${page}`);
    const comment = comments.find(item => item.body?.startsWith(statusMarker));
    if (comment) {
      if (comment.body !== body) await api('PATCH', `${route}/comments/${comment.id}`, { body });
      return;
    }
    if (comments.length < 100) break;
  }
  await api('POST', `${route}/${issue.number}/comments`, { body });
}

export async function reconcileQA({ plan, records, repository, milestone, branch = 'dev', before,
  runUrl, runId, localRun, localHistory = [], api, isAncestor, readArtifact = readArtifactJSON, requiredPasses = 3, now = Date.now }) {
  const observations = collectObservations(plan, records);
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository) || !sha(plan.head) || !sha(before)
      || !Number.isInteger(milestone) || milestone < 1
      || (!localRun && (!Number.isSafeInteger(runId) || runId < 1
      || !/^https:\/\/github\.com\/[\w.-]+\/[\w.-]+\/actions\/runs\/[0-9]+(?:\/attempts\/[0-9]+)?$/.test(runUrl)))) {
    throw new Error('QA needs a repository, milestone, commit range and workflow run');
  }
  if (!Number.isSafeInteger(requiredPasses) || requiredPasses < 1 || !Number.isFinite(plan.started)) {
    throw new Error('QA needs a positive recovery pass count and plan start time');
  }
  if (localRun) {
    validateLocalRun(localRun, plan);
    if (!Array.isArray(localHistory) || runId !== undefined || runUrl !== undefined) {
      throw new Error('Local QA needs stored history and cannot also identify an Actions run');
    }
  }
  const runReference = localRun
    ? `- Local run: \`${localRun.machine}\` at ${new Date(localRun.started).toISOString()}\n- Logs: \`${localRun.logPath}\`\n`
    : `- [Workflow run](${runUrl})\n`;
  const time = now();
  if (!Number.isFinite(time)) throw new Error('Invalid QA clock');
  const route = `/repos/${repository}/issues`;
  const issues = [];
  for (let page = 1;; page++) {
    const batch = await api('GET', `${route}?state=all&labels=qa&per_page=100&page=${page}`);
    issues.push(...batch.filter(issue => !issue.pull_request));
    if (batch.length < 100) break;
  }
  const existing = new Map();
  for (const issue of issues) {
    const state = stateOf(issue);
    if (!state) continue;
    const id = key(state);
    if (existing.has(id)) throw new Error('Duplicate automated QA issues for ' + id);
    existing.set(id, { issue, state });
  }
  const changes = [];
  let flakyLabel;
  const ensureFlaky = async labels => {
    if (labels.includes('flaky')) return;
    // Creating an existing label is harmless; githubAPI exposes the status
    // so only GitHub's already-exists response can be ignored.
    flakyLabel ??= api('POST', `/repos/${repository}/labels`, { name: 'flaky', color: 'FBCA04',
      description: 'QA test failed, passed and failed again within 48 hours' }).catch(error => {
      if (error.status !== 422 || !Array.isArray(error.errors)
          || !error.errors.some(item => item.code === 'already_exists')) throw error;
    });
    await flakyLabel;
    labels.push('flaky');
  };
  const updateIssue = async (previous, state, body, issueState, reference = runReference) => {
    const { issue } = previous;
    const labels = (issue.labels ?? []).map(label => typeof label === 'string' ? label : label.name);
    if (state.flaky) await ensureFlaky(labels);
    // Write the reusable comment first: a retry after a failed issue update
    // finds and edits it instead of posting another notification.
    await updateStatus({ issue, state, issueState, requiredPasses, runReference: reference, route, api });
    await api('PATCH', `${route}/${issue.number}`, { body,
      ...(state.flaky ? { labels } : {}),
      ...(issueState !== issue.state ? { state: issueState,
        ...(issueState === 'closed' ? { state_reason: 'completed' } : { state_reason: 'reopened' }) } : {}),
      ...(state.result === 'fail' ? { milestone } : {}) });
    previous.state = state;
    Object.assign(issue, { body, state: issueState });
  };
  // Upgrade every existing finding on this branch, even if its test did not
  // run today. This reconciles previously flapping findings such as #1228 on
  // the first post-merge run without manufacturing fresh test evidence.
  for (const previous of existing.values()) {
    if (previous.state.branch !== branch || previous.state.schema !== 1) continue;
    const state = await legacyState(previous.issue, previous.state, { api, route });
    const body = previous.issue.body.replace(marker, `<!-- foundation-qa:${JSON.stringify(state)} -->`);
    const url = /\[Workflow run\]\(([^)]+)\)/.exec(body)?.[1];
    await updateIssue(previous, state, body, previous.issue.state, url ? `- [Workflow run](${url})\n` : runReference);
    changes.push({ number: previous.issue.number, action: 'migrated', test: state.test });
  }
  let descendantHistories;
  for (const observation of observations) {
    if (!['fail', 'pass', 'unknown'].includes(observation.result)) continue;
    const identity = { branch, package: observation.package, test: observation.test };
    const previous = existing.get(key(identity));
    if (observation.result !== 'fail' && !previous) continue;
    let seeded;
    if (observation.result === 'fail' && !previous) {
      descendantHistories ??= await descendantHistory({ plan, observations, repository, branch, runId, api, readArtifact, isAncestor, localRun, localHistory });
      seeded = await advanceState(null, observation, { plan, before, identity, requiredPasses, isAncestor });
      for (const item of descendantHistories.get(key(identity)) ?? []) {
        const next = await advanceState(seeded, { ...observation, result: item.result }, {
          plan: { head: item.head, started: item.started }, before, identity, requiredPasses, isAncestor,
        });
        if (next) seeded = next;
      }
      // A complete descendant streak never opens an already recovered finding.
      if (seeded.passes >= requiredPasses) continue;
      seeded.reportHead = plan.head;
      seeded.reportResult = observation.result;
    }
    const state = seeded ?? await advanceState(previous?.state, observation, { plan, before, identity, requiredPasses, isAncestor });
    if (!state) continue;
    let issueState = previous?.issue.state ?? 'open';
    if (state.passes >= requiredPasses) issueState = 'closed';
    if (observation.result === 'fail' && issueState === 'closed'
        && state.passes < requiredPasses
        && (!state.lastReopenedAt || time - state.lastReopenedAt >= 24 * hour)) {
      issueState = 'open';
      state.lastReopenedAt = time;
    }
    const body = bodyOf(state, observation, repository, runReference, requiredPasses);
    if (!previous) {
      const labels = ['qa'];
      if (state.flaky) await ensureFlaky(labels);
      const issue = await api('POST', route, { title: `qa: e2e ${observation.test} (${branch})`.slice(0, 240),
        body, labels, milestone });
      existing.set(key(identity), { issue, state });
      changes.push({ number: issue.number, action: 'opened', test: observation.test });
    } else {
      const action = issueState === previous.issue.state ? 'updated' : issueState === 'closed' ? 'closed' : 'reopened';
      await updateIssue(previous, state, body, issueState);
      changes.push({ number: previous.issue.number, action, test: observation.test });
    }
  }
  return changes;
}

export async function githubAPI(method, path, body) {
  const token = process.env.GH_TOKEN;
  if (!token) throw new Error('GH_TOKEN is required for QA reporting');
  const response = await fetch('https://api.github.com' + path, { method,
    headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28', 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(30000) });
  if (!response.ok) {
    const details = await response.json().catch(() => ({}));
    throw Object.assign(new Error(`GitHub ${method} ${path}: HTTP ${response.status}`),
      { status: response.status, errors: details.errors });
  }
  return path.endsWith('/zip') ? Buffer.from(await response.arrayBuffer()) : response.json();
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const { values } = parseArgs({ options: { plan: { type: 'string' }, results: { type: 'string' } } });
    if (!values.plan || !values.results) throw new Error('--plan and --results are required');
    const root = toplevel(process.cwd());
    const plan = readPlan(values.plan, root);
    const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8'));
    const branch = process.env.GITHUB_REF_NAME;
    if (branch !== 'dev' && !branch?.startsWith('qa/e2e-')) throw new Error('QA reporting requires dev or an explicit qa/e2e- validation branch');
    const before = sha(event.before) && event.before !== '0'.repeat(40) ? event.before : gitOut(root, ['rev-parse', 'HEAD^1']);
    const changes = await reconcileQA({ plan, records: readResults(values.results), repository: process.env.GITHUB_REPOSITORY,
      milestone: Number(process.env.QA_MILESTONE), branch, before,
      requiredPasses: Number(process.env.QA_RECOVERY_PASSES || 3),
      runId: Number(process.env.GITHUB_RUN_ID),
      runUrl: `https://github.com/${process.env.GITHUB_REPOSITORY}/actions/runs/${process.env.GITHUB_RUN_ID}/attempts/${process.env.GITHUB_RUN_ATTEMPT}`,
      api: githubAPI, isAncestor: async (a, b) => {
        const result = git(root, ['merge-base', '--is-ancestor', a, b]);
        if (![0, 1].includes(result.status)) throw new Error('Cannot compare QA commit ancestry');
        return result.status === 0;
      } });
    console.log(JSON.stringify({ head: plan.head, changes }));
  } catch (error) {
    console.error('post-merge QA: ' + error.message);
    process.exitCode = 1;
  }
}
