#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Validates reported measurements. It neither runs the human ladder nor attests
// that an observer's counters describe reality; trace review remains necessary.
import { createHash } from 'node:crypto';
import { readFileSync, realpathSync, statSync } from 'node:fs';
import { dirname, isAbsolute, relative, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';

export const columns = Object.freeze([
  'focus', 'input', 'clipboard', 'file_scope', 'resources', 'lock_unlock', 'sleep_resume', 'disconnect',
]);
const traceKinds = ['input', 'focus', 'clipboard', 'files'];
const zeroCounters = ['focus_steals', 'injected_inputs', 'clipboard_reads', 'clipboard_writes',
  'out_of_scope_accesses', 'lost_edits', 'human_processes_killed'];
const limits = ['input_latency_ms', 'yield_ms', 'recovery_ms'];
const lifecycle = {
  file_scope: ['stale_write_exercised', 'stale_write_rejected'],
  lock_unlock: ['lock_exercised', 'locked_session_blocked', 'stale_unlock_blocked'],
  sleep_resume: ['sleep_exercised', 'expired_resume_blocked', 'stale_resume_blocked'],
  disconnect: ['observed_disconnect_exercised', 'new_work_blocked', 'cancellation_requested',
    'silent_disconnect_exercised', 'expiry_blocked', 'human_session_untouched'],
};

function requireValue(condition, message) { if (!condition) throw new Error(message); }
function object(value, field) {
  requireValue(value !== null && typeof value === 'object' && !Array.isArray(value), `${field}: expected object`);
  return value;
}
function text(value, field) {
  requireValue(typeof value === 'string' && value.trim().length > 0, `${field}: expected nonempty text`);
  return value;
}
function number(value, field, integer = false) {
  requireValue(Number.isFinite(value) && value >= 0 && (!integer || Number.isSafeInteger(value)), `${field}: expected nonnegative ${integer ? 'integer' : 'number'}`);
  return value;
}
function array(value, field, nonempty = false) {
  requireValue(Array.isArray(value) && (!nonempty || value.length > 0), `${field}: expected ${nonempty ? 'nonempty ' : ''}array`);
  return value;
}
function timestamp(value, field) {
  text(value, field);
  requireValue(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/.test(value) && Number.isFinite(Date.parse(value))
    && new Date(value).toISOString() === value.replace(/Z$/, value.includes('.') ? 'Z' : '.000Z'), `${field}: expected UTC timestamp`);
  return Date.parse(value);
}
function digest(value, field) {
  requireValue(typeof value === 'string' && /^[a-f0-9]{64}$/.test(value), `${field}: expected SHA-256`);
}
function unique(values, field) {
  requireValue(new Set(values).size === values.length, `${field}: duplicate value`);
  return values;
}
function ids(values, field) {
  return unique(array(values, field, true).map(v => text(v.id, `${field}.id`)), field);
}
function sameIDs(expected, actual, field) {
  requireValue(expected.length === actual.length && expected.every(id => actual.includes(id)), `${field}: process inventory mismatch`);
}
function localPath(value) {
  text(value, 'trace.path');
  requireValue(!isAbsolute(value) && !value.includes('\\') && value.split('/').every(part => part && part !== '.' && part !== '..')
    && !value.includes(':') && !value.includes('\0'), 'trace.path: expected relative attachment path');
}

function validateCell(r) {
  object(r, 'record');
  text(r.id, 'id');
  requireValue(r.tier === 1 || r.tier === 2, 'tier: only tiers 1 and 2 are supported');
  requireValue(columns.includes(r.column), 'column: unknown matrix column');
  object(r.host, 'host');
  text(r.host.installation_id, 'host.installation_id');
  requireValue(r.host.os === 'linux', 'host.os: Linux measurements required');
  text(r.host.version, 'host.version');
  requireValue(['device-os', 'desktop'].includes(r.host.install_mode), 'host.install_mode: expected shared-machine row');
  const s = object(r.session, 'session');
  for (const name of ['os_user', 'human_gui_session', 'task_id', 'grant_id']) text(s[name], `session.${name}`);
  const profiles = unique(array(s.human_profiles, 'session.human_profiles').map(p => text(p, 'human profile')), 'human profiles');
  requireValue(s.gui_session === null && s.seat_lease === null, 'tiers 1/2: GUI session and seat lease must be explicit null');
  if (r.tier === 1) requireValue(s.profile === null, 'tier 1: profile must be explicit null');
  else {
    text(s.profile, 'session.profile');
    requireValue(!profiles.includes(s.profile), 'tier 2: LINA profile must differ from all human profiles');
  }
  const hardware = object(r.hardware, 'hardware');
  text(hardware.cpu, 'hardware.cpu');
  requireValue(number(hardware.memory_bytes, 'hardware.memory_bytes', true) > 0, 'hardware.memory_bytes: must be positive');
  array(hardware.devices, 'hardware.devices').forEach(d => text(d, 'hardware device'));
  const artifacts = object(r.artifacts, 'artifacts');
  const required = r.host.install_mode === 'device-os' ? ['core', 'os'] : ['node', 'app'];
  for (const name of new Set(['core', 'node', 'app', 'os', 'claude_adapter', ...Object.keys(artifacts)])) {
    const artifact = object(artifacts[name], `artifacts.${name}`);
    if ('absent_reason' in artifact) {
      text(artifact.absent_reason, `${name}.absent_reason`);
      requireValue(!required.includes(name) && !('version' in artifact) && !('manifest_sha256' in artifact), `${name}: required or conflicting artifact status`);
    } else {
      text(artifact.version, `${name}.version`);
      digest(artifact.manifest_sha256, `${name}.manifest_sha256`);
    }
  }
  const w = object(r.workload, 'workload');
  requireValue(typeof w.claude_account === 'boolean', 'workload.claude_account: expected boolean');
  if (w.claude_account) requireValue(!('absent_reason' in artifacts.claude_adapter), 'Claude account requires adapter version and manifest digest');
  for (const name of ['human', 'lina', 'life']) text(w[name], `workload.${name}`);
  for (const name of ['agents_total', 'agents_background', 'background_runs']) number(w[name], `workload.${name}`, true);
  requireValue(w.agents_total > 0 && w.agents_background <= w.agents_total, 'workload: inconsistent agent counts');
  for (const name of ['full_background_load', 'human_working', 'lina_working']) requireValue(w[name] === true, `workload.${name}: concurrent full load required`);
  const processIDs = ids(w.processes, 'workload.processes');
  w.processes.forEach(p => { text(p.role, 'process.role'); text(p.activity, 'process.activity'); });
  const normalIDs = unique(array(w.normal_load, 'workload.normal_load', true).map(id => text(id, 'normal_load id')), 'normal_load');
  sameIDs(processIDs, normalIDs, 'normal_load');
  const observation = object(r.observation, 'observation');
  requireValue(observation.method === 'manual_linux', 'observation.method: requires a human concurrent Linux measurement');
  text(observation.observer, 'observation.observer');
  const started = timestamp(observation.started_at, 'observation.started_at');
  const ended = timestamp(observation.ended_at, 'observation.ended_at');
  requireValue(ended > started, 'observation: duration must be positive');
  const traces = object(r.traces, 'traces');
  for (const kind of traceKinds) {
    const trace = object(traces[kind], `traces.${kind}`);
    localPath(trace.path); digest(trace.sha256, `${kind}.sha256`);
    requireValue(trace.task_id === s.task_id && trace.grant_id === s.grant_id, `${kind}: task/grant mismatch`);
    requireValue(timestamp(trace.started_at, `${kind}.started_at`) <= started
      && timestamp(trace.ended_at, `${kind}.ended_at`) >= ended, `${kind}: trace does not cover the whole session`);
  }
  const bounds = object(r.bounds, 'bounds');
  requireValue(timestamp(bounds.declared_at, 'bounds.declared_at') < started, 'bounds must be declared before observation');
  const measured = object(r.measured, 'measured');
  const deltas = {};
  for (const name of limits) deltas[name] = number(measured[name], `measured.${name}`) - number(bounds[name], `bounds.${name}`);
  sameIDs(processIDs, ids(bounds.processes, 'bounds.processes'), 'bounds');
  sameIDs(processIDs, ids(measured.processes, 'measured.processes'), 'measured');
  deltas.processes = measured.processes.map(p => {
    const bound = bounds.processes.find(b => b.id === p.id);
    return { id: p.id, cpu_percent: number(p.cpu_percent, 'measured.cpu_percent') - number(bound.cpu_percent, 'bounds.cpu_percent'),
      resident_bytes: number(p.resident_bytes, 'measured.resident_bytes', true) - number(bound.resident_bytes, 'bounds.resident_bytes', true) };
  });
  const criteria = Object.fromEntries(zeroCounters.map(name => [name, number(measured[name], `measured.${name}`, true) === 0]));
  if (r.column === 'resources') {
    for (const name of limits) criteria[name] = deltas[name] <= 0;
    for (const p of deltas.processes) criteria[`process:${p.id}`] = p.cpu_percent <= 0 && p.resident_bytes <= 0;
  }
  for (const name of lifecycle[r.column] ?? []) {
    requireValue(typeof measured[name] === 'boolean', `measured.${name}: expected boolean`);
    criteria[name] = measured[name];
  }
  const passed = Object.values(criteria).every(Boolean);
  requireValue(r.result === (passed ? 'pass' : 'fail'), 'result: disagrees with measured pass criteria');
  return { id: r.id, tier: r.tier, column: r.column, passed, criteria, deltas, started, ended };
}

// Record shape/consistency and acceptance are separate: a correctly recorded
// failed cell is valid evidence, but cannot pass or authorize the next tier.
// readTrace is required by the CLI; callers without it only validate records.
export function validateEvidence(data, { readTrace } = {}) {
  const errors = [], cells = [];
  const tiers = [1, 2].map(tier => ({ tier, status: 'unverified', missing: [...columns] }));
  try {
    object(data, 'evidence');
    requireValue(data.schema_version === 1, 'schema_version: expected 1');
    array(data.records, 'records');
  } catch (error) { return { valid: false, passed: false, errors: [error.message], cells, tiers }; }
  const seen = new Set(), seenIDs = new Set();
  let row;
  for (const [i, r] of data.records.entries()) {
    try {
      const cell = validateCell(r);
      const key = `${r.tier}:${r.column}`;
      requireValue(!seen.has(key) && !seenIDs.has(r.id), 'duplicate cell or record id');
      seen.add(key); seenIDs.add(r.id);
      const currentRow = JSON.stringify([r.host.installation_id, r.host.os, r.host.version, r.host.install_mode]);
      if (row === undefined) row = currentRow;
      requireValue(row === currentRow, 'records must describe one install row and OS version');
      if (readTrace) for (const kind of traceKinds) {
        const trace = r.traces[kind];
        const bytes = readTrace(trace.path);
        requireValue(bytes.length > 0 && createHash('sha256').update(bytes).digest('hex') === trace.sha256, `${kind}: missing or mismatched trace bytes`);
      }
      cells.push(cell);
    } catch (error) { errors.push(`records[${i}]: ${error.message}`); }
  }
  for (const tier of tiers) {
    const measured = cells.filter(c => c.tier === tier.tier);
    tier.missing = columns.filter(column => !measured.some(c => c.column === column));
    if (measured.some(c => !c.passed)) tier.status = 'fail';
    else if (!tier.missing.length) tier.status = 'pass';
  }
  const attemptedSecond = data.records.some(r => r?.tier === 2);
  if (attemptedSecond && tiers[0].status !== 'pass') errors.push('ladder stopped: tier 2 attempted before tier 1 passed');
  if (attemptedSecond && tiers[0].status === 'pass') {
    const firstEnd = Math.max(...cells.filter(c => c.tier === 1).map(c => c.ended));
    if (cells.some(c => c.tier === 2 && c.started <= firstEnd)) errors.push('ladder stopped: tier 2 began before tier 1 finished');
  }
  // An invalid lower tier invalidates higher-tier claims even if the complete
  // set of eight valid cells also appeared in the same malformed input.
  if (tiers[0].status !== 'pass' || errors.length) tiers[1].status = 'unverified';
  return { valid: errors.length === 0, passed: errors.length === 0 && tiers.every(t => t.status === 'pass'), errors, cells, tiers };
}

export function checkFile(file) {
  const base = realpathSync(dirname(resolve(file)));
  return validateEvidence(JSON.parse(readFileSync(file, 'utf8')), { readTrace(name) {
    const target = realpathSync(resolve(base, name));
    const rel = relative(base, target);
    requireValue(rel !== '..' && !rel.startsWith(`..${sep}`) && !isAbsolute(rel), 'trace escapes evidence directory');
    const info = statSync(target);
    requireValue(info.isFile() && info.size <= 32 * 1024 * 1024, 'trace must be a regular file of at most 32 MiB');
    return readFileSync(target);
  } });
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    requireValue(process.argv.length === 3, 'usage: node scripts/non-competition.mjs <records.json>');
    const result = checkFile(process.argv[2]);
    console.log(JSON.stringify(result, null, 2));
    process.exitCode = result.passed ? 0 : 1;
  } catch (error) {
    console.log(JSON.stringify({ valid: false, passed: false, errors: [error.message] }, null, 2));
    process.exitCode = 1;
  }
}
