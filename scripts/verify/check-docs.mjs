#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Check local file targets and Markdown heading anchors in tracked Markdown and LICENSE.
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync, realpathSync } from 'node:fs';
import path from 'node:path';
import { stripGitEnv } from '../git.mjs';

stripGitEnv();

const root = execFileSync('git', ['rev-parse', '--show-toplevel'], { encoding: 'utf8' }).trim();
const tracked = new Set(execFileSync('git', ['ls-files', '--cached', '-z'], { cwd: root }).toString('utf8').split('\0').filter(Boolean));
const documents = [...tracked].filter((name) => /\.md$/i.test(name) || name === 'LICENSE').sort();
const failures = [];
const anchorCache = new Map();

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
    return;
  }
  const hash = destination.indexOf('#');
  if (hash < 0 || !/\.md$/i.test(relative) || !tracked.has(relative)) return;
  let fragment;
  try {
    fragment = decodeURIComponent(destination.slice(hash + 1));
  } catch {
    failures.push(`${source}: invalid encoded target ${destination}`);
    return;
  }
  if (!fragment) return;
  if (!anchorCache.has(target)) anchorCache.set(target, headingAnchors(readFileSync(target, 'utf8')));
  if (!anchorCache.get(target).has(fragment)) {
    failures.push(`${source}: broken anchor ${destination}`);
  }
}

function visibleMarkdown(text, keepInlineCode = false) {
  let fence = null;
  return text.split('\n').map((line) => {
    const marker = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (marker) {
      if (!fence) fence = marker[1];
      else if (marker[1][0] === fence[0] && marker[1].length >= fence.length && /^\s*$/.test(line.slice(marker[0].length))) fence = null;
      return '';
    }
    if (fence || /^(?: {4}|\t)/.test(line)) return '';
    return keepInlineCode ? line : line.replace(/(`+)(?:[^`]|(?!\1)`)*?\1/g, '');
  }).join('\n');
}

function headingText(text) {
  // Code contributes literal text, but must not be interpreted as emphasis or links.
  const code = [];
  const plain = text.replace(/(`+)(.*?)\1/g, (_, marker, content) => {
    code.push(content);
    return `\0${code.length - 1}\0`;
  }).replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/!?\[([^\]]*)\]\[[^\]]*\]/g, '$1')
    .replace(/<[^>]*>/g, '')
    .replace(/[*~]/g, '')
    .replace(/(?<![\p{L}\p{N}])(_{1,2})(?=\S)(.+?)\1(?![\p{L}\p{N}])/gu, '$2')
    .replace(/&(#x[\da-f]+|#\d+|amp|lt|gt|quot|apos|nbsp);/gi, (entity, value) => {
      if (!value.startsWith('#')) return { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'", nbsp: '\u00a0' }[value.toLowerCase()];
      const point = value[1].toLowerCase() === 'x' ? parseInt(value.slice(2), 16) : Number(value.slice(1));
      return point > 0 && point <= 0x10ffff ? String.fromCodePoint(point) : '\ufffd';
    });
  return plain.replace(/\0(\d+)\0/g, (_, index) => code[Number(index)]);
}

function headingAnchors(text) {
  const anchors = new Set();
  const lines = visibleMarkdown(text.replace(/<!--[\s\S]*?-->/g, ''), true).split('\n');
  let paragraph = [];
  for (const line of lines) {
    const atx = /^ {0,3}#{1,6}(?:[\t ]+(.*)|$)/.exec(line);
    let title;
    if (atx) title = (atx[1] ?? '').replace(/[\t ]+#+[\t ]*$/, '').trim();
    else if (paragraph.length && /^ {0,3}(?:=+|-+)[\t ]*$/.test(line)) title = paragraph.join('\n');
    else {
      if (!line.trim() || /^ {0,3}(?:(?:-[\t ]*){3,}|(?:\*[\t ]*){3,}|(?:_[\t ]*){3,})$/.test(line)) paragraph = [];
      else paragraph.push(line.trim());
      continue;
    }
    paragraph = [];
    // GitHub lowercases heading text, removes punctuation and replaces spaces.
    const base = headingText(title).toLowerCase().replace(/[^\p{L}\p{M}\p{N}_\- ]/gu, '').replace(/ /g, '-');
    let anchor = base;
    for (let suffix = 1; anchors.has(anchor); suffix++) anchor = `${base}-${suffix}`;
    anchors.add(anchor);
  }
  return anchors;
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
  console.log(`Checked ${documents.length} tracked documents: local file targets and Markdown heading anchors OK (external URLs not checked).`);
}
