// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { delimiter, join } from 'node:path';
import test from 'node:test';
import { run } from './git.mjs';
import { developmentRepository, installBinaryBuildFixture, sandbox, scriptsDir } from './test-fixture.mjs';

function setup(t) {
  const sb = sandbox(t);
  const { work, remote } = developmentRepository(sb);
  const first = sb.g(work, 'rev-parse', 'HEAD');
  const bin = join(sb.dir, 'bin');
  mkdirSync(bin);
  const dataFile = join(sb.dir, 'prs.json');
  const logFile = join(sb.dir, 'gh.jsonl');
  writeFileSync(logFile, '');
  writeFileSync(join(bin, 'gh'), `#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
const { appendFileSync, readFileSync } = require('node:fs');
const args = process.argv.slice(2);
appendFileSync(process.env.NOTES_GH_LOG, JSON.stringify(args) + '\\n');
if (args.join(' ') === 'repo view --json isPrivate --jq .isPrivate') {
  console.log('true');
} else if (args.length === 5 && args[0] === 'pr' && args[1] === 'view'
    && args[3] === '--json' && args[4] === 'title,labels') {
  const pr = JSON.parse(readFileSync(process.env.NOTES_GH_DATA, 'utf8'))[args[2]];
  if (!pr || pr.error) {
    console.error(pr?.error || 'unexpected pull request');
    process.exit(1);
  }
  console.log(pr.raw ?? JSON.stringify(pr));
} else {
  console.error('unexpected gh call: ' + JSON.stringify(args));
  process.exit(90);
}
`);
  chmodSync(join(bin, 'gh'), 0o755);
  const env = { ...sb.env, PATH: `${bin}${delimiter}${process.env.PATH}`,
    NOTES_GH_DATA: dataFile, NOTES_GH_LOG: logFile };
  const prs = {};
  let serial = 0;
  const merge = (number, title, labels = [], message) => {
    const branch = `change-${++serial}`;
    sb.g(work, 'checkout', '-q', '-b', branch);
    sb.commit(work, 'chore: fixture change', { [`docs/design/${branch}.md`]: '# Change\n' });
    sb.g(work, 'checkout', '-q', 'dev');
    sb.g(work, 'merge', '-q', '--no-ff', '-m', message ?? `Merge pull request #${number} from fixture/${branch}`, branch);
    prs[number] = { title, labels: labels.map((name) => ({ name })) };
    return sb.g(work, 'rev-parse', 'HEAD');
  };
  const out = join(sb.dir, 'release notes.md');
  const call = (from = 'root', to = sb.g(work, 'rev-parse', 'HEAD'), options = {}) => {
    writeFileSync(dataFile, JSON.stringify(prs));
    return run(process.execPath, [join(scriptsDir, 'release-notes.mjs'),
      '--from', from, '--to', to, '--out', out], { cwd: work, env, ...options });
  };
  const calls = () => readFileSync(logFile, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse);
  return { sb, work, remote, first, env, prs, merge, out, call, calls };
}

test('a tag range lists each merged PR once using its GitHub title and type label', (t) => {
  const s = setup(t);
  s.merge(1, 'feat: already released', ['feature']);
  s.sb.g(s.work, 'tag', '-a', 'previous', '-m', 'Previous release');
  s.merge(2, 'feat: save work', ['feature']);
  s.merge(3, 'fix: restore work', ['bug']);
  s.merge(4, 'docs: select storage', ['decision']);
  s.merge(5, 'chore: refresh tools');
  s.merge(6, 'docs: usage guide', ['documentation']);
  s.merge(2, 'feat: save work', ['feature']);
  // A matching subject on an ordinary commit is not a PR merge.
  s.sb.commit(s.work, 'Merge pull request #98 from fixture/plain', { 'docs/design/plain.md': '# Plain\n' });
  s.merge(99, 'chore: custom merge', [], 'Merge local maintenance');
  const to = s.sb.g(s.work, 'rev-parse', 'HEAD');
  s.merge(100, 'feat: future release', ['feature']);
  const result = s.call('previous', to);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(s.out, 'utf8'), [
    '## Features', '', '- feat: save work (#2)', '',
    '## Bug fixes', '', '- fix: restore work (#3)', '',
    '## Decisions', '', '- docs: select storage (#4)', '',
    '## Other', '', '- chore: refresh tools (#5)', '- docs: usage guide (#6)', '',
  ].join('\n'));
  assert.deepEqual(s.calls().map((args) => args[2]), ['2', '3', '4', '5', '6']);
});

