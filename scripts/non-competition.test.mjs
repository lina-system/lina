// SPDX-License-Identifier: Apache-2.0
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { test } from 'node:test';
import { checkFile, columns, validateEvidence } from './non-competition.mjs';

// Synthetic observations exercise the format; they are never acceptance records.
function fixture() {
  const records = [1, 2].flatMap(tier => columns.map(column => {
    const started = `2026-01-0${tier}T10:00:00Z`;
    const ended = `2026-01-0${tier}T10:10:00Z`;
    return {
      id: `tier-${tier}-${column}`, tier, column,
      host: { installation_id: 'install-1', os: 'linux', version: 'fixture Linux', install_mode: 'device-os' },
      session: { os_user: 'operator-1', human_gui_session: 'seat-human', gui_session: null,
        human_profiles: ['profile-human'], profile: tier === 2 ? 'profile-lina' : null,
        task_id: 'task-1', grant_id: 'grant-1', seat_lease: null },
      hardware: { cpu: 'fixture CPU', memory_bytes: 8 * 1024 ** 3, devices: ['keyboard', 'pointer'] },
      artifacts: Object.fromEntries(['core', 'node', 'app', 'os', 'claude_adapter'].map(name => [name,
        ['core', 'os'].includes(name)
          ? { version: 'fixture-version', manifest_sha256: 'a'.repeat(64) }
          : { absent_reason: 'not part of this fixture install' }])),
      workload: { human: 'edit a synthetic document while typing', lina: 'run a granted task',
        life: 'world clock and daily events', agents_total: 3, agents_background: 2, background_runs: 4,
        processes: [{ id: 'core-1', role: 'core', activity: 'conversation and agent background work' }],
        normal_load: ['core-1'], full_background_load: true, human_working: true, lina_working: true, claude_account: false },
      observation: { method: 'manual_linux', observer: 'operator-1', started_at: started, ended_at: ended },
      traces: Object.fromEntries(['input', 'focus', 'clipboard', 'files'].map(name => [name, {
        path: `${name}.json`, sha256: 'b'.repeat(64), task_id: 'task-1', grant_id: 'grant-1',
        started_at: started, ended_at: ended,
      }])),
      bounds: { declared_at: '2025-12-31T10:00:00Z', input_latency_ms: 50, yield_ms: 100, recovery_ms: 500,
        processes: [{ id: 'core-1', cpu_percent: 25, resident_bytes: 256 * 1024 ** 2 }] },
      measured: { focus_steals: 0, injected_inputs: 0, clipboard_reads: 0, clipboard_writes: 0,
        out_of_scope_accesses: 0, lost_edits: 0, input_latency_ms: 10, yield_ms: 20, recovery_ms: 100,
        human_processes_killed: 0, processes: [{ id: 'core-1', cpu_percent: 10, resident_bytes: 128 * 1024 ** 2 }],
        lock_exercised: true, locked_session_blocked: true, stale_unlock_blocked: true,
        sleep_exercised: true, expired_resume_blocked: true, stale_resume_blocked: true,
        observed_disconnect_exercised: true, new_work_blocked: true, cancellation_requested: true,
        silent_disconnect_exercised: true, expiry_blocked: true, human_session_untouched: true,
        stale_write_exercised: true, stale_write_rejected: true },
      result: 'pass',
    };
  }));
  return { schema_version: 1, records };
}

test('complete tiers require all eight cells and count agents and background runs', () => {
  const result = validateEvidence(fixture());
  assert.equal(result.valid, true, result.errors.join('\n'));
  assert.equal(result.passed, true);
  assert.deepEqual(result.tiers.map(tier => tier.status), ['pass', 'pass']);
});

test('every contract field is required, including explicit none', () => {
  for (const path of [
    'host.installation_id', 'host.version', 'host.install_mode', 'session.os_user', 'session.gui_session', 'session.profile',
    'session.grant_id', 'session.seat_lease', 'hardware.cpu', 'hardware.memory_bytes', 'hardware.devices',
    'artifacts.core', 'artifacts.node', 'artifacts.app', 'artifacts.os', 'artifacts.claude_adapter',
    'workload.human', 'workload.lina', 'workload.life', 'workload.agents_total',
    'workload.agents_background', 'workload.background_runs', 'workload.processes', 'workload.normal_load',
    'traces.input', 'traces.focus', 'traces.clipboard', 'traces.files', 'bounds.declared_at',
    'bounds.processes', 'measured.processes', 'observation.observer', 'result',
  ]) {
    const data = fixture();
    const keys = path.split('.');
    if (keys.length === 1) delete data.records[0][keys[0]];
    else delete data.records[0][keys[0]][keys[1]];
    assert.equal(validateEvidence(data).valid, false, path);
  }
});

