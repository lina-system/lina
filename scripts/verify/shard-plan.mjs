// SPDX-License-Identifier: Apache-2.0
import { existsSync, readFileSync } from 'node:fs';
import { isAbsolute, join, posix, relative } from 'node:path';
import { createGoContext, execute } from './check-components.mjs';
import { kind, registryFor } from './scope.mjs';
import { scanScenarios } from './scenarios.mjs';

// Longest measured work first. A new package still gets a shard; timings are
// scheduling hints, never evidence that a test can be omitted or cached.
export function balance(items, count) {
  if (!Array.isArray(items) || !Number.isInteger(count) || count < 1) throw new Error('Invalid shard inventory or count');
  const ids = new Set();
  const weighted = items.map(item => {
    if (typeof item.id !== 'string' || !item.id || ids.has(item.id)) throw new Error('Shard item IDs must be unique');
    ids.add(item.id);
    const seconds = item.seconds ?? 1;
    if (!Number.isFinite(seconds) || seconds < 0) throw new Error(`Invalid timing for ${item.id}`);
    return { item, seconds };
  }).sort((a, b) => b.seconds - a.seconds || (a.item.id < b.item.id ? -1 : 1));
  const buckets = Array.from({ length: Math.min(count, items.length) }, () => ({ items: [], seconds: 0 }));
  for (const { item, seconds } of weighted) {
    const target = buckets.reduce((best, bucket) => bucket.seconds < best.seconds
      || (bucket.seconds === best.seconds && bucket.items.length < best.items.length) ? bucket : best);
    target.items.push(item);
    target.seconds += seconds;
  }
  return buckets.filter(bucket => bucket.items.length).map(bucket => bucket.items);
}

// Dependencies come from Go, and scenario inputs include their source and
// helper/fixture files. A deleted or otherwise unaccounted input selects all.
export function selectScenarios({ scenarios, packages, changed, full = false, shared = false }) {
  const all = reason => ({ selected: [...scenarios], reason });
  if (full) return all('full');
  if (shared) return all('shared');
  if (!changed.length) return all('empty-diff');
  const byPath = new Map(packages.map(pkg => [pkg.path, pkg]));
  const directories = [...packages].sort((a, b) => b.directory.length - a.directory.length);
  const inputsOf = scenario => [scenario.file, ...(scenario.inputs ?? [])];
  const inputs = new Set(scenarios.flatMap(inputsOf));
  const changedPackages = new Set();
  for (const path of changed) {
    if (inputs.has(path)) continue;
    const pkg = directories.find(pkg => posix.dirname(path) === pkg.directory
      || path.startsWith(`${pkg.directory}/testdata/`) || pkg.files?.includes(path));
    if (!pkg) return all('unmapped-input');
    changedPackages.add(pkg.path);
  }
  const reachesChange = scenario => {
    if (inputsOf(scenario).some(input => changed.includes(input))) return true;
    const reached = new Set([...(scenario.imports ?? []), ...(scenario.binaries ?? [])]);
    for (const path of reached) {
      if (changedPackages.has(path)) return true;
      for (const dependency of byPath.get(path)?.dependencies ?? []) reached.add(dependency);
    }
    return false;
  };
  return { selected: scenarios.filter(reachesChange), reason: 'affected-inputs' };
}

const goObjects = text => JSON.parse(`[${text.trim().replace(/^}\s*\n{/gm, '},{')}]`);
const portable = path => path.replaceAll('\\', '/');
const owns = (component, directory) => component.files.some(file => posix.dirname(file) === directory)
  || component.directories.some(prefix => `${directory}/`.startsWith(prefix));

export function readInventory(cwd) {
  const registry = registryFor(cwd).filter(component => [...component.files, ...component.directories]
    .some(path => existsSync(join(cwd, path))));
  const context = createGoContext(cwd);
  const graph = new Map();
  const owned = new Set();
  try {
    for (const module of new Set(registry.map(component => component.module))) {
      const patterns = [...new Set(registry.filter(component => component.module === module).flatMap(component => component.packages))];
      const result = execute('go', ['list', '-buildvcs=false', '-mod=readonly', '-deps', '-test', '-json', ...patterns],
        { cwd: join(cwd, module), env: context.env, timeoutMs: 120_000 });
      if (result.status !== 0) throw new Error(`Cannot inventory Go packages: ${result.stderr}`);
      for (const pkg of goObjects(result.stdout)) {
        if (pkg.Error || pkg.DepsErrors?.length) throw new Error(`Cannot inventory ${pkg.ImportPath}: ${JSON.stringify(pkg.Error ?? pkg.DepsErrors)}`);
        // The generated test main and ForTest variants are compilation inputs,
        // not independently executable package shards.
        if (pkg.ForTest || (pkg.Name === 'main' && pkg.GoFiles?.some(isAbsolute))) continue;
        const directory = portable(relative(cwd, pkg.Dir));
        const inside = !isAbsolute(directory) && directory !== '..' && !directory.startsWith('../');
        const sourceFiles = inside ? [...new Set(['GoFiles', 'CgoFiles', 'TestGoFiles', 'XTestGoFiles']
          .flatMap(key => pkg[key] ?? []).map(file => posix.join(directory, file)))].sort() : [];
        const record = {
          path: pkg.ImportPath, directory: inside ? directory || '.' : '',
          module: inside ? portable(relative(cwd, pkg.Module?.Dir ?? cwd)) || '.' : null,
          dependencies: pkg.Imports ?? [], sourceFiles,
          files: [...sourceFiles, ...(inside ? ['EmbedFiles', 'TestEmbedFiles', 'XTestEmbedFiles']
            .flatMap(key => pkg[key] ?? []).map(file => posix.join(directory, file)) : [])],
        };
        graph.set(record.path, record);
        if (inside && pkg.Match?.length) owned.add(record.path);
      }
    }
  } finally {
    context.close();
  }
  return { registry, packages: [...graph.values()], owned: [...owned] };
}

