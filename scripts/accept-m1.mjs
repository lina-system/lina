#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Verify an owner record; never manufacture observations or declare a hand run.
import { createHash, randomBytes } from 'node:crypto';
import { readFileSync, writeFileSync, realpathSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { spawn, spawnSync } from 'node:child_process';
import { createInterface } from 'node:readline';
import { git, gitOut } from './git.mjs';

const schema = JSON.parse(readFileSync(new URL('../evidence/m1/record.schema.json', import.meta.url)));
export const checks = Object.freeze(Object.keys(schema.$defs.attempt.properties.checks.properties));
export const failureChecks = Object.freeze(Object.keys(schema.$defs.attempt.properties.failures.properties));
const steps = ['fresh', 'planned_restart', 'crash_restart', 'restore'];
const pkg = 'github.com/thisisjun786/lina/e2e/m1';
const uiPackage = 'github.com/thisisjun786/lina/internal/tui/ui';
const uiTests = ['TestUnchangedConnectionWritesNothing', 'TestConversationDoneReplacesStreamAndUnchangedWritesNothing',
  'TestConversationMarkdownProfilesAndUnchangedOutput'];
export const headlessTests = Object.freeze([
  ...uiTests,
  'TestM1Install', 'TestM1StartupCrashRecovery', 'TestM1Doctor', 'TestM1CanonFaultAdmissionAndDoctor',
  'TestM1TUIConversationInstalledCore', 'TestM1TUIAccountsInstalledCore', 'TestM1ChatGPTSignInOwnerBinding',
  'TestM1ReplyShapeLONGQA', 'TestM1TurnBudgetHandoffInstalledCore', 'TestM1RunLandingNotice', 'TestM1RunQuestionNotice',
  'TestM1MemoryCorrectionForgetRestore', 'TestM1KoreanMemoryByID', 'TestM1PreparedSummaries', 'TestM1MemoryJobs',
  'TestM1SkillsInstalledCore', 'TestM1ImportReconciliation', 'TestM1ImportRehearsalPromotion', 'TestM1StagedImportWorker',
  'TestM1TUIChannelPairing', 'TestM1TelegramCommitCrash', 'TestM1TelegramOwnerButton', 'TestM1TUITalkLogInstalledCore',
  'TestM1SandboxUnavailable', 'TestM1ClientAdmissionSandbox', 'TestM1ToolEvidenceCannotApprove',
  'TestM1RestartWaitsForEffect', 'TestPlannedRestart', 'TestM1WaitingRunningNotices', 'TestM1TUICards',
  'TestM1TUIApprovalsInstalledCore', 'TestM1TurnCrashBoundaries', 'TestM1BackgroundRunSIGKILL',
  'TestM1ProviderFailureHistory', 'TestM1ClientListener', 'TestM1TopicStreams', 'TestM1TUIStream', 'TestM1TUIRecovery',
  'TestM1BackupInstalledConversation', 'TestM1RestoreInstalledConversation', 'TestM2InstalledWorkerOutcome',
  ...Array.from({ length: 13 }, (_, i) => `TestM1StartupCrashRecovery/${i + 1}`),
  ...['claimed', 'prepared', 'model_item', 'tool_intent', 'tool_effect', 'tool_result', 'completed']
    .map(phase => `TestM1TurnCrashBoundaries/${phase}`),
]);

function requireValue(condition, code) { if (!condition) throw new Error(code); }
const hashOf = bytes => createHash('sha256').update(bytes).digest('hex');
function parseJSON(bytes) {
  let value;
  try { value = JSON.parse(bytes); } catch { throw new Error('invalid_json'); }
  // JSON.parse discards duplicate keys. Scan the already-valid JSON grammar so
  // a later key cannot hide a failed result or an unredacted diagnosis field.
  const tokens = String(bytes).match(/"(?:\\.|[^"\\])*"|[{}\[\]:,]/g) ?? [];
  const stack = [];
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i];
    if (token === '{') stack.push(new Set());
    else if (token === '[') stack.push(null);
    else if (token === '}' || token === ']') stack.pop();
    else if (token.startsWith('"') && tokens[i + 1] === ':') {
      const key = JSON.parse(token), keys = stack.at(-1);
      requireValue(keys && !keys.has(key), 'duplicate_json_key');
      keys.add(key);
    }
  }
  return value;
}

