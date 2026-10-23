// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { checks, failureChecks, headlessTests, validateRecord, checkFile, summarizeHeadless } from './accept-m1.mjs';

const hex = (n, size = 32) => n.toString(16).padStart(size, '0');
const digest = bytes => createHash('sha256').update(bytes).digest('hex');

// Synthetic fixtures exist only in tests. They are not owner acceptance evidence.
function fixture(n = 1, revision = hex(n, 40)) {
  const attachments = new Map();
  const attach = value => {
    const bytes = JSON.stringify(value);
    const hash = digest(bytes);
    attachments.set(hash, bytes);
    return hash;
  };
  const commit = revision, run_id = hex(n);
  const headless = attach({ format: 1, kind: 'm1-headless', commit, run_id, exit_code: 0,
    tests: headlessTests.map(id => ({ id, result: 'pass' })) });
  const observation = attach({ format: 1, kind: 'owner-observation', commit, run_id });
  const attempt = {
    run_id, commit, host: 'linux-amd64', manifest_sha256: attach({ commit, target: 'linux/amd64' }),
    clean_install: true, headless_sha256: headless,
    matrix: ['fresh', 'planned_restart', 'crash_restart', 'restore'].map((step, i) => ({
      step, result: 'pass', epoch: String(i + 1), root_id: hex(n * 10 + (i === 3 ? 1 : 0)),
      ready: true, effects_replayed: 0, evidence_sha256: observation,
    })),
    restore: { generation_id: 'generation-ABCDEFGHIJKLMNOPQRSTUVWXYZ', whole_generation: true,
      forgotten_search_hits: 0, forgotten_summary_hits: 0, forgotten_context_hits: 0 },
    checks: Object.fromEntries(checks.map(id => [id, { result: 'pass', evidence_sha256: headless }])),
    failures: Object.fromEntries(failureChecks.map(id => [id, { result: 'pass', evidence_sha256: headless }])),
    ui_ux: { result: 'pass', evidence_sha256: observation },
    terminals: { confirmed: true, ids: [hex(n * 30)], records: Array.from({ length: 11 }, (_, i) => ({
      terminal_id: hex(n * 30), scenario: `S${i + 1}`, result: 'pass', evidence_sha256: attach({
        format: 1, scenario: `S${i + 1}`, terminal: { name: 'synthetic', version: '1', term: 'xterm', ssh: false },
        libraries: { synthetic: 'v1.0.0' }, bounds: { synthetic: true },
        measured: { source_revision: commit, owner_confirmed_matrix: true }, result: 'pass',
      }),
    })) },
    diagnosis: ['model', 'interrupted', 'storage'].map((error_class, i) => {
      const root = hex(n * 100 + i * 2), leaf = hex(n * 100 + i * 2 + 1);
      const base = { time: '2026-10-23T00:00:00Z', component: 'task', conversation_id: null,
        task_id: hex(n), agent_id: null, epoch: '1', node_id: null };
      const cause_code = ['model_failed', 'task_stopped', 'storage_failed'][i];
      return { result: 'pass', error_class, injection_root: root, query: { task_id: hex(n) },
        citation: { root_event_id: root, event_id: leaf, error_class, cause_code },
        events: [
          { ...base, event_id: root, cause: { code: 'task_started', parent_event_id: null },
            result: 'pending', next_action: 'continue', error_class: null },
          { ...base, event_id: leaf, cause: { code: cause_code, parent_event_id: root },
            result: error_class === 'interrupted' ? 'cancelled' : 'failure', next_action: 'report_failure', error_class },
        ] };
    }),
  };
  return { record: { format: 1, attempts: [attempt] }, attachments,
    readEvidence: hash => attachments.get(hash) };
}

test('a complete synthetic record validates, without mutating input', () => {
  const f = fixture(), before = structuredClone(f.record);
  assert.deepEqual(validateRecord(f.record, f), { accepted: true, attempts: 1, commit: hex(1, 40) });
  assert.deepEqual(f.record, before);
});

test('every phase field, check and failure injection is required', () => {
  const paths = [];
  function walk(value, prefix = []) {
    if (!value || typeof value !== 'object') return;
    if (!Array.isArray(value)) for (const key of Object.keys(value)) paths.push([...prefix, key]);
    for (const [key, child] of Object.entries(value)) walk(child, [...prefix, key]);
  }
  walk(fixture().record);
  for (const path of paths) {
    const f = fixture();
    let parent = f.record;
    for (const part of path.slice(0, -1)) parent = parent[part];
    delete parent[path.at(-1)];
    assert.throws(() => validateRecord(f.record, f), undefined, path.join('.'));
  }
});