function componentGroups(items, registry) {
  return registry.map(component => ({ ...component,
    packages: items.filter(item => item.component === component.name).map(item => item.id).sort(),
  })).filter(component => component.packages.length);
}

// The same -run expression cannot distinguish identical names from internal
// and external test packages. Join just the connected files so each declaration
// still runs once and unrelated files retain their own shards.
export function distinctScenarios(scenarios) {
  const result = [];
  for (const pkg of new Set(scenarios.map(scenario => scenario.package))) {
    const remaining = scenarios.filter(scenario => scenario.package === pkg);
    while (remaining.length) {
      const group = [remaining.shift()];
      const names = new Set(group[0].tests);
      for (let index = 0; index < remaining.length;) {
        if (remaining[index].tests.some(name => names.has(name))) {
          const [file] = remaining.splice(index, 1);
          group.push(file);
          for (const name of file.tests) names.add(name);
          index = 0;
        } else index++;
      }
      const union = key => [...new Set(group.flatMap(scenario => scenario[key]))].sort();
      result.push({ ...group[0], files: [...new Set(group.flatMap(scenario => scenario.files ?? [scenario.file]))].sort(),
        tests: [...names].sort(), imports: union('imports'), inputs: union('inputs'), binaries: union('binaries'),
        unknownBinary: group.some(scenario => scenario.unknownBinary) });
    }
  }
  return result;
}

export function planComponents({ cwd, scope, inventory = readInventory, unitCount = 4,
  e2eCounts = { m1: 6, m2: 2, m3: 1 }, timings }) {
  if (!scope.components) return { tasks: [], selectedComponents: [], scenarios: [], e2eReason: 'no-components' };
  const data = inventory(cwd);
  const byPath = new Map(data.packages.map(pkg => [pkg.path, pkg]));
  const selectedNames = new Set(scope.selectedComponents);
  const metadata = scanScenarios({ cwd, packages: data.packages });
  const scenarios = distinctScenarios(metadata.scenarios);
  const changed = scope.changed.filter(path => kind(path) !== 'docs');
  const choice = selectScenarios({ scenarios, packages: data.packages, changed,
    full: scope.automation || scope.dependencies || ['manual-full', 'empty-diff', 'base-unavailable', 'unmapped-paths'].includes(scope.reason),
    shared: changed.some(path => metadata.sharedFiles.includes(path)) || scenarios.some(scenario => scenario.unknownBinary),
  });
  for (const scenario of choice.selected) {
    const owner = data.registry.find(component => owns(component, byPath.get(scenario.package).directory));
    if (!owner) throw new Error(`Unregistered acceptance package ${scenario.package}`);
    selectedNames.add(owner.name);
  }
  const selected = data.registry.filter(component => selectedNames.has(component.name));
  if (selected.length !== selectedNames.size) throw new Error('Unknown selected component');
  if (!timings) {
    const path = join(cwd, 'scripts/verify/test-times.json');
    if (existsSync(path)) {
      timings = JSON.parse(readFileSync(path, 'utf8'));
      if (timings?.schema !== 1 || ['packages', 'scenarios'].some(key => !timings[key]
          || typeof timings[key] !== 'object' || Array.isArray(timings[key])
          || Object.values(timings[key]).some(value => !Number.isFinite(value) || value < 0))) {
        throw new Error('Invalid timing inventory');
      }
    } else timings = {};
  }
  const tasks = [{ id: 'build', kind: 'build', components: selected }];
  const units = data.owned.map(path => byPath.get(path)).filter(pkg => pkg.directory !== 'e2e' && !pkg.directory.startsWith('e2e/'))
    .map(pkg => ({ ...pkg, component: selected.find(component => owns(component, pkg.directory))?.name }))
    .filter(pkg => pkg.component).map(pkg => ({ id: pkg.path, component: pkg.component, seconds: timings.packages?.[pkg.path] }));
  for (const [index, items] of balance(units, unitCount).entries()) {
    tasks.push({ id: `unit-${index + 1}`, kind: 'unit', components: componentGroups(items, selected) });
  }
  const counters = new Map();
  for (const pkg of [...new Set(choice.selected.map(scenario => scenario.package))].sort()) {
    const files = choice.selected.filter(scenario => scenario.package === pkg);
    for (const milestone of [...new Set(files.map(scenario => scenario.milestone))].sort()) {
      const items = files.filter(scenario => scenario.milestone === milestone).map(scenario => ({
        ...scenario, id: scenario.file, seconds: scenario.files.reduce((sum, file) => sum + (timings.scenarios?.[file] ?? 1), 0),
      }));
      for (const shard of balance(items, e2eCounts[milestone] ?? 2)) {
        const number = (counters.get(milestone) ?? 0) + 1;
        counters.set(milestone, number);
        const tests = shard.flatMap(scenario => scenario.tests).sort();
        const owner = selected.find(component => owns(component, byPath.get(pkg).directory));
        tasks.push({ id: `e2e-${milestone}-${number}`, kind: 'e2e',
          components: [{ ...owner, packages: [pkg] }], tests,
          testPattern: tests.length ? `^(${tests.join('|')})$` : '^$', files: shard.flatMap(scenario => scenario.files).sort() });
      }
    }
  }
  return { tasks, selectedComponents: selected.map(component => component.name), scenarios: choice.selected, e2eReason: choice.reason };
}