test('any nonzero violation fails a cell and stops the next tier', () => {
  for (const [column, field] of [
    ['focus', 'focus_steals'], ['input', 'injected_inputs'], ['clipboard', 'clipboard_reads'],
    ['clipboard', 'clipboard_writes'], ['file_scope', 'out_of_scope_accesses'], ['file_scope', 'lost_edits'],
    ['resources', 'human_processes_killed'],
  ]) {
    const data = fixture();
    const record = data.records.find(r => r.column === column);
    record.measured[field] = 1;
    assert.equal(validateEvidence(data).valid, false, 'dishonest pass: ' + field);
    record.result = 'fail';
    const stopped = validateEvidence(data);
    assert.equal(stopped.passed, false);
    assert.equal(stopped.valid, false, 'higher tier was attempted: ' + field);
    data.records = data.records.filter(r => r.tier === 1);
    const failure = validateEvidence(data);
    assert.equal(failure.valid, true, failure.errors.join('\n'));
    assert.deepEqual(failure.tiers.map(t => t.status), ['fail', 'unverified']);
  }
});

test('missing, duplicate, foreign row and premature tier records cannot pass', () => {
  for (const mutate of [
    d => d.records.pop(),
    d => d.records.push(structuredClone(d.records[0])),
    d => d.records[0].host.os = 'darwin',
    d => d.records[0].host.install_mode = 'vm',
    d => d.records[0].host.install_mode = 'desktop',
    d => d.records[8].host.installation_id = 'different-install',
    d => d.records[8].observation.started_at = '2026-01-01T10:01:00Z',
    d => d.records[0].tier = 3,
  ]) {
    const data = fixture(); mutate(data);
    assert.equal(validateEvidence(data).passed, false);
  }
  assert.equal(validateEvidence({ schema_version: 1, records: [] }).passed, false);
  const onlySecond = fixture(); onlySecond.records = onlySecond.records.filter(r => r.tier === 2);
  assert.equal(validateEvidence(onlySecond).valid, false);
});

test('invalid evidence cannot authorize advancing from an otherwise complete tier', () => {
  for (const tier of [1, 2]) {
    for (const defect of ['duplicate failure', 'malformed extra record', 'unknown tier']) {
      for (const prepend of [false, true]) {
        const data = fixture();
        const extra = structuredClone(data.records.find(r => r.tier === tier));
        if (defect === 'duplicate failure') {
          extra.measured.focus_steals = 1;
          extra.result = 'fail';
        } else if (defect === 'malformed extra record') {
          delete extra.workload;
        } else {
          extra.tier = 3;
        }
        data.records[prepend ? 'unshift' : 'push'](extra);
        const result = validateEvidence(data);
        assert.equal(result.valid, false);
        assert.equal(result.passed, false);
        assert.notEqual(result.tiers[tier - 1].status, 'pass', `${tier}: ${defect}, prepend=${prepend}`);
        assert.notEqual(result.tiers[1].status, 'pass');
        if (tier === 2 && defect !== 'unknown tier') assert.equal(result.tiers[0].status, 'pass');
      }
    }
  }
});

test('observations, complete load, trace identity/coverage and advance bounds are mandatory', () => {
  for (const mutate of [
    r => r.observation.method = 'headless',
    r => r.observation.ended_at = r.observation.started_at,
    r => r.workload.human_working = false,
    r => r.workload.full_background_load = false,
    r => r.workload.agents_background = 4,
    r => r.workload.background_runs = -1,
    r => r.workload.agents_total = 1.5,
    r => r.workload.claude_account = true,
    r => r.workload.normal_load.push('missing-worker'),
    r => r.bounds.declared_at = r.observation.ended_at,
    r => r.bounds.input_latency_ms = -1,
    r => r.bounds.processes = [],
    r => r.measured.processes = [],
    r => r.artifacts.core = { absent_reason: 'missing' },
    r => r.artifacts.core.manifest_sha256 = 'unknown',
    r => r.artifacts.moirai_worker = { version: 'fixture-version' },
    r => r.traces.input.grant_id = 'other-grant',
    r => r.traces.focus.started_at = r.observation.ended_at,
    r => r.traces.files.path = '../private.json',
    r => r.traces.clipboard.sha256 = '',
    r => r.session.gui_session = 'seat-human',
    r => r.session.seat_lease = 'lease-1',
  ]) {
    const data = fixture(); mutate(data.records[0]);
    assert.equal(validateEvidence(data).valid, false);
  }
  const data = fixture(); data.records[8].session.profile = 'profile-human';
  assert.equal(validateEvidence(data).valid, false);
});