// Interpret only the keywords used by the bundled schema, with no dependency or
// downloaded schema. Cross-record invariants are checked below.
function validate(value, rule, field = 'record') {
  if (rule.$ref) return validate(value, schema.$defs[rule.$ref.slice('#/$defs/'.length)], field);
  if (rule.anyOf) {
    requireValue(rule.anyOf.some(option => { try { validate(value, option, field); return true; } catch { return false; } }), `${field}: type`);
    return;
  }
  if ('const' in rule) requireValue(value === rule.const, `${field}: constant`);
  if (rule.enum) requireValue(rule.enum.includes(value), `${field}: enum`);
  if (rule.type === 'object') {
    requireValue(value !== null && typeof value === 'object' && !Array.isArray(value), `${field}: object`);
    requireValue(rule.required.every(key => Object.hasOwn(value, key)), `${field}: missing_field`);
    requireValue(Object.keys(value).every(key => Object.hasOwn(rule.properties, key)), `${field}: unknown_field`);
    for (const [key, child] of Object.entries(rule.properties)) validate(value[key], child, `${field}.${key}`);
  } else if (rule.type === 'array') {
    requireValue(Array.isArray(value) && value.length >= rule.minItems, `${field}: array`);
    value.forEach((child, i) => validate(child, rule.items, `${field}.${i}`));
  } else if (rule.type === 'integer') {
    requireValue(Number.isSafeInteger(value) && value >= rule.minimum, `${field}: integer`);
  } else if (rule.type === 'null') requireValue(value === null, `${field}: null`);
  else if (rule.type) requireValue(typeof value === rule.type, `${field}: ${rule.type}`);
  if (rule.pattern) requireValue(new RegExp(rule.pattern).test(value), `${field}: pattern`);
}

function sameSet(actual, expected, code) {
  requireValue(actual.length === expected.length && new Set(actual).size === actual.length
    && expected.every(id => actual.includes(id)), code);
}

function diagnosis(d) {
  requireValue(d.query && d.citation && d.injection_root && d.events.length >= 2, 'diagnosis_missing_observation');
  const events = new Map(d.events.map(event => [event.event_id, event]));
  requireValue(events.size === d.events.length, 'diagnosis_duplicate_event');
  const c = d.citation, leaf = events.get(c.event_id), root = events.get(d.injection_root);
  requireValue(c.root_event_id === d.injection_root && root?.cause.parent_event_id === null, 'diagnosis_root');
  requireValue(root.result === 'pending' && root.next_action === 'continue' && root.error_class === null
    && ['boot_started', 'turn_started', 'task_started'].includes(root.cause.code), 'diagnosis_root_fields');
  requireValue(leaf && leaf.error_class === d.error_class && c.error_class === d.error_class
    && leaf.cause.code === c.cause_code && ['failure', 'cancelled'].includes(leaf.result), 'diagnosis_citation');
  for (const event of events.values()) {
    requireValue(event.task_id === d.query.task_id, 'diagnosis_query');
    requireValue(Number.isFinite(Date.parse(event.time)), 'diagnosis_time');
    const seen = new Set();
    let current = event;
    while (current.cause.parent_event_id !== null) {
      requireValue(!seen.has(current.event_id), 'diagnosis_cycle');
      seen.add(current.event_id);
      current = events.get(current.cause.parent_event_id);
      requireValue(current, 'diagnosis_missing_parent');
    }
    requireValue(current.event_id === d.injection_root, 'diagnosis_disconnected');
  }
}

function validateHeadless(receipt, attempt) {
  requireValue(receipt.format === 1 && receipt.kind === 'm1-headless'
    && receipt.commit === attempt.commit && receipt.run_id === attempt.run_id && receipt.exit_code === 0, 'headless_provenance');
  requireValue(Array.isArray(receipt.tests), 'headless_tests');
  sameSet(receipt.tests.map(t => t.id), headlessTests, 'headless_coverage');
  requireValue(receipt.tests.every(t => t.result === 'pass'), 'headless_failure');
}