const invalidCases = {
  'unknown check': a => { a.checks.extra = a.checks.A1; },
  'personal text': a => { a.notes = 'private conversation'; },
  'unmeasured UI': a => { a.ui_ux.result = 'not_run'; },
  'failed check': a => { a.checks.A4.result = 'fail'; },
  'not fresh': a => { a.clean_install = false; },
  'same epoch': a => { a.matrix[2].epoch = a.matrix[1].epoch; },
  'falling epoch': a => { a.matrix[3].epoch = '1'; },
  'numeric epoch': a => { a.matrix[0].epoch = 1; },
  'overflow epoch': a => { a.matrix[3].epoch = '18446744073709551616'; },
  'missing restart': a => { a.matrix.splice(1, 1); },
  'wrong matrix order': a => { a.matrix.reverse(); },
  'replayed effect': a => { a.matrix[2].effects_replayed = 1; },
  'same restore root': a => { a.matrix[3].root_id = a.matrix[0].root_id; },
  'restart changed root': a => { a.matrix[1].root_id = hex(99); },
  'partial backup': a => { a.restore.whole_generation = false; },
  'restored forgotten content': a => { a.restore.forgotten_context_hits = 1; },
  'unconfirmed terminals': a => { a.terminals.confirmed = false; },
  'missing spike': a => { a.terminals.records.pop(); },
  'duplicate spike': a => { a.terminals.records[10] = a.terminals.records[0]; },
  'unknown terminal': a => { a.terminals.records[0].terminal_id = hex(900); },
  'missing diagnosis': a => { a.diagnosis.pop(); },
  'duplicate diagnosis': a => { a.diagnosis[2] = a.diagnosis[0]; },
  'wrong injection root': a => { a.diagnosis[0].injection_root = hex(900); },
  'missing chain link': a => { a.diagnosis[0].events[1].cause.parent_event_id = hex(900); },
  'cyclic chain': a => { a.diagnosis[0].events[1].cause.parent_event_id = a.diagnosis[0].events[1].event_id; },
  'wrong cited code': a => { a.diagnosis[0].citation.cause_code = 'storage_failed'; },
  'secret event field': a => { a.diagnosis[0].events[1].payload = 'secret'; },
  'free-text event id': a => { a.diagnosis[0].events[0].event_id = '/home/person/private'; },
  'wrong query': a => { a.diagnosis[0].query.task_id = hex(900); },
  'headless receipt as spike': a => { a.terminals.records[0].evidence_sha256 = a.headless_sha256; },
  'wrong build manifest': a => { a.manifest_sha256 = a.headless_sha256; },
};
for (const [name, mutate] of Object.entries(invalidCases)) test(`rejects ${name}`, () => {
  const f = fixture();
  mutate(f.record.attempts[0]);
  assert.throws(() => validateRecord(f.record, f));
});

test('epochs compare exactly beyond the JavaScript safe integer range', () => {
  const f = fixture();
  f.record.attempts[0].matrix.forEach((row, i) => { row.epoch = String(9007199254740992n + BigInt(i)); });
  assert.equal(validateRecord(f.record, f).accepted, true);
});

test('a failed epoch or diagnosis remains in history while the fixing commit reruns all checks', () => {
  const old = fixture(), next = fixture(2);
  old.record.attempts[0].checks.A1.result = 'fail';
  old.record.attempts[0].matrix[2].epoch = '1';
  old.record.attempts[0].diagnosis[0].result = 'fail';
  old.record.attempts[0].diagnosis[0].citation.cause_code = 'storage_failed';
  old.record.attempts.push(next.record.attempts[0]);
  for (const [hash, bytes] of next.attachments) old.attachments.set(hash, bytes);
  assert.equal(validateRecord(old.record, old).accepted, true);
});

test('an early failure retains explicit unrun cells instead of inventing observations', () => {
  const old = fixture(), next = fixture(2), a = old.record.attempts[0];
  a.checks.A1.result = 'fail';
  for (const row of a.matrix.slice(1)) for (const key of Object.keys(row)) {
    if (key !== 'step') row[key] = key === 'result' ? 'not_run' : null;
  }
  a.restore = null;
  for (const d of a.diagnosis) {
    d.result = 'not_run'; d.injection_root = null; d.query = null; d.citation = null; d.events = [];
  }
  old.record.attempts.push(next.record.attempts[0]);
  for (const [hash, bytes] of next.attachments) old.attachments.set(hash, bytes);
  assert.equal(validateRecord(old.record, old).accepted, true);
});

