// SPDX-License-Identifier: Apache-2.0
// Classifies a change into the checks it needs. The path map below is the executable registry:
// a path that no entry covers is unmapped, and the gate refuses to pass until it is registered.
import { existsSync } from 'node:fs';
import { join, relative } from 'node:path';
import { git, gitOut, resolveCommit } from '../git.mjs';
import { createGoContext, execute } from './check-components.mjs';

const documents = new Set([
  'README.md', 'MANIFESTO.md', 'LICENSE', 'THIRD-PARTY-NOTICES.md', 'SECURITY.md', 'AGENTS.md', 'CONTRIBUTING.md', 'NOTICE', 'docs/qa.md',
]);
const configuration = new Set(['.editorconfig', '.gitattributes', '.gitignore', '.node-version', '.npmrc', 'package.json', 'package-lock.json', '.devflow']);
const modules = new Set(['go.mod', 'go.sum', 'go.work', 'go.work.sum', 'kit/go.mod', 'kit/go.sum']);

export function registryFor(cwd) {
  // Older trees and minimal verification fixtures may not have these packages yet.
  const architecture = existsSync(join(cwd, 'internal/archtest')) ? ['./internal/archtest/...'] : [];
  const buildinfo = existsSync(join(cwd, 'internal/buildinfo')) ? ['./internal/buildinfo/...'] : [];
  const core = existsSync(join(cwd, 'internal/core')) ? ['./internal/core/...'] : [];
  const addonDirectories = ['internal/addons/importer/hermes/'];
  const addonPackages = addonDirectories.filter(directory => existsSync(join(cwd, directory)))
    .map(directory => `./${directory}...`);
  const skills = existsSync(join(cwd, 'skills')) ? ['./skills/...'] : [];
  const persona = existsSync(join(cwd, 'persona')) ? ['./persona/...'] : [];
  const vendored = existsSync(join(cwd, 'third_party/codex-app-server/upstream')) ? ['./third_party/codex-app-server/upstream/...'] : [];
  const clientDirectories = ['internal/design/', 'internal/tui/', 'internal/clientapi/', 'internal/fakecore/'];
  const clientPackages = clientDirectories.filter(directory => existsSync(join(cwd, directory)))
    .map(directory => `./${directory}...`);
  // The harness launches lina as a subprocess, so go list cannot discover its
  // dependency on Core and the TUI. Keep it in their component so either side
  // selects the acceptance suite, including transitive kit changes.
  const e2e = existsSync(join(cwd, 'e2e')) ? ['./e2e/...'] : [];
  return [
    { name: 'storebench', module: '.', directories: ['internal/core/storebench/'], files: [], packages: ['./internal/core/storebench/...'] },
    { name: 'crw-takeover', module: '.', directories: ['tools/crw-takeover/'], files: [], packages: ['./tools/crw-takeover/...'] },
    { name: 'kit', module: 'kit', directories: ['kit/', 'protocol/schema/', 'protocol/fixtures/'], files: [], packages: ['./...'] },
    { name: 'lina', module: '.', directories: ['cmd/lina/', 'internal/archtest/', 'internal/buildinfo/', 'internal/core/', 'e2e/', 'persona/', 'skills/', 'testdata/korean/', 'testdata/hermes/', 'testdata/crw/', 'testdata/provider-replay/', ...addonDirectories, ...clientDirectories, 'third_party/codex-app-server/'], files: ['doc.go'], packages: ['.', './cmd/lina/...', ...architecture, ...buildinfo, ...persona, ...skills, ...core, ...addonPackages, ...clientPackages, ...e2e, ...vendored], binary: 'lina' },
    { name: 'moirai-worker', module: '.', directories: ['cmd/moirai-worker/', 'internal/moirai/'], files: [], packages: ['./cmd/moirai-worker/...', './internal/moirai/...'], binary: 'moirai-worker' },
    { name: 'lina-node', module: '.', directories: ['cmd/lina-node/'], files: [], packages: ['./cmd/lina-node/...'], binary: 'lina-node' },
  ];
}

export const componentRegistry = registryFor(process.cwd());

function componentOf(path) {
  return componentRegistry.find(component => component.files.includes(path)
    || component.directories.some(directory => path.startsWith(directory)));
}

// Go emits adjacent, indented JSON objects, with closing braces at column zero.
const goObjects = text => JSON.parse(`[${text.trim().replace(/^}\s*\n{/gm, '},{')}]`);

export function kind(path) {
  if (['scripts/verify/test-times.json', 'scripts/verify/bwrap.apparmor'].includes(path)) return 'automation';
  if (path.startsWith('adapters/claude/') || path.startsWith('adapters/claude-acp/')) return 'automation';
  if (path.startsWith('third_party/')) return 'dependencies';
  if (path === 'packaging/README.md') return 'docs';
  if (path === 'packaging/sqlite-vec/build.sh') return 'automation';
  if (/^packaging\/sqlite-vec\/(?:README|NOTICE)\.md$/.test(path)) return 'docs';
  if (path === 'scripts/verify/dependency-exceptions.json' || path === 'THIRD-PARTY-NOTICES.md') return 'dependencies';
  if (modules.has(path)) return 'modules';
  if (path === 'DESIGN.md' || /^design\/tokens\/[^/]+\.(?:json|css|svg|swift)$/.test(path)) return 'automation';
  if (/^evidence\/(?:brand|generated-ui)\/[^/]+\.png$/.test(path)) return 'docs';
  if (componentOf(path)) return 'components';
  if (documents.has(path) || /^docs\/(?:policy|design|research)\/[^/]+\.md$/.test(path)
      || /^evidence\/.+\.(?:json|md)$/.test(path)) return 'docs';
  if (/^\.github\/workflows\/[^/]+\.ya?ml$/.test(path)) return 'automation';
  if (path === '.github/PULL_REQUEST_TEMPLATE.md' || /^\.github\/ISSUE_TEMPLATE\/[^/]+\.ya?ml$/.test(path)) return 'docs';
  if (configuration.has(path) || /^scripts\/(?:verify\/)?[^/]+\.(?:mjs|sh|go)$/.test(path)) return 'automation';
  return 'unmapped';
}

