// SPDX-License-Identifier: Apache-2.0
import { existsSync, writeFileSync } from 'node:fs';
import { join, posix } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createGoContext, execute } from './check-components.mjs';

export function scanScenarios({ cwd, packages }) {
  const acceptance = packages.filter(pkg => pkg.directory === 'e2e' || pkg.directory.startsWith('e2e/'));
  if (!acceptance.length) return { scenarios: [], sharedFiles: [] };
  const context = createGoContext(cwd);
  let files;
  try {
    const request = join(context.directory, 'scenarios.json');
    writeFileSync(request, JSON.stringify({ Root: cwd, Packages: acceptance.map(pkg => ({ Path: pkg.path, Files: pkg.sourceFiles })) }));
    const result = execute('go', ['run', fileURLToPath(new URL('./scenarios.go', import.meta.url)), request],
      { cwd, env: context.env, timeoutMs: 120_000 });
    if (result.status !== 0) throw new Error(`Cannot parse acceptance scenarios: ${result.stderr}`);
    files = JSON.parse(result.stdout);
  } finally {
    context.close();
  }
  const commands = new Map(packages.map(pkg => [pkg.directory, pkg.path]));
  const scenarios = [];
  for (const file of files.filter(file => file.Tests.length)) {
    // Go permits separate internal and external test packages in a directory.
    const siblings = files.filter(other => other.Package === file.Package && other.Name === file.Name);
    const declarations = siblings.flatMap(source => (source.Declarations ?? []).map(value => ({ ...value, source })));
    const byName = new Map();
    for (const declaration of declarations) {
      const matches = byName.get(declaration.Name) ?? [];
      matches.push(declaration);
      byName.set(declaration.Name, matches);
    }
    const reached = new Set(declarations.filter(value => value.source === file || value.Global));
    for (const declaration of reached) {
      const references = new Set(declaration.References);
      // A subprocess can select a helper test through a literal -test.run flag.
      for (const name of byName.keys()) {
        if (/^(Test|Fuzz|Example)/.test(name) && declaration.Strings.some(text => text.includes(name))) references.add(name);
      }
      for (const name of references) for (const dependency of byName.get(name) ?? []) reached.add(dependency);
    }
    const sources = new Set([file, ...[...reached].map(value => value.source)]);
    const inputs = new Set([...sources].map(source => source.Path));
    const binaries = new Set();
    let unknownBinary = false;
    for (const declaration of reached) {
      unknownBinary ||= declaration.UnknownBinary === true;
      for (const command of declaration.Binaries) {
        const path = commands.get(command);
        if (path) binaries.add(path);
        else unknownBinary = true;
      }
      for (const text of declaration.Strings) {
        if (text.includes('\0') || text.includes('\n') || text.length > 512) continue;
        const path = posix.normalize(posix.join(posix.dirname(declaration.source.Path), text));
        if (!path.startsWith('../') && existsSync(join(cwd, path))) inputs.add(path);
      }
    }
    const tests = file.Tests;
    const milestone = tests.some(name => /^TestM3/.test(name)) ? 'm3'
      : tests.some(name => /^TestM2/.test(name)) ? 'm2'
      : file.Path.match(/^e2e\/(m\d+)\//)?.[1] ?? 'm1';
    scenarios.push({
      file: file.Path, package: file.Package, tests, milestone,
      imports: [...new Set([...sources].flatMap(source => source.Imports ?? []))].sort(),
      binaries: [...binaries].sort(), inputs: [...inputs].sort(), unknownBinary,
    });
  }
  for (const pkg of acceptance) {
    if (scenarios.some(scenario => scenario.package === pkg.path)) continue;
    const sources = files.filter(file => file.Package === pkg.path);
    if (!sources.length) throw new Error('Acceptance package has no source inventory: ' + pkg.path);
    const declarations = sources.flatMap(source => source.Declarations ?? []);
    const targets = [...new Set(declarations.flatMap(declaration => declaration.Binaries))];
    scenarios.push({
      file: sources[0].Path, files: sources.map(source => source.Path).sort(), package: pkg.path, tests: [],
      milestone: pkg.directory.match(/^e2e\/(m\d+)/)?.[1] ?? 'm1',
      inputs: sources.map(source => source.Path).sort(),
      imports: [...new Set(sources.flatMap(source => source.Imports ?? []))].sort(),
      binaries: targets.map(target => commands.get(target)).filter(Boolean).sort(),
      unknownBinary: targets.some(target => !commands.has(target)) || declarations.some(declaration => declaration.UnknownBinary),
    });
  }
  return { scenarios, sharedFiles: files.filter(file => !file.Tests.length).map(file => file.Path).sort() };
}