test('a failure requires a complete new attempt on the fixing commit', () => {
  const old = fixture(), next = fixture(2);
  old.record.attempts[0].checks.A4.result = 'fail';
  assert.throws(() => validateRecord(old.record, old));
  old.record.attempts.push(next.record.attempts[0]);
  for (const [hash, bytes] of next.attachments) old.attachments.set(hash, bytes);
  assert.equal(validateRecord(old.record, old).attempts, 2);
  for (const mutate of [
    a => { a.commit = old.record.attempts[0].commit; },
    a => { a.run_id = old.record.attempts[0].run_id; },
    a => { a.matrix[0].root_id = old.record.attempts[0].matrix[0].root_id; },
    a => { delete a.checks.A1; },
    a => { a.headless_sha256 = old.record.attempts[0].headless_sha256; },
  ]) {
    const record = structuredClone(old.record);
    mutate(record.attempts[1]);
    assert.throws(() => validateRecord(record, old));
  }
});

test('missing, changed, stale or failed headless evidence never passes', () => {
  for (const mutate of [
    f => f.attachments.clear(),
    f => f.attachments.set(f.record.attempts[0].headless_sha256, '{}'),
    f => {
      const a = f.record.attempts[0];
      const evidence = JSON.parse(f.attachments.get(a.headless_sha256));
      evidence.tests.pop();
      const bytes = JSON.stringify(evidence), hash = digest(bytes);
      f.attachments.set(hash, bytes);
      a.headless_sha256 = hash;
    },
  ]) {
    const f = fixture(); mutate(f);
    assert.throws(() => validateRecord(f.record, f));
  }
});

test('one terminal measurement cannot satisfy two confirmed terminals', () => {
  const f = fixture(), terminals = f.record.attempts[0].terminals;
  terminals.ids.push(hex(999));
  terminals.records.push(...terminals.records.map(row => ({ ...row, terminal_id: hex(999) })));
  assert.throws(() => validateRecord(f.record, f), /terminal_identity/);
});

test('restore accepts the opaque generation ID emitted by the backup service', () => {
  const f = fixture();
  f.record.attempts[0].restore.generation_id = 'generation-ABCDEFGHIJKLMNOPQRSTUVWXYZ';
  assert.equal(validateRecord(f.record, f).accepted, true);
});

test('headless evidence cannot attest the owner UI or serial restart session', () => {
  for (const part of ['ui_ux', 'matrix']) {
    const f = fixture(), a = f.record.attempts[0];
    if (part === 'ui_ux') a.ui_ux.evidence_sha256 = a.headless_sha256;
    else a.matrix[0].evidence_sha256 = a.headless_sha256;
    assert.throws(() => validateRecord(f.record, f), /owner_observation/);
  }
});

test('a terminal gap is retained unchanged in history but never passes acceptance', () => {
  const old = fixture(), a = old.record.attempts[0], row = a.terminals.records[6];
  const spike = JSON.parse(old.attachments.get(row.evidence_sha256));
  spike.result = row.result = 'gap';
  const bytes = JSON.stringify(spike);
  row.evidence_sha256 = digest(bytes); old.attachments.set(row.evidence_sha256, bytes);
  assert.throws(() => validateRecord(old.record, old));
  const next = fixture(2);
  old.record.attempts.push(next.record.attempts[0]);
  for (const [hash, content] of next.attachments) old.attachments.set(hash, content);
  assert.equal(validateRecord(old.record, old).accepted, true);
});

test('headless receipt requires observed passing tests and successful package exit', () => {
  const events = headlessTests.map(id => ({ Action: 'pass', Package: 'github.com/thisisjun786/lina/'
    + (id.startsWith('TestUnchanged') || id.startsWith('TestConversation') ? 'internal/tui/ui' : 'e2e/m1'), Test: id }));
  events.push({ Action: 'pass', Package: 'github.com/thisisjun786/lina/e2e/m1' });
  events.push({ Action: 'pass', Package: 'github.com/thisisjun786/lina/internal/tui/ui' });
  assert.equal(summarizeHeadless(events, 0, hex(1, 40), hex(1)).tests.length, headlessTests.length);
  for (const action of ['skip', 'fail']) {
    const bad = structuredClone(events); bad[0].Action = action;
    assert.throws(() => summarizeHeadless(bad, 0, hex(1, 40), hex(1)));
  }
  assert.throws(() => summarizeHeadless(events.slice(0, -1), 0, hex(1, 40), hex(1)));
  assert.throws(() => summarizeHeadless(events, 1, hex(1, 40), hex(1)));
});