export function validateRecord(record, { readEvidence } = {}) {
  validate(record, schema);
  requireValue(typeof readEvidence === 'function', 'evidence_reader_required');
  const runs = new Set(), commits = new Set(), roots = new Set();
  let lastPassed = false;
  for (const [index, a] of record.attempts.entries()) {
    requireValue(!runs.has(a.run_id) && !commits.has(a.commit), 'rerun_requires_new_run_and_fixing_commit');
    requireValue(!lastPassed, 'rerun_requires_failed_attempt');
    runs.add(a.run_id); commits.add(a.commit);
    const attachment = hash => {
      requireValue(hash !== null, 'missing_evidence');
      let bytes;
      try { bytes = readEvidence(hash); } catch { throw new Error('missing_evidence'); }
      requireValue(typeof bytes === 'string' || Buffer.isBuffer(bytes), 'missing_evidence');
      requireValue(hashOf(bytes) === hash, 'evidence_hash');
      return parseJSON(bytes);
    };
    const evidence = hash => {
      const data = attachment(hash);
      requireValue(data.commit === a.commit && data.run_id === a.run_id, 'stale_evidence');
      return data;
    };
    const ownerObservation = hash => {
      const receipt = evidence(hash);
      requireValue(receipt.format === 1 && receipt.kind === 'owner-observation', 'owner_observation_required');
    };
    const outcomes = [...Object.values(a.checks), ...Object.values(a.failures), a.ui_ux, ...a.terminals.records];
    lastPassed = [...outcomes, ...a.diagnosis, ...a.matrix].every(c => c.result === 'pass');
    // Even failed attempts retain their complete matrix and evidence. Later
    // attempts cannot patch a subset of the old record into a passing result.
    if (lastPassed) requireValue(a.clean_install, 'clean_install_required');
    requireValue(a.matrix.length === steps.length && a.matrix.every((row, i) => row.step === steps[i]), 'matrix_steps');
    let epoch = 0n;
    const firstRoot = a.matrix[0].root_id, restoredRoot = a.matrix[3].root_id;
    requireValue(!roots.has(firstRoot) && !roots.has(restoredRoot), 'reused_install_root');
    if (firstRoot !== null) roots.add(firstRoot);
    if (restoredRoot !== null) roots.add(restoredRoot);
    if (lastPassed) requireValue(firstRoot !== null && restoredRoot !== null && firstRoot !== restoredRoot, 'fresh_and_restore_roots');
    for (const row of a.matrix) {
      if (row.result === 'not_run') {
        requireValue(['epoch', 'root_id', 'ready', 'effects_replayed', 'evidence_sha256'].every(key => row[key] === null), 'unrun_matrix_observation');
        continue;
      }
      requireValue(row.epoch !== null && row.root_id !== null && row.ready !== null && row.effects_replayed !== null, 'missing_matrix_observation');
      const current = BigInt(row.epoch);
      requireValue(current <= 18446744073709551615n, 'epoch_overflow');
      if (lastPassed) requireValue(current > epoch, 'matrix_epoch');
      epoch = current;
      requireValue(row.root_id === (row.step === 'restore' ? restoredRoot : firstRoot), 'restart_root');
      ownerObservation(row.evidence_sha256);
      if (lastPassed) requireValue(row.ready && row.effects_replayed === 0, 'restart_observation');
    }
    if (lastPassed) requireValue(a.restore?.whole_generation && a.restore.forgotten_search_hits === 0
      && a.restore.forgotten_summary_hits === 0 && a.restore.forgotten_context_hits === 0, 'restore_zero_exposure');
    const manifest = attachment(a.manifest_sha256);
    requireValue(manifest.commit === a.commit && manifest.target === a.host.replace('-', '/'), 'build_provenance');
    const headless = evidence(a.headless_sha256);
    if (lastPassed) validateHeadless(headless, a);
    for (const result of [...Object.values(a.checks), ...Object.values(a.failures), a.ui_ux]) {
      if (result.result !== 'not_run') evidence(result.evidence_sha256);
      else requireValue(result.evidence_sha256 === null, 'unrun_evidence');
    }
    if (a.ui_ux.result !== 'not_run') ownerObservation(a.ui_ux.evidence_sha256);
    for (const result of [...Object.values(a.checks), ...Object.values(a.failures)]) {
      if (result.result === 'pass') requireValue(result.evidence_sha256 === a.headless_sha256, 'behavior_requires_headless');
    }
    requireValue(new Set(a.terminals.ids).size === a.terminals.ids.length, 'duplicate_terminal');
    sameSet(a.terminals.records.map(r => `${r.terminal_id}/${r.scenario}`),
      a.terminals.ids.flatMap(id => Array.from({ length: 11 }, (_, i) => `${id}/S${i + 1}`)), 'terminal_matrix');
    if (lastPassed) requireValue(a.terminals.confirmed, 'terminal_confirmation');
    const terminalIdentities = new Map(), identityOwners = new Map();
    for (const row of a.terminals.records) {
      if (row.result === 'not_run') {
        requireValue(row.evidence_sha256 === null, 'unrun_evidence');
        continue;
      }
      const spike = attachment(row.evidence_sha256);
      requireValue(spike.format === 1 && spike.scenario === row.scenario && spike.result === row.result
        && spike.measured?.source_revision === a.commit, 'spike_provenance');
      if (lastPassed && row.scenario !== 'S8') requireValue(spike.measured.owner_confirmed_matrix === true, 'spike_confirmation');
      if (row.scenario !== 'S8') {
        const terminal = spike.terminal;
        requireValue(terminal && typeof terminal.name === 'string' && typeof terminal.version === 'string'
          && typeof terminal.term === 'string' && typeof terminal.ssh === 'boolean', 'terminal_metadata');
        const identity = JSON.stringify([terminal.name, terminal.version, terminal.term, terminal.ssh]);
        requireValue((!terminalIdentities.has(row.terminal_id) || terminalIdentities.get(row.terminal_id) === identity)
          && (!identityOwners.has(identity) || identityOwners.get(identity) === row.terminal_id), 'terminal_identity');
        terminalIdentities.set(row.terminal_id, identity); identityOwners.set(identity, row.terminal_id);
      }
    }
    sameSet(a.diagnosis.map(d => d.error_class), ['model', 'interrupted', 'storage'], 'diagnosis_matrix');
    for (const d of a.diagnosis) {
      if (d.result === 'pass') diagnosis(d);
      else if (d.result === 'not_run') requireValue(d.injection_root === null && d.query === null
        && d.citation === null && d.events.length === 0, 'unrun_diagnosis_observation');
    }
    if (index === record.attempts.length - 1) requireValue(lastPassed, 'acceptance_incomplete');
  }
  return { accepted: true, attempts: record.attempts.length, commit: record.attempts.at(-1).commit };
}

