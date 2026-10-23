#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Check vendored trees, Go pins, sums, vulnerabilities, licenses and notices. All Go calls are injectable.
import { existsSync, lstatSync, readdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { homedir, tmpdir } from 'node:os';
import { isAbsolute, join, relative, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';
import { execute } from './check-components.mjs';
import { checkNpm } from './check-npm.mjs';

const manifests = ['go.mod', 'kit/go.mod', 'go.work'];
const scannerTools = [
  { module: 'golang.org/x/vuln', path: 'golang.org/x/vuln/cmd/govulncheck' },
  { module: 'github.com/google/go-licenses/v2', path: 'github.com/google/go-licenses/v2' },
];
const allowedLicenses = new Set(['MIT', 'Apache-2.0', 'BSD-2-Clause', 'BSD-3-Clause', 'BSD-4-Clause', 'ISC']);
const exceptionFile = 'scripts/verify/dependency-exceptions.json';

function readExceptions(cwd) {
  try {
    const entries = JSON.parse(readFileSync(join(cwd, exceptionFile), 'utf8'));
    if (!Array.isArray(entries)) throw new Error('expected an array');
    const seen = new Set();
    for (const entry of entries) {
      const vulnerability = entry?.kind === 'vulnerability';
      const keys = ['kind', 'name', 'version', 'reason', 'reference', ...(vulnerability ? ['id'] : [])];
      if (!entry || (!vulnerability && entry.kind !== 'install-script')
          || Object.keys(entry).some(key => !keys.includes(key))
          || typeof entry.name !== 'string' || !/^[A-Za-z0-9@][A-Za-z0-9@._/+!-]*$/.test(entry.name)
          || !exactVersion(vulnerability ? entry.version : `v${entry.version}`)
          || (vulnerability && (typeof entry.id !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9.:-]*$/.test(entry.id)))
          || typeof entry.reason !== 'string' || !entry.reason.trim()) {
        throw new Error('entry needs an exact kind, name, version, vulnerability id and nonempty reason; no wildcards or unknown fields');
      }
      if (entry.reference !== undefined && (typeof entry.reference !== 'string'
          || !['https:', 'http:'].includes(new URL(entry.reference).protocol))) {
        throw new Error('reference must be an HTTP(S) URL');
      }
      const key = JSON.stringify([entry.kind, entry.name, entry.version, entry.id]);
      if (seen.has(key)) throw new Error(`duplicate exception for ${entry.name}@${entry.version}`);
      seen.add(key);
    }
    return entries;
  } catch (error) {
    throw new Error(`${exceptionFile}: ${error.message}`);
  }
}

// Both Go list and govulncheck emit adjacent indented JSON objects, ending at column zero.
function goObjects(source) {
  return JSON.parse(`[${source.trim().replace(/^}\s*\n{/gm, '},{')}]`);
}

function checkVulnerabilities(source, exceptions) {
  const messages = goObjects(source);
  if (messages[0]?.config?.protocol_version !== 'v1.0.0') throw new Error('govulncheck: missing or unsupported JSON protocol');
  for (const message of messages) {
    if (!message || typeof message !== 'object' || Object.keys(message).length !== 1
        || !['config', 'progress', 'SBOM', 'osv', 'finding'].includes(Object.keys(message)[0])) {
      throw new Error('govulncheck: malformed message');
    }
    if (!Object.hasOwn(message, 'finding')) continue;
    const { osv, trace } = message.finding ?? {};
    const frame = Array.isArray(trace) ? trace[0] : undefined;
    if (typeof osv !== 'string' || !osv || typeof frame?.module !== 'string' || !frame.module || !exactVersion(frame.version)) {
      throw new Error('govulncheck: malformed finding');
    }
    if (!exceptions.some(entry => entry.kind === 'vulnerability' && entry.name === frame.module
        && entry.version === frame.version && entry.id === osv)) {
      throw new Error(`unexcepted vulnerability ${osv}: ${frame.module}@${frame.version}`);
    }
  }
}

function noticeEntries(source) {
  // A heading identifies an entry; incidental mentions and empty headings are not notices.
  return new Set([...source.matchAll(/^## ([^\r\n]+)\r?\n([\s\S]*?)(?=^## |$(?![\s\S]))/gm)]
    .filter(match => match[2].trim()).map(match => match[1].trim()));
}

function checkLicenses(packageSource, reportSource, ownModules, notices) {
  const packages = goObjects(packageSource);
  if (!packages.length || packages.some(pkg => !pkg?.ImportPath || pkg.Error || pkg.DepsErrors
      || (!pkg.Standard && !pkg.Module?.Path))) throw new Error('malformed Go package inventory');
  const modules = new Map(packages.filter(pkg => !pkg.Standard).map(pkg => [pkg.Module.Path, pkg.Module]));
  const report = JSON.parse(reportSource);
  if (!Array.isArray(report)) throw new Error('malformed license report');
  const scanned = new Set();
  for (const library of report) {
    if (typeof library?.name !== 'string' || typeof library.license !== 'string') throw new Error('malformed license report');
    // Use the longest module path so a nested external module cannot be mistaken for LINA.
    const module = [...modules.values()].sort((a, b) => b.Path.length - a.Path.length)
      .find(module => library.name === module.Path || library.name.startsWith(`${module.Path}/`));
    if (!module) throw new Error(`license report names an unknown module: ${library.name}`);
    if (ownModules.has(module.Path)) continue;
    scanned.add(module.Path);
    if (!allowedLicenses.has(library.license)) throw new Error(`disallowed license ${library.license || '(empty)'}: ${library.name}`);
  }
  for (const module of modules.values()) {
    if (ownModules.has(module.Path)) continue;
    if (!scanned.has(module.Path)) throw new Error(`missing license scan for ${module.Path}`);
    // Versioned replacements name the actual upstream; local copies retain the import identity.
    const source = module.Replace?.Version ? module.Replace.Path : module.Path;
    if (!notices.has(source)) throw new Error(`missing THIRD-PARTY-NOTICES.md entry for ${source}`);
  }
}

function exactVersion(version) {
  if (typeof version !== 'string') return false;
  const match = version.match(/^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+incompatible)?$/);
  return Boolean(match) && !(match[1] ?? '').split('.').some(part => /^0\d+$/.test(part));
}

function localModule(root, directory, path) {
  if (typeof path !== 'string' || !(isAbsolute(path) || /^\.{1,2}(?:[\\/]|$)/.test(path))) return false;
  try {
    const target = realpathSync(resolve(directory, path));
    const name = relative(root, target);
    if (isAbsolute(name) || name === '..' || name.startsWith(`..${sep}`)) return false;
    return (name === '' || name === 'kit' || name.startsWith(`third_party${sep}`))
      && statSync(target).isDirectory() && existsSync(join(target, 'go.mod'));
  } catch {
    return false;
  }
}

function checkPins(data, file, root) {
  if (!data || typeof data !== 'object' || !data.Go || !data.Toolchain
      || (data.Require != null && !Array.isArray(data.Require))
      || (data.Replace != null && !Array.isArray(data.Replace))) {
    throw new Error(`${file}: malformed Go manifest response`);
  }
  for (const entry of data.Require ?? []) {
    if (!entry?.Path || !exactVersion(entry.Version)) {
      throw new Error(`${file}: require ${entry?.Path ?? ''} must name an exact version`);
    }
  }
  for (const entry of data.Replace ?? []) {
    const { Old, New } = entry ?? {};
    if (!Old?.Path || (Old.Version && !exactVersion(Old.Version))) {
      throw new Error(`${file}: replace selector must name an exact version when versioned`);
    }
    const directory = resolve(root, file, '..');
    if (!New?.Path || (New.Version ? !exactVersion(New.Version) : !localModule(root, directory, New.Path))) {
      throw new Error(`${file}: replace ${Old.Path} must name an exact version or an in-repository module directory`);
    }
  }
}

// Vendored inventories are checked offline before any Go command. A manifest is
// data, never a program: recorded generation commands are not executed by CI.
function checkVendoredTrees(cwd) {
  const root = join(cwd, 'third_party');
  if (!existsSync(root)) return 'vendored trees: 0 verified\n';
  const hash = bytes => createHash('sha256').update(bytes).digest('hex');
  const digest = value => typeof value === 'string' && /^[0-9a-f]{64}$/.test(value);
  const text = value => typeof value === 'string' && value.trim().length > 0;
  const safe = path => text(path) && !path.includes('\\') && !path.includes('\0')
    && !isAbsolute(path) && path.split('/').every(part => part && part !== '.' && part !== '..');
  const object = value => value && typeof value === 'object' && !Array.isArray(value);
  const require = (ok, message) => { if (!ok) throw new Error(message); };
  const walk = directory => {
    require(lstatSync(directory).isDirectory(), `not a regular directory: ${relative(cwd, directory)}`);
    return readdirSync(directory, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))
      .flatMap(entry => {
        require(entry.isDirectory() || entry.isFile(), `symlink or special file: ${entry.name}`);
        return entry.isDirectory() ? walk(join(directory, entry.name)).map(path => `${entry.name}/${path}`) : [entry.name];
      });
  };
  try {
    require(lstatSync(root).isDirectory(), 'third_party must be a regular directory');
    const notices = readFileSync(join(cwd, 'THIRD-PARTY-NOTICES.md'), 'utf8');
    const sections = new Map([...notices.matchAll(/^## ([^\r\n]+)\r?\n([\s\S]*?)(?=^## |$(?![\s\S]))/gm)]
      .map(match => [match[1].trim(), match[2]]));
    let count = 0;
    for (const name of readdirSync(root).sort()) {
      const tree = join(root, name);
      const all = walk(tree);
      require(all.includes('vendor.json'), `${name}: missing vendor.json`);
      const vendor = JSON.parse(readFileSync(join(tree, 'vendor.json'), 'utf8'));
      require(vendor.name === name && /^https:\/\//.test(vendor.source) && allowedLicenses.has(vendor.license), `${name}: invalid identity, source or license`);
      require(Array.isArray(vendor.scope) && vendor.scope.length && vendor.scope.every(safe)
        && new Set(vendor.scope).size === vendor.scope.length, `${name}: invalid scope`);
      require(object(vendor.files) && Object.keys(vendor.files).length > 0
        && Object.keys(vendor.files).every(safe), `${name}: invalid file inventory`);
      require(Array.isArray(vendor.patches), `${name}: missing patch inventory`);
      const recorded = Object.keys(vendor.files).sort();
      const actual = walk(join(tree, 'upstream')).sort();
      require(JSON.stringify(recorded) === JSON.stringify(actual), `${name}: upstream file inventory differs from vendor.json`);
      require(JSON.stringify([...vendor.scope].sort()) === JSON.stringify(recorded), `${name}: scope must enumerate the included upstream files`);
      require(actual.some(path => /(^|\/)LICENSE(?:\.[^/]*)?$/.test(path)), `${name}: missing upstream license`);
      const patched = new Set();
      const patchPaths = new Set();
      const patchIDs = new Set();
      for (const patch of vendor.patches) {
        require(object(patch) && text(patch.id) && !patchIDs.has(patch.id) && safe(patch.file)
          && patch.file.startsWith('patches/') && !patchPaths.has(patch.file) && digest(patch.sha256)
          && text(patch.reason) && text(patch.wrapperCannot) && Array.isArray(patch.files) && patch.files.length
          && patch.files.every(path => safe(path) && Object.hasOwn(vendor.files, path))
          && (patch.reference === undefined || /^https?:\/\//.test(patch.reference)), `${name}: incomplete patch record`);
        require(all.includes(patch.file) && hash(readFileSync(join(tree, patch.file))) === patch.sha256, `${name}: patch digest mismatch`);
        patchIDs.add(patch.id); patchPaths.add(patch.file);
        patch.files.forEach(path => patched.add(path));
      }
      require(all.filter(path => path.startsWith('patches/')).every(path => patchPaths.has(path)), `${name}: unrecorded patch`);
      for (const path of recorded) {
        const entry = vendor.files[path];
        require(object(entry) && digest(entry.upstream) && digest(entry.vendored), `${name}/${path}: invalid hashes`);
        require(entry.upstream === entry.vendored || patched.has(path), `${name}/${path}: changed without a patch record`);
        require(hash(readFileSync(join(tree, 'upstream', path))) === entry.vendored, `${name}/${path}: SHA-256 mismatch`);
      }
      let revision;
      if (vendor.generation !== undefined) {
        const generation = vendor.generation;
        require(object(generation) && Array.isArray(generation.tools) && generation.tools.length > 0
          && Array.isArray(generation.commands) && generation.commands.length > 0
          && generation.commands.every(text), `${name}: incomplete generation provenance`);
        const tools = new Set();
        for (const tool of generation.tools) {
          require(object(tool) && text(tool.name) && !tools.has(tool.name) && text(tool.version)
            && digest(tool.sha256), `${name}: incomplete generator tool`);
          tools.add(tool.name);
          if (tool.path !== undefined) {
            require(safe(tool.path) && all.includes(tool.path), `${name}: invalid generator path`);
            require(hash(readFileSync(join(tree, tool.path))) === tool.sha256, `${name}: generator digest mismatch`);
          }
        }
        revision = generation.tools[0].version;
      } else {
        require(typeof vendor.commit === 'string' && /^[0-9a-f]{40}$/.test(vendor.commit), `${name}: invalid upstream commit`);
        revision = vendor.commit;
      }
      const notice = sections.get(name) ?? '';
      require(notice.includes(vendor.source) && notice.includes(revision) && notice.includes(vendor.license)
        && vendor.patches.every(patch => notice.includes(patch.id)), `${name}: missing or mismatched notice revision/license/patches`);
      count++;
    }
    return `vendored trees: ${count} verified\n`;
  } catch (error) {
    throw new Error(`vendored trees: ${error.message}`);
  }
}

export function checkDependencies({ cwd = process.cwd(), execute: run = execute } = {}) {
  let output = '';
  let temporary;
  const invoke = (args, directory, env) => {
    const result = run('go', args, { cwd: directory, env });
    output += `$ go ${args.join(' ')}\n${result.stdout}${result.stderr}`;
    if (result.status !== 0) throw new Error(`go ${args.join(' ')} failed in ${relative(cwd, directory) || '.'}`);
    return result.stdout;
  };
  try {
    cwd = realpathSync(cwd);
    output += checkVendoredTrees(cwd);
    const exceptions = readExceptions(cwd);
    const npm = checkNpm({ cwd, execute: run, exceptions });
    output += npm.output;
    if (npm.status !== 0) throw new Error('npm verification failed');
    // Reject drift before Go can select or download a different toolchain.
    const versions = manifests.map(file => {
      const source = readFileSync(join(cwd, file), 'utf8');
      return { go: source.match(/^go\s+(\S+)/m)?.[1], toolchain: source.match(/^toolchain\s+(\S+)/m)?.[1] };
    });
    const { go, toolchain } = versions[0];
    if (!/^1\.\d+(?:\.\d+)?$/.test(go ?? '') || !/^go1\.\d+\.\d+$/.test(toolchain ?? '')
        || versions.some(value => value.go !== go || value.toolchain !== toolchain)) {
      throw new Error('go.mod, kit/go.mod and go.work must name the same Go and toolchain releases');
    }
    // Keep the public Go proxy/checksum defaults and share only the Go download/build caches.
    const inherited = Object.fromEntries(['PATH', 'SystemRoot', 'SYSTEMROOT', 'WINDIR', 'TMPDIR', 'TMP', 'TEMP', 'GOCACHE', 'GOMODCACHE', 'GOMAXPROCS']
      .filter(name => process.env[name] !== undefined).map(name => [name, process.env[name]]));
    const [gocache, gomodcache] = invoke(['env', 'GOCACHE', 'GOMODCACHE'], cwd,
      { ...inherited, HOME: homedir(), GOENV: 'off', GOTOOLCHAIN: 'local' }).trim().split(/\r?\n/);
    if (!gocache || !gomodcache) throw new Error('Go did not return its download/build cache paths');
    temporary = mkdtempSync(join(tmpdir(), 'lina-dependencies-'));
    const env = {
      ...inherited, HOME: temporary, USERPROFILE: temporary, XDG_CONFIG_HOME: temporary,
      GOENV: 'off', GOTOOLCHAIN: toolchain, GOWORK: join(cwd, 'go.work'), GOFLAGS: '-mod=readonly',
      GOCACHE: gocache, GOMODCACHE: gomodcache, CGO_ENABLED: '0',
      GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0',
    };
    if (invoke(['env', 'GOVERSION'], cwd, env).trim() !== toolchain) throw new Error(`Expected toolchain ${toolchain}`);
    // Go's parser handles blocks, comments and quoted paths; edit -json only reads the manifest.
    const ownModules = new Set();
    for (const file of manifests) {
      const directory = resolve(cwd, file, '..');
      const data = JSON.parse(invoke([file === 'go.work' ? 'work' : 'mod', 'edit', '-json'], directory, env));
      checkPins(data, file, cwd);
      if (file !== 'go.work') {
        if (!data.Module?.Path) throw new Error(`${file}: missing module path`);
        ownModules.add(data.Module.Path);
      }
      if (file === 'go.mod') {
        for (const tool of scannerTools) {
          if (!data.Tool?.some(entry => entry.Path === tool.path)
              || !data.Require?.some(entry => entry.Path === tool.module && exactVersion(entry.Version))) {
            throw new Error(`go.mod: declare and pin module tool ${tool.path}`);
          }
        }
      }
    }
    for (const directory of [cwd, join(cwd, 'kit')]) invoke(['mod', 'verify'], directory, env);
    const notices = noticeEntries(readFileSync(join(cwd, 'THIRD-PARTY-NOTICES.md'), 'utf8'));
    const template = join(temporary, 'licenses.tmpl');
    writeFileSync(template, '[{{range $i, $lib := .}}{{if $i}},{{end}}{"name":{{printf "%q" $lib.Name}},"license":{{printf "%q" $lib.LicenseName}}}{{end}}]');
    for (const directory of [cwd, join(cwd, 'kit')]) {
      // JSON mode exits successfully even with findings; evaluate every finding ourselves.
      checkVulnerabilities(invoke(['tool', 'govulncheck', '-json', './...'], directory, env), exceptions);
      const packages = invoke(['list', '-deps', '-json', './...'], directory, env);
      const report = invoke(['tool', 'go-licenses', 'report', '--template', template, './...'], directory, env);
      checkLicenses(packages, report, ownModules, notices);
    }
    return { status: 0, output, toolchain };
  } catch (error) {
    return { status: 1, output: `${output}dependencies: ${error.message}\n` };
  } finally {
    if (temporary) rmSync(temporary, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const result = checkDependencies();
  process.stdout.write(result.output);
  process.exitCode = result.status;
}
