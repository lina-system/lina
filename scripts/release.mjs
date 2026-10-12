#!/usr/bin/env node
// Releases one exact commit of dev.
//
//   node scripts/release.mjs --commit <sha> --tag vX.Y.Z --notes-file <file> [--publish]
//                            [--remote <name>] [--out <directory>]
//
// The default is a dry run: every check and the build run, and nothing is pushed or published.
// --publish runs only with the owner's approval of this commit and tag. The script checks that
//   - the commit belongs to dev and main can fast-forward to it,
//   - the tag does not exist on the remote or point elsewhere locally (tags are immutable),
//   - the commit has foundation evidence for its exact tree, or foundation passes again on a
//     clean work tree of exactly that commit,
//   - the GitHub repository is private, because the release artifacts are private.
// The build step runs on the exact commit without account credentials. The publish step then
// uses the owner's local Git and gh credentials, downloads no modules, and runs in this order:
// fast-forward main, push the tag, create the private GitHub Release with the artifacts. A failed
// main push leaves nothing published, so the same command can run again. main moves only
// forward and nothing is force-pushed.
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import {
  cleanEnv, commonDir, credentialFreeEnv, git, gitOut, isAncestor, pushRef, resolveCommit, run, stripGitEnv, toplevel,
} from './git.mjs';

stripGitEnv();

export const tagPattern = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$/;
const notesRef = 'refs/notes/foundation';
const remoteNotesRef = 'refs/notes/remote/foundation';

const sha256 = (path) => createHash('sha256').update(readFileSync(path)).digest('hex');

function fail(message) {
  throw new Error(message);
}

// Fetches a remote branch into its tracking ref; null when the remote has no such branch.
function fetchBranch(root, remote, branch) {
  const listed = git(root, ['ls-remote', '--exit-code', '--heads', remote, `refs/heads/${branch}`]);
  if (listed.status === 2) return null;
  if (listed.status !== 0) fail(`Cannot reach ${remote}: ${listed.stderr.trim()}`);
  gitOut(root, ['fetch', '--no-tags', remote, `+refs/heads/${branch}:refs/remotes/${remote}/${branch}`]);
  return resolveCommit(root, `refs/remotes/${remote}/${branch}`);
}

// The ancestry and tag conditions, evaluated against the remote's current dev, main and tags.
function checkReferences(root, { remote, commit, tag }) {
  const dev = fetchBranch(root, remote, 'dev');
  const main = fetchBranch(root, remote, 'main');
  if (!dev) fail(`${remote} has no dev branch.`);
  if (!isAncestor(root, commit, dev)) fail('The release commit must belong to dev.');
  if (main && !isAncestor(root, main, commit)) {
    fail('The release must fast-forward main; an older or divergent commit cannot replace it.');
  }
  const remoteTag = git(root, ['ls-remote', '--tags', remote, `refs/tags/${tag}`]);
  if (remoteTag.status !== 0) fail(`Cannot reach ${remote}: ${remoteTag.stderr.trim()}`);
  if (remoteTag.stdout.trim()) fail(`The tag ${tag} already exists on ${remote}. Tags are immutable; choose a new version.`);
  const localTag = resolveCommit(root, `refs/tags/${tag}`);
  if (localTag && localTag !== commit) fail(`The local tag ${tag} identifies a different commit.`);
  return { dev, main };
}

// The recorded foundation evidence for the exact commit and tree, from the local and fetched notes.
function recordedEvidence(root, remote, commit) {
  if (git(root, ['ls-remote', '--exit-code', remote, notesRef]).status === 0) {
    gitOut(root, ['fetch', '--no-tags', remote, `+${notesRef}:${remoteNotesRef}`]);
  }
  const tree = gitOut(root, ['rev-parse', `${commit}^{tree}`]);
  for (const ref of [notesRef, remoteNotesRef]) {
    const shown = git(root, ['notes', `--ref=${ref}`, 'show', commit]);
    if (shown.status !== 0) continue;
    for (const line of shown.stdout.split('\n').filter(Boolean)) {
      try {
        const evidence = JSON.parse(line);
        if (evidence.result === 'success' && evidence.head === commit && evidence.tree === tree) return evidence;
      } catch {
        // A note line that is not JSON is not evidence.
      }
    }
  }
  return null;
}

// Runs foundation again in a clean work tree of exactly the release commit.
function verifyCommit(root, commit, { writeNote, log }) {
  const parent = mkdtempSync(join(tmpdir(), 'lina-release-'));
  const worktree = join(parent, 'candidate');
  gitOut(root, ['worktree', 'add', '--quiet', '--detach', worktree, commit]);
  try {
    log(`No foundation evidence for ${commit.slice(0, 12)}; running foundation on a clean work tree of that commit.`);
    const args = [join(worktree, 'scripts/foundation.mjs'), '--full', ...(writeNote ? [] : ['--no-note'])];
    const result = run(process.execPath, args, { cwd: worktree, env: credentialFreeEnv() });
    if (result.stderr) log(result.stderr.trimEnd());
    if (result.status !== 0) fail('Foundation failed on the release commit.');
    return JSON.parse(result.stdout.trim().split('\n').filter(Boolean).at(-1));
  } finally {
    git(root, ['worktree', 'remove', '--force', worktree]);
    rmSync(parent, { recursive: true, force: true });
  }
}

function ghRun(root, args) {
  try {
    return run('gh', args, { cwd: root, env: cleanEnv() });
  } catch (error) {
    if (error.code === 'ENOENT') fail('The gh command is required for the GitHub Release.');
    throw error;
  }
}