export function checkFile(file, { cwd = process.cwd(), checkSpike } = {}) {
  const directory = realpathSync(dirname(resolve(file)));
  const record = parseJSON(readFileSync(file));
  const pathFor = hash => {
    const attachment = join(directory, `${hash}.json`);
    requireValue(dirname(realpathSync(attachment)) === directory, 'evidence_outside_directory');
    return attachment;
  };
  const result = validateRecord(record, { readEvidence: hash => readFileSync(pathFor(hash)) });
  for (const attempt of record.attempts) {
    requireValue(git(cwd, ['merge-base', '--is-ancestor', attempt.commit, 'origin/dev']).status === 0, 'dev_commit_required');
    requireValue(typeof checkSpike === 'function', 'spike_validator_required');
    for (const row of attempt.terminals.records) if (row.result !== 'not_run') checkSpike(pathFor(row.evidence_sha256));
  }
  return result;
}

export function summarizeHeadless(events, exitCode, commit, run_id) {
  requireValue(exitCode === 0 && [pkg, uiPackage].every(p => events.some(e => e.Package === p && !e.Test && e.Action === 'pass'))
    && !events.some(e => e.Action === 'fail'), 'headless_command_failed');
  const results = new Map();
  for (const e of events) if (e.Package === (uiTests.includes(e.Test) ? uiPackage : pkg) && headlessTests.includes(e.Test)
    && ['pass', 'fail', 'skip'].includes(e.Action)) {
    requireValue(!results.has(e.Test), 'headless_duplicate_result');
    results.set(e.Test, e.Action);
  }
  const receipt = { format: 1, kind: 'm1-headless', commit, run_id, exit_code: exitCode,
    tests: [...results].map(([id, result]) => ({ id, result })) };
  validateHeadless(receipt, { commit, run_id });
  return receipt;
}

