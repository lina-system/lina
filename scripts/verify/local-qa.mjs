#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
import { spawn, spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { accessSync, closeSync, constants, existsSync, fstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, readdirSync, realpathSync, renameSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { availableParallelism, hostname, tmpdir } from 'node:os';
import { basename, delimiter, dirname, isAbsolute, join, relative, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { git, gitOut, toplevel } from '../git.mjs';
import { finishFoundation } from '../foundation.mjs';
import { aggregate, readPlan, readResults } from './pipeline.mjs';
import { githubAPI, reconcileQA } from './post-merge-qa.mjs';

const sha = value => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value);
const readJSON = path => JSON.parse(readFileSync(path, 'utf8'));
function save(path, value) {
  const temporary = path + '.' + randomUUID() + '.tmp';
  const fd = openSync(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try {
    writeFileSync(fd, JSON.stringify(value) + '\n');
    renameSync(temporary, path);
  } finally {
    closeSync(fd);
    rmSync(temporary, { force: true });
  }
}

// Called only after the sandbox has exited. The parent directory is created
// by the host and mounted as /output, so candidate code cannot replace it.
export function readSandboxJSON(path) {
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const info = fstatSync(fd);
    if (!info.isFile() || info.nlink !== 1 || info.size <= 0 || info.size > 64 * 1024 * 1024) {
      throw new Error('QA output must be a bounded regular file, not a link or special file');
    }
    return JSON.parse(readFileSync(fd, 'utf8'));
  } finally {
    closeSync(fd);
  }
}
const isolatedGit = { env: { GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0' } };

// Keep independent Git metadata: the sandbox must not see the operator's
// common Git directory (which may contain credentials and other worktrees).
function checkout(root, worktree, head) {
  gitOut(root, ['-c', 'core.hooksPath=/dev/null', 'clone', '--quiet', '--no-local', '--no-hardlinks', '--no-checkout', root, worktree], isolatedGit);
  gitOut(worktree, ['-c', 'core.hooksPath=/dev/null', 'fetch', '--quiet', '--no-tags', root, head], isolatedGit);
  gitOut(worktree, ['-c', 'core.hooksPath=/dev/null', 'checkout', '--quiet', '--detach', head], isolatedGit);
  gitOut(worktree, ['config', '--remove-section', 'remote.origin'], isolatedGit);
  if (gitOut(worktree, ['status', '--porcelain', '--untracked-files=all'], isolatedGit)) throw new Error('QA checkout is not clean');
}

export function readLocalHistory(directory, root) {
  if (!existsSync(directory)) return [];
  const history = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    if (!entry.isDirectory() || !sha(entry.name)) continue;
    const runDirectory = join(directory, entry.name);
    const manifest = readJSON(join(runDirectory, 'run.json'));
    if (manifest.phase !== 'completed') continue;
    const plan = readJSON(join(runDirectory, 'plan.json'));
    if (plan.head !== entry.name || gitOut(root, ['rev-parse', plan.head + '^{tree}']) !== plan.tree) {
      throw new Error('Stored QA plan does not match its commit tree');
    }
    history.push({ ...manifest, plan, records: readResults(join(runDirectory, 'results')) });
  }
  return history;
}

export async function reportLocalRun({ root, runDirectory, repository, milestone, requiredPasses }) {
  const history = readLocalHistory(dirname(runDirectory), root);
  const current = history.find(run => run.localRun.head === readJSON(join(runDirectory, 'run.json')).localRun.head);
  if (!current) throw new Error('Missing completed local QA result');
  return reconcileQA({ ...current, repository, milestone, requiredPasses, localHistory: history,
    api: githubAPI, isAncestor: async (a, b) => {
      const result = git(root, ['merge-base', '--is-ancestor', a, b]);
      if (![0, 1].includes(result.status)) throw new Error('Cannot compare QA commit ancestry');
      return result.status === 0;
    } });
}

// Each invocation is finite. The operator's timer calls it again; no watcher or
// daemon is installed. The CLI holds flock across selection, testing and reporting.
export async function runLocalQA({ cwd = process.cwd(), stateFile, repository, milestone,
  jobs = 2, intervalMs = 30 * 60 * 1000, requiredPasses = 3, now = Date.now,
  execute = executeLocalRun, report = reportLocalRun } = {}) {
  if (!stateFile || !/^[\w.-]+\/[\w.-]+$/.test(repository ?? '')
      || !Number.isSafeInteger(milestone) || milestone < 1
      || !Number.isSafeInteger(jobs) || jobs < 1
      || !Number.isSafeInteger(intervalMs) || intervalMs < 0
      || !Number.isSafeInteger(requiredPasses) || requiredPasses < 1) throw new Error('Invalid local QA configuration');
  const root = toplevel(cwd);
  stateFile = resolve(stateFile);
  const inside = relative(root, stateFile);
  if (!inside.startsWith('..' + '/') && !isAbsolute(inside)) throw new Error('QA state must be outside the repository');
  const directory = dirname(stateFile);
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  let state = existsSync(stateFile) ? readJSON(stateFile) : { schema: 1, repository, branch: 'dev' };
  if (state.schema !== 1 || state.repository !== repository || state.branch !== 'dev'
      || (state.lastHead !== undefined && (!sha(state.lastHead) || !Number.isFinite(state.lastStarted)))
      || (state.pending !== undefined && !sha(state.pending))) throw new Error('Invalid or mismatched local QA state');
  const reportPending = async () => {
    const runDirectory = join(directory, 'runs', state.pending);
    const manifest = readJSON(join(runDirectory, 'run.json'));
    if (manifest.phase !== 'completed') {
      // A previous process died before it published a complete run. Its head
      // stays consumed, but incomplete evidence must never reach recovery.
      save(join(runDirectory, 'run.json'), { ...manifest, phase: 'interrupted' });
      delete state.pending;
      save(stateFile, state);
      return [];
    }
    const changes = await report({ root, runDirectory, repository, milestone, requiredPasses });
    delete state.pending;
    save(stateFile, state);
    return changes;
  };
  if (state.pending) await reportPending();
  gitOut(root, ['fetch', '--no-tags', 'origin', 'refs/heads/dev:refs/remotes/origin/dev']);
  const head = gitOut(root, ['rev-parse', 'refs/remotes/origin/dev^{commit}']);
  const runDirectory = join(directory, 'runs', head);
  if (head === state.lastHead || existsSync(runDirectory)) return { status: 'unchanged', head };
  const started = now();
  if (!Number.isFinite(started)) throw new Error('Invalid local QA clock');
  if (state.lastStarted !== undefined && started - state.lastStarted < intervalMs) return { status: 'interval', head };
  mkdirSync(runDirectory, { recursive: true, mode: 0o700 });
  const localRun = { machine: hostname(), head, started, logPath: join(runDirectory, 'run.log') };
  const manifest = { schema: 1, repository, branch: 'dev', before: gitOut(root, ['rev-parse', head + '^1']), localRun, phase: 'running' };
  save(join(runDirectory, 'run.json'), manifest);
  // Consume the head before starting any candidate code, including selection.
  state = { ...state, lastHead: head, lastStarted: started, pending: head };
  save(stateFile, state);
  const temporary = mkdtempSync(join(tmpdir(), 'lina-local-qa-'));
  const worktree = join(temporary, 'candidate');
  let result;
  try {
    checkout(root, worktree, head);
    result = await execute({ worktree, runDirectory, localRun, jobs, cacheDirectory: join(directory, 'cache') });
    if (result.plan) localRun.started = result.plan.started;
    save(join(runDirectory, 'run.json'), { ...manifest, phase: 'completed', result: result.result });
  } catch (error) {
    save(join(runDirectory, 'run.json'), { ...manifest, phase: 'interrupted' });
    delete state.pending;
    save(stateFile, state);
    throw error;
  } finally {
    rmSync(temporary, { recursive: true, force: true });
  }
  const changes = await reportPending();
  return { status: 'tested', head, result: result.result, changes, logPath: localRun.logPath };
}

// Resolved in-process: inside the sandbox /usr/bin/which may point through an
// unmounted /etc/alternatives entry.
export function toolPath(name) {
  for (const directory of (process.env.PATH ?? '').split(delimiter)) {
    if (!isAbsolute(directory)) continue;
    const candidate = join(directory, name);
    try {
      accessSync(candidate, constants.X_OK);
      if (statSync(candidate).isFile()) return realpathSync(candidate);
    } catch {
      // Missing or unusable entries fall through to the next PATH directory.
    }
  }
  throw new Error('Required QA tool is unavailable: ' + name);
}

// The npm package shipped beside a user-local Node (nvm, mise, a release
// archive). System packages under /usr are already visible and may resolve
// dependencies through sibling directories, so they are not rebound.
export function npmPackage(node) {
  const launcher = join(dirname(node), 'npm');
  if (!existsSync(launcher)) return undefined;
  const cli = realpathSync(launcher);
  const directory = dirname(dirname(cli));
  if (directory.startsWith('/usr/') || basename(cli) !== 'npm-cli.js'
      || readJSON(join(directory, 'package.json')).name !== 'npm') return undefined;
  return directory;
}

// Same foundation shard command as CI, with an additional outer filesystem/PID
// sandbox for a persistent machine. Only public dependency caches are shared;
// networking remains available for dependency and vulnerability checks, as in CI.
export function sandboxArguments({ worktree, outputDirectory, planFile, cacheDirectory, goRoot, node, npm, actionlint, jobs }, args) {
  return [
    '--unshare-all', '--share-net', '--die-with-parent', '--new-session', '--cap-drop', 'ALL', '--clearenv',
    ...['/usr', '/bin', '/lib', '/lib64', '/etc/ssl', '/etc/resolv.conf', '/etc/hosts', '/etc/nsswitch.conf', '/etc/ld.so.cache', '/etc/apparmor.d']
      .filter(existsSync).flatMap(path => ['--ro-bind', path, path]),
    '--proc', '/proc', '--dev', '/dev', '--tmpfs', '/tmp', '--dir', '/home/qa', '--dir', '/tools',
    '--ro-bind', node, '/tools/node', '--ro-bind', actionlint, '/tools/actionlint',
    ...(npm ? ['--ro-bind', npm, '/tools/npm-package', '--symlink', '/tools/npm-package/bin/npm-cli.js', '/tools/npm'] : []),
    '--ro-bind', goRoot, '/toolchain', '--ro-bind', worktree, '/source',
    '--bind', outputDirectory, '/output',
    ...(planFile ? ['--ro-bind', planFile, '/qa/plan.json'] : []),
    '--bind', cacheDirectory, '/cache', '--chdir', '/source',
    ...Object.entries({ PATH: '/toolchain/bin:/tools:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin', HOME: '/home/qa',
      XDG_CONFIG_HOME: '/home/qa', TMPDIR: '/tmp', GOROOT: '/toolchain', GOENV: 'off',
      GOCACHE: '/cache/build', GOMODCACHE: '/cache/modules',
      GOMAXPROCS: String(Math.max(1, Math.floor(availableParallelism() / jobs))),
      GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0' })
      .flatMap(([name, value]) => ['--setenv', name, value]),
    '/tools/node', '/source/scripts/foundation.mjs', ...args,
  ];
}

function sandboxRun(context, args, logPath) {
  const fd = openSync(logPath, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  return new Promise((resolveRun, reject) => {
    const child = spawn('bwrap', sandboxArguments(context, args), {
      env: { PATH: '/usr/bin:/bin' }, detached: true, stdio: ['ignore', fd, fd],
    });
    closeSync(fd);
    const timer = setTimeout(() => {
      try { process.kill(-child.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') reject(error); }
    }, 1_205_000);
    child.on('error', error => { clearTimeout(timer); reject(error); });
    child.on('close', code => { clearTimeout(timer); resolveRun(code ?? 1); });
  });
}

export async function runLocalShards(plan, jobs, run) {
  let index = 0;
  let upstream = 'success';
  const errors = [];
  await Promise.all(Array.from({ length: Math.min(jobs, plan.tasks.length) }, async () => {
    while (index < plan.tasks.length) {
      const task = plan.tasks[index++];
      // A failing shard must not cancel the rest of the full plan.
      try {
        const status = await run(['--plan', '/qa/plan.json', '--shard', task.id,
          '--out', '/output/' + task.id + '.json'], task.id);
        if (status !== 0) upstream = 'failure';
      } catch (error) {
        errors.push(error);
      }
    }
  }));
  if (errors.length) throw errors[0];
  return upstream;
}

export async function executeLocalRun(input) {
  if (process.platform !== 'linux') throw new Error('Local QA requires Linux and bubblewrap');
  mkdirSync(input.cacheDirectory, { recursive: true, mode: 0o700 });
  const go = spawnSync('go', ['env', 'GOROOT'], { encoding: 'utf8', env: {
    PATH: process.env.PATH, HOME: tmpdir(), GOENV: 'off', GOTOOLCHAIN: 'local',
  } });
  if (go.status !== 0) throw new Error('Cannot locate the installed Go toolchain');
  const node = realpathSync(process.execPath);
  const context = { ...input, goRoot: realpathSync(go.stdout.trim()), node, npm: npmPackage(node), actionlint: toolPath('actionlint') };
  const outputs = mkdtempSync(join(tmpdir(), 'lina-qa-output-'));
  const planFile = join(input.runDirectory, 'plan.json');
  const results = join(input.runDirectory, 'results');
  mkdirSync(results);
  try {
    const run = async (args, id) => {
      const outputDirectory = join(outputs, id);
      mkdirSync(outputDirectory);
      const status = await sandboxRun({ ...context, outputDirectory, planFile: id === 'selection' ? undefined : planFile },
        args, join(input.runDirectory, id + '.log'));
      const name = id === 'selection' ? 'plan.json' : id + '.json';
      try {
        const record = readSandboxJSON(join(outputDirectory, name));
        save(id === 'selection' ? planFile : join(results, name), record);
      } catch (error) {
        // A killed command may not have produced a result. Links and malformed
        // output fail closed, after the other independent shards have exited.
        if (error.code !== 'ENOENT') throw error;
      }
      return status;
    };
    if (await run(['--full', '--event', 'schedule', '--no-note', '--select', '/output/plan.json'], 'selection') !== 0) {
      throw new Error('Local QA selection failed; see selection.log');
    }
    const plan = readPlan(planFile, input.worktree);
    const upstream = await runLocalShards(plan, input.jobs, run);
    const records = readResults(results);
    const evidence = finishFoundation(input.worktree, plan, aggregate(plan, records, upstream), { note: false });
    save(join(input.runDirectory, 'foundation.json'), evidence);
    save(input.localRun.logPath, { ...evidence, logs: ['selection.log', ...plan.tasks.map(task => task.id + '.log')] });
    return { plan, result: evidence.result };
  } finally {
    rmSync(outputs, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const { values } = parseArgs({ options: { state: { type: 'string' }, repository: { type: 'string' },
      milestone: { type: 'string' }, jobs: { type: 'string' }, 'interval-minutes': { type: 'string' },
      'recovery-passes': { type: 'string' }, locked: { type: 'boolean' } } });
    if (!values.state) throw new Error('--state is required (outside the checkout)');
    if (!values.locked) {
      mkdirSync(dirname(resolve(values.state)), { recursive: true, mode: 0o700 });
      const child = spawn('flock', ['--exclusive', '--nonblock', '--conflict-exit-code', '75',
        resolve(values.state) + '.lock', process.execPath, fileURLToPath(import.meta.url), ...process.argv.slice(2), '--locked'], { stdio: 'inherit' });
      const code = await new Promise((resolveChild, reject) => { child.on('error', reject); child.on('close', resolveChild); });
      if (code === 75) console.log(JSON.stringify({ status: 'busy' }));
      process.exitCode = code === 75 ? 0 : code ?? 1;
    } else {
      const result = await runLocalQA({ stateFile: values.state, repository: values.repository,
        milestone: Number(values.milestone ?? process.env.QA_MILESTONE), jobs: Number(values.jobs ?? 2),
        intervalMs: Number(values['interval-minutes'] ?? 30) * 60_000,
        requiredPasses: Number(values['recovery-passes'] ?? process.env.QA_RECOVERY_PASSES ?? 3) });
      console.log(JSON.stringify(result));
      if (result.result === 'failure') process.exitCode = 1;
    }
  } catch (error) {
    console.error('local QA: ' + error.message);
    process.exitCode = 1;
  }
}