function requirePrivateRepository(root) {
  const viewed = ghRun(root, ['repo', 'view', '--json', 'isPrivate', '--jq', '.isPrivate']);
  if (viewed.status !== 0) fail(`Cannot read the repository visibility with gh: ${viewed.stderr.trim()}`);
  if (viewed.stdout.trim() !== 'true') fail('The GitHub repository is not private. Release artifacts are private; make the repository private first.');
}

// Builds the artifacts from the exact commit without account credentials.
function build(root, { commit, tag, evidence, evidenceSource, out }) {
  const version = tag.slice(1);
  mkdirSync(out, { recursive: true });
  if (readdirSync(out).length) fail(`The output directory is not empty: ${out}`);
  const archive = join(out, `lina-${version}-source.tar.gz`);
  const archived = git(root, ['archive', '--format=tar.gz', `--prefix=lina-${version}/`, '-o', archive, commit], { env: credentialFreeEnv() });
  if (archived.status !== 0) fail(`git archive failed: ${archived.stderr.trim()}`);
  const manifest = join(out, 'release-manifest.json');
  const describe = (path) => ({ name: path.slice(out.length + 1), bytes: statSync(path).size, sha256: sha256(path) });
  writeFileSync(manifest, `${JSON.stringify({
    schema: 1, tag, version, prerelease: tag.includes('-'),
    commit, tree: gitOut(root, ['rev-parse', `${commit}^{tree}`]),
    commit_date: gitOut(root, ['show', '-s', '--format=%cI', commit]),
    foundation: { source: evidenceSource, result: evidence.result, mode: evidence.mode, finished: evidence.finished },
    artifacts: [describe(archive)],
  }, null, 2)}\n`);
  const sums = join(out, 'SHA256SUMS');
  writeFileSync(sums, [archive, manifest].map((path) => `${sha256(path)}  ${path.slice(out.length + 1)}`).join('\n') + '\n');
  return [archive, manifest, sums];
}

/**
 * Runs the release checks and the build; with `publish` it then publishes. Returns a summary.
 * Throws on the first failed condition, and a failed condition never leaves a published change.
 */
export function release({
  cwd = process.cwd(), commit, tag, notesFile, publish = false, remote = 'origin', outDir, log = console.error,
}) {
  if (!/^[0-9a-f]{40}$/.test(commit ?? '')) fail('Provide --commit as an exact 40-character commit id.');
  if (!tagPattern.test(tag ?? '')) fail('Provide --tag as a version such as v0.1.0 or v0.1.0-rc.1.');
  if (!notesFile) fail('Provide --notes-file with the user-facing release notes.');
  if (!/\S/.test(readFileSync(notesFile, 'utf8'))) fail('The release notes are empty.');
  const root = toplevel(cwd);
  if (git(root, ['remote', 'get-url', remote]).status !== 0) fail(`There is no remote named ${remote}.`);
  if (resolveCommit(root, commit) !== commit) fail('The release commit is not in this repository; fetch it first.');

  checkReferences(root, { remote, commit, tag });
  let evidence = recordedEvidence(root, remote, commit);
  const evidenceSource = evidence ? 'note' : 'rerun';
  evidence ??= verifyCommit(root, commit, { writeNote: publish, log });
  requirePrivateRepository(root);
  // The default directory lives in the Git directory and is rebuilt on every run; a directory the
  // caller names must be empty, so a release never deletes anything it did not create.
  const out = outDir ?? join(commonDir(root), 'lina-release', tag);
  if (!outDir) rmSync(out, { recursive: true, force: true });
  const files = build(root, { commit, tag, evidence, evidenceSource, out });
  const summary = {
    mode: publish ? 'publish' : 'dry-run', commit, tag, evidence: evidenceSource, artifacts: files,
    steps: ['push main (fast-forward)', `push ${tag}`, 'create the private GitHub Release'],
  };
  if (!publish) return summary;

  // Publish step: owner credentials, no module downloads, nothing built.
  checkReferences(root, { remote, commit, tag });
  const main = pushRef(root, remote, `${commit}:refs/heads/main`);
  if (!main.ok) fail(`Fast-forwarding main failed${main.rejected ? ' because main moved' : ''}; nothing was published. ${main.stderr}`);
  const pushedTag = pushRef(root, remote, `${commit}:refs/tags/${tag}`);
  if (!pushedTag.ok) fail(`main now identifies ${commit}, but pushing ${tag} failed: ${pushedTag.stderr}`);
  const args = ['release', 'create', tag, ...files, '--verify-tag', '--title', tag, '--notes-file', notesFile];
  if (tag.includes('-')) args.push('--prerelease');
  const created = ghRun(root, args);
  if (created.status !== 0) {
    fail(`main and ${tag} are published, but creating the GitHub Release failed: ${created.stderr.trim()}\nCreate it with: gh ${args.join(' ')}`);
  }
  return summary;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const { values } = parseArgs({ options: {
      commit: { type: 'string' }, tag: { type: 'string' }, 'notes-file': { type: 'string' },
      publish: { type: 'boolean', default: false }, remote: { type: 'string', default: 'origin' }, out: { type: 'string' },
    } });
    const summary = release({
      commit: values.commit, tag: values.tag, notesFile: values['notes-file'],
      publish: values.publish, remote: values.remote, outDir: values.out,
    });
    console.log(JSON.stringify(summary));
  } catch (error) {
    console.error(`release: ${error.message}`);
    process.exitCode = 1;
  }
}
