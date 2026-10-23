#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
import { readFileSync } from 'node:fs';
import { parseArgs } from 'node:util';
import { pathToFileURL } from 'node:url';
import { git, gitOut, toplevel } from '../git.mjs';
import { readPlan, readResults } from './pipeline.mjs';

const marker = /<!-- foundation-qa:(\{[^\n]+\}) -->/;
const sha = value => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value);
const key = state => JSON.stringify([state.branch, state.package, state.test]);

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
  return state.schema === 1 && typeof state.branch === 'string' && typeof state.package === 'string'
    && typeof state.test === 'string' && sha(state.firstBase) && sha(state.firstHead) && sha(state.lastHead)
    && ['fail', 'pass'].includes(state.result) ? state : null;
}

function bodyOf(state, observation, repository, runUrl) {
  const range = `${state.firstBase}..${state.firstHead}`;
  const lines = observation.output.replaceAll('```', '` ` `').replace(/[\x00-\x08\x0b-\x1f\x7f]/g, '').slice(-16000);
  return `<!-- foundation-qa:${JSON.stringify(state)} -->\n`
    + `Automated post-merge e2e observation on \`${state.branch}\`.\n\n`
    + `- Test: \`${state.package}:${state.test}\`\n`
    + `- First failing commit range: [${range}](https://github.com/${repository}/compare/${range})\n`
    + `- Latest tested head: \`${state.lastHead}\`\n`
    + `- Result: ${state.result === 'fail' ? 'failed' : 'passed on a later head'}\n`
    + `- [Workflow run](${runUrl})\n\n`
    + (state.result === 'fail' ? `Failure lines:\n\n\`\`\`text\n${lines}\n\`\`\`\n` : 'The selected test passed; this QA finding is resolved.\n');
}

export async function reconcileQA({ plan, records, repository, milestone, branch = 'dev', before,
  runUrl, api, isAncestor }) {
  const observations = collectObservations(plan, records);
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository) || !sha(plan.head) || !sha(before)
      || !Number.isInteger(milestone) || milestone < 1
      || !/^https:\/\/github\.com\/[\w.-]+\/[\w.-]+\/actions\/runs\/[0-9]+(?:\/attempts\/[0-9]+)?$/.test(runUrl)) {
    throw new Error('QA needs a repository, milestone, commit range and workflow run');
  }
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
  for (const observation of observations) {
    if (!['fail', 'pass'].includes(observation.result)) continue;
    const identity = { branch, package: observation.package, test: observation.test };
    const previous = existing.get(key(identity));
    if (previous) {
      if (previous.state.lastHead === plan.head) continue; // A later head must prove recovery.
      if (!await isAncestor(previous.state.lastHead, plan.head)) continue; // Ignore out-of-order runs.
    }
    if (observation.result === 'pass' && !previous) continue;
    const state = { schema: 1, ...identity,
      firstBase: previous?.state.firstBase ?? before, firstHead: previous?.state.firstHead ?? plan.head,
      lastHead: plan.head, result: observation.result };
    const body = bodyOf(state, observation, repository, runUrl);
    if (!previous) {
      const issue = await api('POST', route, { title: `qa: e2e ${observation.test} (${branch})`.slice(0, 240),
        body, labels: ['qa'], milestone });
      existing.set(key(identity), { issue, state });
      changes.push({ number: issue.number, action: 'opened', test: observation.test });
    } else {
      await api('PATCH', `${route}/${previous.issue.number}`, { body,
        state: observation.result === 'pass' ? 'closed' : 'open',
        ...(observation.result === 'pass' ? { state_reason: 'completed' } : { milestone }) });
      changes.push({ number: previous.issue.number, action: observation.result === 'pass' ? 'closed' : 'updated', test: observation.test });
    }
  }
  return changes;
}

async function githubAPI(method, path, body) {
  const token = process.env.GH_TOKEN;
  if (!token) throw new Error('GH_TOKEN is required for QA reporting');
  const response = await fetch('https://api.github.com' + path, { method,
    headers: { Authorization: `Bearer ${token}`, Accept: 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28', 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(30000) });
  if (!response.ok) throw new Error(`GitHub ${method} ${path}: HTTP ${response.status}`);
  return response.json();
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