test('resource limits produce measured deltas; lifecycle assertions must be exercised', () => {
  const result = validateEvidence(fixture());
  assert.equal(result.cells.find(c => c.column === 'resources').deltas.input_latency_ms, -40);
  for (const [column, mutate] of [
    ['resources', r => r.measured.input_latency_ms = 51],
    ['resources', r => r.measured.yield_ms = 101],
    ['resources', r => r.measured.recovery_ms = 501],
    ['resources', r => r.measured.processes[0].cpu_percent = 26],
    ['resources', r => r.measured.processes[0].resident_bytes = 257 * 1024 ** 2],
    ['lock_unlock', r => r.measured.stale_unlock_blocked = false],
    ['sleep_resume', r => r.measured.expired_resume_blocked = false],
    ['disconnect', r => r.measured.expiry_blocked = false],
    ['file_scope', r => r.measured.stale_write_rejected = false],
  ]) {
    const data = fixture(); mutate(data.records.find(r => r.column === column));
    assert.equal(validateEvidence(data).passed, false, column);
  }
});

test('malformed input returns diagnostics instead of throwing or passing', () => {
  for (const data of [null, [], {}, { schema_version: 2, records: [] }, { schema_version: 1, records: [null, {}] }]) {
    assert.equal(validateEvidence(data).valid, false);
  }
});

test('CLI checks attached bytes, rejects missing/tampered traces and never passes an empty ladder', t => {
  const root = mkdtempSync(join(tmpdir(), 'lina-non-competition-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const data = fixture();
  for (const kind of ['input', 'focus', 'clipboard', 'files']) {
    const bytes = JSON.stringify({ synthetic: true, kind });
    writeFileSync(join(root, `${kind}.json`), bytes);
    for (const r of data.records) r.traces[kind].sha256 = createHash('sha256').update(bytes).digest('hex');
  }
  const recordPath = join(root, 'records.json');
  const run = () => spawnSync(process.execPath, [resolve('scripts/non-competition.mjs'), recordPath], { encoding: 'utf8' });
  writeFileSync(recordPath, JSON.stringify(data));
  const passing = run();
  assert.equal(passing.status, 0, passing.stderr + passing.stdout);
  assert.equal(JSON.parse(passing.stdout).passed, true);
  writeFileSync(join(root, 'input.json'), 'tampered');
  assert.equal(run().status, 1);
  rmSync(join(root, 'input.json'));
  assert.equal(run().status, 1);
  const outsideRoot = mkdtempSync(join(tmpdir(), 'lina-non-competition-outside-'));
  t.after(() => rmSync(outsideRoot, { recursive: true, force: true }));
  const outsideBytes = JSON.stringify({ synthetic: true, kind: 'input' });
  writeFileSync(join(outsideRoot, 'input.json'), outsideBytes);
  symlinkSync(join(outsideRoot, 'input.json'), join(root, 'input.json'));
  assert.equal(run().status, 1, 'symlink escapes attachment directory');
  writeFileSync(recordPath, JSON.stringify({ schema_version: 1, records: [] }));
  assert.equal(run().status, 1);
  writeFileSync(recordPath, '{broken');
  assert.equal(run().status, 1);
});

test('committed Linux measurement sets remain well-formed even while incomplete', () => {
  const directory = resolve('evidence/non-competition/records');
  for (const file of readdirSync(directory).filter(name => name.endsWith('.json'))) {
    const result = checkFile(join(directory, file));
    assert.equal(result.valid, true, `${file}: ${result.errors.join('\n')}`);
    // Format validation accepts an honest failure or an unmeasured ladder.
    // Acceptance uses the CLI's separate passed field and nonzero exit status.
  }
});
