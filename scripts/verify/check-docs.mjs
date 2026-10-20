#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Check local file targets in tracked Markdown and LICENSE. This is not an anchor or URL checker.
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { stripGitEnv } from '../git.mjs';

stripGitEnv();

const root = execFileSync('git', ['rev-parse', '--show-toplevel'], { encoding: 'utf8' }).trim();
const tracked = new Set(execFileSync('git', ['ls-files', '--cached', '-z'], { cwd: root }).toString('utf8').split('\0').filter(Boolean));
const documents = [...tracked].filter((name) => /\.md$/i.test(name) || name === 'LICENSE').sort();
const failures = [];

function insideRoot(target) {
  const relative = path.relative(root, target);
  return relative === '' || (relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative));
}

function check(source, destination) {
  if (!destination || destination.startsWith('#') || destination.startsWith('//') || /^[a-z][a-z\d+.-]*:/i.test(destination)) return;
  let local;
  try {
    local = decodeURIComponent(destination.split(/[?#]/, 1)[0]);
  } catch {
    failures.push(`${source}: invalid encoded target ${destination}`);
    return;
  }
  if (!local) return; // A same-document query or fragment has no file target.
  const target = path.resolve(local.startsWith('/') ? root : path.dirname(path.join(root, source)), local.replace(/^\//, ''));
  if (!insideRoot(target)) {
    failures.push(`${source}: target escapes repository: ${destination}`);
    return;
  }
  const relative = path.relative(root, target).split(path.sep).join('/');
  const trackedTarget = tracked.has(relative) || [...tracked].some((file) => file.startsWith(`${relative ? `${relative}/` : ''}`));
  if (!trackedTarget || !existsSync(target) || !insideRoot(realpathSync(target))) {
    failures.push(`${source}: broken target ${destination}`);
  }
}

function visibleMarkdown(text) {
  let fence = null;
  return text.split('\n').map((line) => {
    const marker = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (marker) {
      if (!fence) fence = marker[1];
      else if (marker[1][0] === fence[0] && marker[1].length >= fence.length && /^\s*$/.test(line.slice(marker[0].length))) fence = null;
      return '';
    }
    if (fence || /^(?: {4}|\t)/.test(line)) return '';
    return line.replace(/(`+)(?:[^`]|(?!\1)`)*?\1/g, '');
  }).join('\n');
}

function links(text) {
  const refs = new Map();
  const body = visibleMarkdown(text).replace(/^ {0,3}\[([^\]]+)\]:\s*(<[^>]+>|\S+)(?:\s+(?:"[^"]*"|'[^']*'|\([^)]*\)))?\s*$/gm, (_, label, url) => {
    refs.set(label.trim().replace(/\s+/g, ' ').toLowerCase(), url.replace(/^<|>$/g, ''));
    return '';
  });
  const destinations = [];
  const remaining = body.replace(/!?\[[^\]\n]*\]\(\s*(<[^>]*>|(?:\\.|[^\s)])+)(?:\s+(?:"[^"]*"|'[^']*'|\([^)]*\)))?\s*\)/g, (_, url) => {
    destinations.push(url.replace(/^<|>$/g, ''));
    return ' ';
  });
  remaining.replace(/!?\[([^\]\n]+)\](?:\[([^\]\n]*)\])?/g, (_, label, id) => {
    const url = refs.get((id || label).trim().replace(/\s+/g, ' ').toLowerCase());
    if (url) destinations.push(url);
    return '';
  });
  return destinations;
}

for (const source of documents) {
  if (!existsSync(path.join(root, source))) {
    failures.push(`${source}: tracked document is missing`);
    continue;
  }
  for (const destination of links(readFileSync(path.join(root, source), 'utf8'))) check(source, destination);
}
if (failures.length) {
  console.error(failures.join('\n'));
  process.exitCode = 1;
} else {
  console.log(`Checked ${documents.length} tracked documents: local file targets OK (external URLs and anchors not checked).`);
}