async function runHeadless(directory) {
  const cwd = process.cwd(), commit = gitOut(cwd, ['rev-parse', 'HEAD']);
  requireValue(gitOut(cwd, ['status', '--porcelain']) === '', 'clean_checkout_required');
  requireValue(git(cwd, ['merge-base', '--is-ancestor', commit, 'origin/dev']).status === 0, 'dev_commit_required');
  const run_id = randomBytes(16).toString('hex'), events = [];
  const child = spawn('go', ['test', '-json', '-count=1', './e2e/m1/...', './internal/tui/ui'],
    { stdio: ['ignore', 'pipe', 'inherit'], detached: process.platform !== 'win32' });
  const completed = new Promise(resolve => { child.once('error', () => resolve(1)); child.once('close', resolve); });
  const stop = () => {
    try { process.platform === 'win32' ? child.kill('SIGKILL') : process.kill(-child.pid, 'SIGKILL'); } catch { /* already joined */ }
  };
  process.once('SIGINT', stop); process.once('SIGTERM', stop);
  const lines = createInterface({ input: child.stdout });
  let exitCode;
  try {
    for await (const line of lines) {
      const e = parseJSON(line);
      // Output can include synthetic conversation text and paths; retain only codes.
      if (['pass', 'fail', 'skip'].includes(e.Action)) events.push({ Action: e.Action, Package: e.Package, Test: e.Test });
    }
    exitCode = await completed;
  } finally {
    stop(); await completed; lines.close();
    process.removeListener('SIGINT', stop); process.removeListener('SIGTERM', stop);
  }
  requireValue(gitOut(cwd, ['rev-parse', 'HEAD']) === commit && gitOut(cwd, ['status', '--porcelain']) === '', 'checkout_changed');
  let receipt, passed = true;
  try { receipt = summarizeHeadless(events, exitCode, commit, run_id); }
  catch {
    passed = false;
    receipt = { format: 1, kind: 'm1-headless', commit, run_id, exit_code: exitCode ?? 1,
      tests: headlessTests.map(id => ({ id, result: events.find(e => e.Test === id
        && e.Package === (uiTests.includes(id) ? uiPackage : pkg))?.Action ?? 'not_run' })) };
  }
  const bytes = JSON.stringify(receipt) + '\n', sha256 = hashOf(bytes);
  writeFileSync(join(directory, `${sha256}.json`), bytes, { flag: 'wx', mode: 0o600 });
  return { passed, commit, run_id, headless_sha256: sha256 };
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    const args = process.argv.slice(2);
    requireValue(args.length === 1 || (args.length === 2 && args[0] === '--run-headless'),
      'usage: node scripts/accept-m1.mjs <record.json> | --run-headless <evidence-directory>');
    const checkSpike = file => {
      requireValue(process.env.LINA_SPIKE_RUNNER, 'set_LINA_SPIKE_RUNNER');
      const checked = spawnSync(process.env.LINA_SPIKE_RUNNER, ['-check-evidence', file], { stdio: 'pipe', timeout: 30000 });
      requireValue(checked.status === 0, 'spike_validation_failed');
    };
    const result = args.length === 1 ? checkFile(args[0], { checkSpike }) : await runHeadless(args[1]);
    console.log(JSON.stringify(result));
    if (result.passed === false) process.exitCode = 1;
  } catch (error) {
    // Never print raw JSON parser errors, paths or record values.
    console.error(error.code ? 'accept-m1: cannot read or write evidence' : `accept-m1: ${error.message}`);
    process.exitCode = 1;
  }
}
