// Classifies a change into the checks it needs. The path map below is the executable registry:
// a path that no entry covers is unmapped, and the gate refuses to pass until it is registered.
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { git, gitOut, resolveCommit } from '../git.mjs';

const documents = new Set([
  'README.md', 'MANIFESTO.md', 'LICENSE', 'THIRD-PARTY-NOTICES.md', 'SECURITY.md', 'AGENTS.md',
]);
const configuration = new Set(['.editorconfig', '.gitattributes', '.gitignore']);

export function kind(path) {
  if (documents.has(path) || /^docs\/(?:policy|design)\/[^/]+\.md$/.test(path)) return 'docs';
  if (configuration.has(path) || /^scripts\/(?:verify\/)?[^/]+\.(?:mjs|sh)$/.test(path)) return 'automation';
  return 'unmapped';
}

// The files that exist for the candidate. When the candidate is the checked-out commit, the work
// tree is the candidate: tracked files that are present and untracked files that are not ignored.
// Any other candidate is read from its commit tree.
function inventoryOf(cwd, head) {
  if (resolveCommit(cwd, 'HEAD') === head) {
    return gitOut(cwd, ['ls-files', '-z', '--cached', '--others', '--exclude-standard']).split('\0')
      .filter(Boolean).filter((name) => existsSync(join(cwd, name)));
  }
  return gitOut(cwd, ['ls-tree', '-r', '--name-only', '-z', head]).split('\0').filter(Boolean);
}

/**
 * Selection for the candidate `head`. `base` is the commit the change is measured from; `full`
 * selects every check regardless of the changed paths. Deleted paths and both sides of a rename
 * participate in the changed set, so a removed file cannot hide behind a document-only diff.
 */
export function scopeFor({ base = '', head, full = false, cwd = process.cwd() }) {
  const resolvedHead = resolveCommit(cwd, head);
  if (!resolvedHead) throw new Error(`Unknown candidate revision: ${head}`);
  const inventory = inventoryOf(cwd, resolvedHead);
  let resolvedBase = null;
  let changed = [];
  if (!full && base) {
    resolvedBase = resolveCommit(cwd, base);
    if (resolvedBase) {
      const diff = git(cwd, ['diff', '--name-only', '--no-renames', '-z', resolvedBase, resolvedHead, '--']);
      if (diff.status === 0) changed = diff.stdout.split('\0').filter(Boolean);
      else resolvedBase = null;
    }
  }
  // Only files that exist can be unregistered product code; a deleted path with no mapping only
  // keeps the change out of the documents-only class.
  const unmapped = [...new Set(inventory.filter((path) => kind(path) === 'unmapped'))].sort();
  const docsOnly = !full && resolvedBase !== null
    && changed.length > 0 && changed.every((path) => kind(path) === 'docs') && unmapped.length === 0;
  const reason = unmapped.length ? 'unmapped-paths'
    : full ? 'manual-full'
    : resolvedBase === null ? 'base-unavailable'
    : changed.length === 0 ? 'empty-diff'
    : docsOnly ? 'documents-only' : 'automation-change';
  return {
    mode: docsOnly ? 'docs' : 'full', reason,
    base: resolvedBase, head: resolvedHead,
    docs: true, automation: !docsOnly, changed, unmapped,
  };
}