test('root includes all history and breaking titles take precedence over labels', (t) => {
  const s = setup(t);
  s.merge(1, 'feat: first feature', ['feature']);
  s.merge(2, 'fix(core)!: change the state format', ['bug']);
  s.merge(3, 'feat!: replace the protocol');
  s.merge(4, 'docs: explain !: syntax', ['documentation']);
  s.merge(5, 'feat: shared types', ['decision', 'bug', 'feature']);
  const result = s.call();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(s.out, 'utf8'), [
    '## Breaking changes', '', '- fix(core)!: change the state format (#2)',
    '- feat!: replace the protocol (#3)', '',
    '## Features', '', '- feat: first feature (#1)', '- feat: shared types (#5)', '',
    '## Other', '', '- docs: explain !: syntax (#4)', '',
  ].join('\n'));
});

test('empty ranges and ranges without PR merges fail without replacing notes or calling gh', (t) => {
  const s = setup(t);
  writeFileSync(s.out, 'Keep these notes.\n');
  for (const from of [s.first, 'root']) {
    const result = s.call(from, s.first);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /no pull request merges/i);
    assert.equal(readFileSync(s.out, 'utf8'), 'Keep these notes.\n');
  }
  assert.deepEqual(s.calls(), []);
});

test('unknown revisions and missing or unknown options fail before fetching PRs', (t) => {
  const s = setup(t);
  s.merge(1, 'feat: work', ['feature']);
  for (const [from, to] of [['missing-tag', 'HEAD'], ['root', 'missing-commit']]) {
    const result = s.call(from, to);
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /unknown.*revision/i);
  }
  for (const args of [[], ['--from', 'root'], ['--unknown']]) {
    const result = run(process.execPath, [join(scriptsDir, 'release-notes.mjs'), ...args], { cwd: s.work, env: s.env });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /--from|--to|--out|unknown option/i);
  }
  assert.equal(existsSync(s.out), false);
  assert.deepEqual(s.calls(), []);
});

test('a failed gh lookup or invalid metadata leaves existing notes intact', (t) => {
  const s = setup(t);
  s.merge(1, 'feat: first', ['feature']);
  s.merge(2, 'fix: second', ['bug']);
  writeFileSync(s.out, 'Keep these notes.\n');
  for (const response of [
    { error: 'lookup failed' }, { raw: '{broken' },
    { title: '', labels: [] }, { title: 'fix: second', labels: null },
  ]) {
    s.prs[2] = response;
    const result = s.call();
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /#2/);
    assert.equal(readFileSync(s.out, 'utf8'), 'Keep these notes.\n');
  }
});

test('the CLI uses its working repository despite inherited GIT_* variables', (t) => {
  const s = setup(t);
  const to = s.merge(1, 'feat: correct repository', ['feature']);
  const decoy = join(s.sb.dir, 'decoy');
  s.sb.g(s.sb.dir, 'init', '-q', decoy);
  s.sb.commit(decoy, 'chore: decoy', { 'README.md': '# Decoy\n' });
  const result = s.call('root', to, { env: { ...s.env, GIT_DIR: join(decoy, '.git'), GIT_WORK_TREE: decoy } });
  assert.equal(result.status, 0, result.stderr);
  assert.match(readFileSync(s.out, 'utf8'), /feat: correct repository \(#1\)/);
});

test('generated notes are accepted by the release CLI with a local remote and stub gh', { skip: process.platform !== 'linux' }, (t) => {
  const s = setup(t);
  s.sb.g(s.work, 'push', '-q', 'origin', 'dev:refs/heads/main');
  installBinaryBuildFixture(s.sb, s.work);
  const commit = s.merge(1, 'feat: release notes', ['feature']);
  s.sb.g(s.work, 'push', '-q', 'origin', 'dev');
  const generated = s.call();
  assert.equal(generated.status, 0, generated.stderr);
  const tag = `v0.0.1-alpha.${new Date().toISOString().slice(0, 10).replaceAll('-', '')}.1`;
  const result = run(process.execPath, [join(scriptsDir, 'release.mjs'),
    '--commit', commit, '--tag', tag, '--notes-file', s.out, '--out', join(s.sb.dir, 'artifacts')],
  { cwd: s.work, env: s.env });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(JSON.parse(result.stdout).mode, 'dry-run');
  assert.equal(s.sb.g(s.remote, 'rev-parse', 'refs/heads/main'), s.first);
  assert.equal(s.sb.g(s.remote, 'tag', '--list'), '');
});