test('file checker and CLI verify content-addressed attachments and fail closed', t => {
  const dir = mkdtempSync(join(tmpdir(), 'lina-accept-m1-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const git = args => {
    const r = spawnSync('git', args, { cwd: dir, encoding: 'utf8',
      env: { ...process.env, GIT_AUTHOR_NAME: 'Test', GIT_COMMITTER_NAME: 'Test',
        GIT_AUTHOR_EMAIL: 'test@example.invalid', GIT_COMMITTER_EMAIL: 'test@example.invalid' } });
    assert.equal(r.status, 0, r.stderr); return r.stdout.trim();
  };
  git(['init', '-b', 'dev']);
  git(['commit', '--allow-empty', '-m', 'Synthetic fixture']);
  git(['update-ref', 'refs/remotes/origin/dev', 'HEAD']);
  const f = fixture(1, git(['rev-parse', 'HEAD'])), file = join(dir, 'record.json');
  writeFileSync(file, JSON.stringify(f.record));
  for (const [hash, bytes] of f.attachments) writeFileSync(join(dir, `${hash}.json`), bytes);
  let validatedSpikes = 0;
  assert.equal(checkFile(file, { cwd: dir, checkSpike: () => { validatedSpikes++; } }).accepted, true);
  assert.equal(validatedSpikes, 11);
  assert.throws(() => checkFile(file, { cwd: dir, checkSpike: () => { throw new Error('spike_failed'); } }));
  const cli = args => spawnSync(process.execPath, [fileURLToPath(new URL('./accept-m1.mjs', import.meta.url)), ...args], { cwd: dir, encoding: 'utf8' });
  assert.equal(cli([file]).status, 1, 'real spike validator required');
  git(['update-ref', '-d', 'refs/remotes/origin/dev']);
  assert.throws(() => checkFile(file, { cwd: dir, checkSpike: () => {} }));
  writeFileSync(file, '{"private":"do not print this"');
  const bad = cli([file]);
  assert.equal(bad.status, 1);
  assert.doesNotMatch(bad.stderr, /do not print this/);
  writeFileSync(file, JSON.stringify(f.record).replace('"format":1', '"format":2,"format":1'));
  assert.throws(() => checkFile(file, { cwd: dir, checkSpike: () => {} }), /duplicate_json_key/);
  assert.equal(cli([]).status, 1);
});

test('the published schema is a closed versioned record contract', () => {
  const schema = JSON.parse(readFileSync(new URL('../evidence/m1/record.schema.json', import.meta.url)));
  assert.equal(schema.$schema, 'https://json-schema.org/draft/2020-12/schema');
  assert.equal(schema.additionalProperties, false);
  assert.equal(schema.properties.format.const, 1);
});

test('headless command captures actual child outcomes, retains failure and strips text output', t => {
  const dir = mkdtempSync(join(tmpdir(), 'lina-headless-command-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const repo = join(dir, 'repo'), bin = join(dir, 'bin');
  mkdirSync(repo); mkdirSync(bin);
  const env = { ...process.env, PATH: bin + ':' + process.env.PATH,
    GIT_AUTHOR_NAME: 'Test', GIT_COMMITTER_NAME: 'Test', GIT_AUTHOR_EMAIL: 'test@example.invalid', GIT_COMMITTER_EMAIL: 'test@example.invalid' };
  for (const args of [['init', '-b', 'dev'], ['commit', '--allow-empty', '-m', 'Synthetic fixture'], ['update-ref', 'refs/remotes/origin/dev', 'HEAD']]) {
    assert.equal(spawnSync('git', args, { cwd: repo, env }).status, 0);
  }
  const events = headlessTests.map(id => ({ Action: 'pass', Package: 'github.com/thisisjun786/lina/'
    + (id.startsWith('TestUnchanged') || id.startsWith('TestConversation') ? 'internal/tui/ui' : 'e2e/m1'), Test: id }));
  for (const pkg of ['e2e/m1', 'internal/tui/ui']) events.push({ Action: 'pass', Package: `github.com/thisisjun786/lina/${pkg}` });
  events.push({ Action: 'output', Output: 'PRIVATE RAW TEXT' });
  const go = join(bin, 'go');
  writeFileSync(go, '#!/usr/bin/env node\n// SPDX-License-Identifier: Apache-2.0\n'
    + `for (const event of ${JSON.stringify(events)}) console.log(JSON.stringify(event));\nprocess.exit(Number(process.env.SYNTHETIC_EXIT ?? 0));\n`);
  chmodSync(go, 0o755);
  const command = [fileURLToPath(new URL('./accept-m1.mjs', import.meta.url)), '--run-headless', dir];
  for (const exit of [0, 1]) {
    const result = spawnSync(process.execPath, command, { cwd: repo, env: { ...env, SYNTHETIC_EXIT: String(exit) }, encoding: 'utf8' });
    assert.equal(result.status, exit, result.stderr);
    const receipt = JSON.parse(result.stdout);
    assert.equal(receipt.passed, exit === 0);
    const bytes = readFileSync(join(dir, `${receipt.headless_sha256}.json`), 'utf8');
    assert.equal(digest(bytes), receipt.headless_sha256);
    assert.doesNotMatch(bytes, /PRIVATE RAW TEXT/);
    assert.equal(JSON.parse(bytes).exit_code, exit);
  }
  writeFileSync(join(repo, 'dirty'), 'uncommitted');
  assert.equal(spawnSync(process.execPath, command, { cwd: repo, env }).status, 1);
});