// Let Go parse imports, including internal/external test packages. Package names alone are not
// component boundaries: use each package's source directory from the module being inspected.
function importers(cwd, active) {
  const context = createGoContext(cwd);
  try {
    const listedModules = execute('go', ['list', '-m', '-json'], { cwd, env: context.env });
    if (listedModules.status !== 0) throw new Error(`Cannot read Go modules: ${listedModules.stderr}`);
    const workspace = goObjects(listedModules.stdout).sort((a, b) => b.Path.length - a.Path.length);
    const ownerOfImport = imported => {
      const module = workspace.find(module => imported === module.Path || imported.startsWith(`${module.Path}/`));
      if (!module?.Dir) return undefined;
      const directory = relative(cwd, join(module.Dir, imported.slice(module.Path.length))).replaceAll('\\', '/');
      return componentOf(directory ? `${directory}/package.go` : 'doc.go')?.name;
    };
    const packages = [];
    for (const module of new Set(active.map(component => component.module))) {
      const patterns = [...new Set(active.filter(component => component.module === module).flatMap(component => component.packages))];
      const result = execute('go', ['list', '-e', '-mod=readonly', '-json', ...patterns], { cwd: join(cwd, module), env: context.env });
      if (result.status !== 0) throw new Error(`Cannot read component imports: ${result.stderr}`);
      for (const pkg of goObjects(result.stdout)) {
        if (pkg.Error) throw new Error(`Cannot read component imports: ${pkg.Error.Err}`);
        const directory = `${relative(cwd, pkg.Dir).replaceAll('\\', '/')}/`;
        const owner = directory === '/' ? componentOf('doc.go') : componentOf(`${directory}package.go`);
        if (owner) packages.push({ ...pkg, owner: owner.name });
      }
    }
    const owners = new Map(packages.map(pkg => [pkg.ImportPath, pkg.owner]));
    const edges = new Map(active.map(component => [component.name, new Set()]));
    for (const pkg of packages) {
      for (const imported of [...(pkg.Imports ?? []), ...(pkg.TestImports ?? []), ...(pkg.XTestImports ?? [])]) {
        // A deleted package still has importers: its module path identifies the owner even
        // when go list can no longer return a package record for that directory.
        const dependency = owners.get(imported) ?? ownerOfImport(imported);
        if (dependency && dependency !== pkg.owner) edges.get(dependency)?.add(pkg.owner);
      }
    }
    return edges;
  } finally {
    context.close();
  }
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
  if (resolveCommit(cwd, 'HEAD') === resolvedHead) {
    changed = [...new Set([...changed,
      ...gitOut(cwd, ['diff', '--name-only', '--no-renames', 'HEAD']).split('\n').filter(Boolean),
      ...gitOut(cwd, ['ls-files', '--others', '--exclude-standard']).split('\n').filter(Boolean),
    ])].sort();
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
    : docsOnly ? 'documents-only'
    : changed.some(path => ['modules', 'components'].includes(kind(path))) ? 'component-change'
    : changed.some(path => kind(path) === 'dependencies') ? 'dependency-change' : 'automation-change';
  const active = registryFor(cwd).filter(component => inventory.some(path => componentOf(path)?.name === component.name));
  const all = full || resolvedBase === null || changed.length === 0 || unmapped.length > 0
    || changed.some(path => ['automation', 'modules', 'unmapped'].includes(kind(path)))
    || changed.some(path => componentOf(path) && !active.some(component => component.name === componentOf(path).name));
  const selected = new Set(all ? active.map(component => component.name)
    : changed.map(path => componentOf(path)?.name).filter(name => active.some(component => component.name === name)));
  if (selected.size && !all) {
    if (resolveCommit(cwd, 'HEAD') !== resolvedHead) {
      // Never use the current checkout's imports as evidence about a different commit.
      for (const component of active) selected.add(component.name);
    } else {
      const edges = importers(cwd, active);
      for (const name of selected) for (const importer of edges.get(name) ?? []) selected.add(importer);
    }
  }
  // Architecture scans source files, so Go's import graph cannot express its dependency
  // on every component. Run it with any selected Go component, including kit-only changes.
  if (selected.size && inventory.some(path => path.startsWith('internal/archtest/'))) selected.add('lina');
  const selectedComponents = active.map(component => component.name).filter(name => selected.has(name));
  return {
    mode: docsOnly ? 'docs' : 'full', reason,
    base: resolvedBase, head: resolvedHead,
    docs: true, automation: all && !docsOnly,
    dependencies: changed.some(path => kind(path) === 'dependencies')
      || (all && !docsOnly && [...inventory, ...changed].some(path => modules.has(path) || path.startsWith('third_party/') || /(^|\/)package(-lock)?\.json$/.test(path))),
    components: selectedComponents.length > 0,
    selectedComponents, changed, unmapped,
  };
}
