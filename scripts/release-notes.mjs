#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Generate release notes from GitHub PR merge commits; this does not publish a release.
import { writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { gitOut, resolveCommit, run, stripGitEnv, toplevel } from './git.mjs';

stripGitEnv();

const groups = [
  ['breaking', 'Breaking changes'],
  ['feature', 'Features'],
  ['bug', 'Bug fixes'],
  ['decision', 'Decisions'],
  ['other', 'Other'],
];
const types = ['feature', 'bug', 'decision'];

function pullRequest(root, number) {
  let result;
  try {
    result = run('gh', ['pr', 'view', number, '--json', 'title,labels'], { cwd: root });
  } catch (error) {
    throw new Error(`Cannot read pull request #${number} with gh: ${error.message}`);
  }
  if (result.status !== 0) {
    throw new Error(`Cannot read pull request #${number} with gh: ${result.stderr.trim() || result.stdout.trim()}`);
  }
  let pr;
  try {
    pr = JSON.parse(result.stdout);
  } catch {
    throw new Error(`Invalid JSON from gh for pull request #${number}.`);
  }
  if (!pr || typeof pr.title !== 'string' || !pr.title.trim() || /[\r\n]/.test(pr.title)
      || !Array.isArray(pr.labels) || pr.labels.some((label) => typeof label?.name !== 'string')) {
    throw new Error(`Invalid title or labels from gh for pull request #${number}.`);
  }
  return pr;
}

/** Render each PR reachable from `to` but not `from`, or all history when `from` is root. */
export function releaseNotes({ cwd = process.cwd(), from, to }) {
  if (!from || !to) throw new Error('Provide --from <tag or root> and --to <sha>.');
  const root = toplevel(cwd);
  const head = resolveCommit(root, to);
  if (!head) throw new Error(`Unknown --to revision: ${to}`);
  const base = from === 'root' ? null : resolveCommit(root, from);
  if (from !== 'root' && !base) throw new Error(`Unknown --from revision: ${from}`);
  const subjects = gitOut(root, ['log', '--merges', '--reverse', '--topo-order', '--format=%s',
    base ? `${base}..${head}` : head, '--']);
  const numbers = [...new Set(subjects.split('\n').flatMap((subject) => {
    const match = /^Merge pull request #([1-9]\d*)(?:\s|$)/.exec(subject);
    return match ? [match[1]] : [];
  }))];
  if (!numbers.length) throw new Error('No pull request merges in the requested range.');

  const entries = new Map(groups.map(([key]) => [key, []]));
  for (const number of numbers) {
    const { title, labels } = pullRequest(root, number);
    const group = /^[^:]+!:/.test(title) ? 'breaking'
      : types.find((type) => labels.some((label) => label.name === type)) ?? 'other';
    entries.get(group).push(`- ${title} (#${number})`);
  }
  return groups.filter(([key]) => entries.get(key).length)
    .map(([key, heading]) => `## ${heading}\n\n${entries.get(key).join('\n')}`)
    .join('\n\n') + '\n';
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const { values } = parseArgs({ options: {
      from: { type: 'string' }, to: { type: 'string' }, out: { type: 'string' },
    } });
    if (!values.out) throw new Error('Provide --out <file> for the release notes.');
    const notes = releaseNotes({ from: values.from, to: values.to });
    writeFileSync(values.out, notes);
  } catch (error) {
    console.error(`release-notes: ${error.message}`);
    process.exitCode = 1;
  }
}
