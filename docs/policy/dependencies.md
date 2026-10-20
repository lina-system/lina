# Dependencies

This policy covers code that LINA takes from outside the repository: Go modules, the Go toolchain, the npm packages of the Claude adapter and of repository tooling, pinned binaries, external components, and upstream source and generated upstream code vendored into the repository. LINA Core runs all day with the user's credentials, so outside code must be pinned, reviewed like any other code, and verified by `foundation` before it can join `dev`. The rules apply from the first change that adds a dependency, and the `foundation` check enforces them.

## Scope

This policy covers:

- Go modules in every `go.mod` and `go.sum`
- the Go toolchain used by `foundation` and release builds
- npm packages in the root `package.json` and lockfile: repository tooling that needs an npm package
- the Claude adapter's lockfile, which pins the Claude Agent SDK package that LINA installs on the person's machine on first use of a Claude account ([runtime.md](../design/runtime.md))
- binaries that LINA ships or downloads, and external components
- upstream source and generated upstream code under `third_party/`
- the `dependencies` check that enforces these rules

Tools the user installs are outside this policy: workers such as Codex, the command-line tools that plugins rely on, and the plugins the user adds with their servers. They are not LINA dependencies. LINA records the version and source it observed for them and never pins them ([runtime.md](../design/runtime.md), [integrations.md](../design/integrations.md)). First-party plugins are LINA code and follow this policy. Tools that only the developers of LINA run during development are outside this policy as well; they are recorded under Development-time tools.

Other documents cover what each component uses and why. [runtime.md](../design/runtime.md) names the vendored upstream, the provider adapters, external components, the packaging, and the release manifest. The [foundation policy](foundation.md) owns path mapping and check layout.

## Go modules

LINA's components are Go. Their outside code comes in as Go modules.

### Toolchain and configuration

- Every `go.mod` has a `toolchain` line that names one exact Go release. `foundation` and release builds use that toolchain.
- `go.mod` and `go.sum` of every LINA module, and the workspace file when the repository uses one, are committed.
- Modules come only from the public Go module proxy and are verified against the Go checksum database. `GOPROXY`, `GOSUMDB`, `GONOSUMDB`, `GOPRIVATE` and `GOINSECURE` keep their defaults in `foundation`, release builds and scripts; do not override them with flags, environment variables, or user configuration.
- Go tools that scripts run, such as `govulncheck`, are module requirements pinned in `go.mod` and verified through `go.sum`, and run from those versions.

### Exact versions and sources

- Every `require` entry names one exact module version. A `replace` entry names one exact module version, or points to a directory of a LINA module or a vendored tree in this repository; a `replace` to any other local path is rejected.
- LINA's own modules refer to each other at exact versions or through such in-repository directories.

### go.sum and builds

- `go.sum` changes only through the `go` command, in the same change as the `go.mod` change that causes it.
- `foundation` and release builds run with `-mod=readonly` or from vendored modules, so a build that would change `go.mod` or `go.sum` fails. Use `go get` and `go mod tidy` only to change dependencies, never to verify them.
- `go mod verify` passes before every build in `foundation` and before every release build. A release build resolves nothing new.
- Install and build steps run in a clean worktree with no credentials. The publication stage (pushing the tag and placing the artifacts) is a separate stage that fetches no modules and uses the owner's local credentials ([releases.md](releases.md)).

### Vulnerabilities

- `govulncheck` runs on every LINA module in the `dependencies` check, and a reported vulnerability fails it unless the exceptions file lists it.
- The root and kit modules each run `go tool govulncheck -json ./...`. Every JSON `finding` requires an exception matching its vulnerability id, affected module and version, including module-level findings and `stdlib` findings. An advisory's `osv` metadata alone is not a finding. JSON mode returns success even when it reports findings, so the check evaluates the findings rather than relying on the process exit status. A scanner error or malformed output fails the check.

## npm packages

npm is used only for packages that run on Node.js: the Claude Agent SDK package that the Claude adapter installs from its own lockfile, and repository tooling that needs an npm package. LINA ships no Node.js runtime: the Claude adapter runs on the Node.js the person installed ([runtime.md](../design/runtime.md)). LINA's other components use no npm package.

### Package manager and configuration

npm is the only package manager for these packages. The root `.npmrc` contains:

```ini
registry=https://registry.npmjs.org/
save-exact=true
ignore-scripts=true
```

These settings apply to every npm command in the checkout and in release builds. Do not override them with flags, environment variables, or user configuration.

Run tools from the lockfile through package scripts or `npx --no-install`. Scripts never fetch an unpinned tool at run time, for example with `npx <package>` for a package outside the lockfile, `go run` or `go install` of a module version outside `go.mod`, or a piped install script.

### Exact versions and sources

- Every `dependencies`, `devDependencies`, and `optionalDependencies` entry in every `package.json` names one exact version, such as `1.2.3`. Ranges, dist-tags, `*`, Git URLs, tarball URLs, and `file:` specs are rejected.
- An `overrides` entry, which pins a transitive package, names one exact version.
- Packages come only from the public npm registry. Every lockfile entry resolves to `https://registry.npmjs.org/` and has a `sha512` integrity value.

### Lockfile

- One `package-lock.json` at the root covers every npm package. It is committed and changes only through npm, in the same change as the manifest change that causes it.
- `foundation` and release builds install with `npm ci --ignore-scripts`, which fails if the lockfile and manifests disagree. Use `npm install` only to change dependencies, never to verify them.
- The Claude adapter's lockfile is committed with the adapter. On first use LINA Core installs from it with `npm ci --ignore-scripts --omit=dev`, from the public registry, and verifies the installed packages against the lockfile's integrity values; nothing new is resolved.
- Install and build steps run in a clean worktree with no credentials. The publication stage is a separate stage that installs no packages and uses the owner's local credentials ([releases.md](releases.md)).

### No lifecycle scripts

- Because `ignore-scripts=true`, no package runs a `preinstall`, `install`, `postinstall`, or `prepare` script, anywhere.
- LINA's own `package.json` files define none of these scripts and no `pre` or `post` hooks. Scripts invoked explicitly, such as `npm test`, still run.
- A dependency that needs an install script to work is not admitted. Native code comes from prebuilt platform packages verified by lockfile integrity or from a pinned binary verified by digest.
- A lockfile entry with `hasInstallScript` is admitted only when the exceptions file lists that exact name and version and explains why the package works without the script. The component's tests must pass with the script never run.

### Signatures and provenance

- After `npm ci --ignore-scripts`, run `npm audit signatures`. A missing or invalid registry signature or an invalid provenance attestation fails the check.

## Adding or updating a dependency

- Use the Go standard library when it covers the need, and Node.js built-in modules for repository tooling.
- A new upstream release may be adopted as soon as it is published. No minimum release age applies to any dependency.
- Review the dependency change as code, including its license and the module and transitive package counts that the `dependencies` check reports.
- LINA is licensed under the Apache License, Version 2.0 ([LICENSE](../../LICENSE)). Every dependency linked into LINA's own binaries uses a permissive license only: MIT, Apache-2.0, BSD or ISC.
- A component that needs copyleft code (the GPL family or similar licenses) runs only as a separate process, as an external component ([runtime.md](../design/runtime.md)).
- Non-commercial and source-available licenses are not used anywhere.
- Every included component keeps its notices in [THIRD-PARTY-NOTICES.md](../../THIRD-PARTY-NOTICES.md), including the `NOTICE` file of each Apache-2.0 component.
- Update dependencies in small groups that can be reviewed together. Split bulk updates of unrelated modules or packages. The merge commit message of an update states the reason and the checks it ran, whoever made the update.

## Runtime and binaries

- The `toolchain` line of `go.mod` records the Go release that `foundation` and release builds use.
- `.node-version` records one exact Node.js version for repository tooling and for testing the Claude adapter. Releases ship no Node.js runtime.
- Pin every other binary that LINA ships or downloads, including sqlite-vec platform binaries and [external components](../design/runtime.md), by version and SHA-256 digest. Verify each download against its digest before extraction or execution, every time, including after a cache hit.
- An external component ships with its license and notices.
- The release manifest in [runtime.md](../design/runtime.md) records each pinned version and digest.

## Vendored code

When LINA takes upstream source or generated upstream code into the repository, it vendors it unchanged instead of translating it, so each upstream change remains a diff that can be reviewed. [runtime.md](../design/runtime.md) names what LINA vendors. Each vendored upstream has its own directory:

```text
third_party/<name>/
  upstream/      upstream files at their upstream relative paths, unchanged, with their tests, license, and notices
  patches/       recorded patches, applied in order; present only when a patch is unavoidable
  vendor.json    source, scope, file hashes, and patch records
```

- **Unchanged:** every file under `upstream/` is byte-identical to the recorded upstream commit, or to the result of applying the recorded patches. Vendored tests come with the code they test and run unchanged in `foundation`.
- **Licenses and notices:** keep the upstream license and notice files in `upstream/`. [THIRD-PARTY-NOTICES.md](../../THIRD-PARTY-NOTICES.md) has one entry per vendored tree with the source repository, commit, scope, license, and one line for each patch.
- **Wrapping layers:** LINA changes, including adapters, extra checks, and extra behavior, live in LINA's own packages that import vendored code. LINA never edits files under `upstream/`, and vendored code never imports LINA packages.
- **Patches:** use a patch only when a wrapping layer cannot make the change. Examples are an unreleased upstream security fix and removing an import that would bring in a rejected dependency. Record each patch as a file in `patches/` and an entry in `vendor.json` with its id, the files it touches, its reason, why a wrapper cannot make the change, and the upstream issue or PR when one exists. Offer a general fix upstream, and delete the patch once upstream includes it.
- **Scope:** `vendor.json` lists the upstream paths that LINA includes: the import closure of the code LINA uses and its tests. An excluded file is outside the scope, not a patch.
- **Generated upstream code:** code generated from an upstream tool's output is vendored the same way. The Go types of the Codex app-server protocol, generated from the JSON Schema that `codex app-server generate-json-schema --experimental` writes for each verified Codex version, are such code. Instead of a commit, `vendor.json` records each tool in the generation, the version and digest it ran at, and the exact commands. Never edit generated code by hand. A sync regenerates it.

`vendor.json` records the name, source repository URL, upstream commit (40 hex characters) and release tag when one exists, license, scope, SHA-256 hash of each file as upstream ships it and as it sits in the tree, and patch records.

### Upstream sync

A sync updates one vendored tree in one integrated change, with a merge commit title such as `chore(vendor): sync <name> to <tag>`:

1. Choose an upstream release. For an upstream without releases, choose a default-branch commit.
2. Review upstream changes in the scope between the recorded and target commits, including the changelog, breaking changes, new imports, and license changes.
3. Overwrite `upstream/` with the target commit's files for the scope. Change the scope only to follow the import closure, and state the change.
4. Re-apply the recorded patches in order. Delete a patch that upstream makes unnecessary. Rewrite a patch that fails to apply, and keep its reason current. Record a new patch in full.
5. Update `vendor.json`, the notices entry, and any wrapping layer affected by the upstream change.
6. Run the vendored tests unchanged, the wrapping layers' tests, and the checks of every dependent component.
7. List the upstream range, scope changes, kept, rewritten, and dropped patches, test results, and any behavior change at the wrapper boundary in the merge commit message.

Another change never updates vendored code as a side effect.

## Development-time tools

Some tools are used only by the people who develop LINA, to produce files that LINA then ships. They are not dependencies of LINA. Nothing from them is linked into a LINA binary, bundled in an installer or artifact, installed for a user, or run by a LINA install, so they have no entry in `THIRD-PARTY-NOTICES.md`, and the rules of this policy for Go modules, npm packages, pinned binaries and external components do not apply to them. What they produce is an ordinary change: it is reviewed like any other change and committed to the repository. What ships is those files, never the tool.

- Harness2 (Google Research, Apache-2.0, <https://github.com/google-research/harness2>) is such a tool. Developers run the Python Harness2 as it is published, pinned to one exact release or commit, during LINA's development and continuously, to improve the files of LINA's agent harness: the persona text, guardrails, memory guidance, tool guidance, sub-agent definitions and skills. It runs on a developer's machine with the developer's own model accounts. It has no cost cap of its own and no Go port. Pin: none yet. The first development run that uses it records the exact release or commit here.
- The loop reaches LINA only through two commands: the replay command ([runtime.md](../design/runtime.md#replay-command)) and the episode export ([conversation-and-memory.md](../design/conversation-and-memory.md#episode-export)). An episode holds conversation content from a developer's own install. Episode files are never committed to the repository and are not shared.
- A change of the pinned version of a development-time tool is deliberate and is reviewed in its own change, like a change of any other pin.

## Exceptions

`scripts/verify/dependency-exceptions.json` is the only place where a rule in this policy can be waived. Each entry names its kind (`install-script`, or `vulnerability` for a reported vulnerability that has no fixed version yet or does not reach LINA's code), the exact package or module name and version, the vulnerability id for a `vulnerability` entry, the reason, and a reference URL when one exists. Wildcards are not allowed. Each entry is reviewed in the change that adds it.

The file is a JSON array, initially empty. Entries use `kind`, `name`, `version`, and a nonempty `reason`; a `vulnerability` entry also requires `id`. The optional `reference` is an HTTP(S) URL. Go versions include the `v` prefix (including the version for `stdlib`); an `install-script` entry uses an exact npm version without that prefix. Unknown fields, duplicate entries, invalid selectors, missing fields, malformed JSON and a missing file fail before any Go command runs. An install-script exception cannot waive a vulnerability, and neither kind can waive the license rules.

## Foundation enforcement

The `dependencies` check runs whenever the [foundation policy](foundation.md) selects it, and reports to the gate like every selected check. A failure keeps the change out of `dev`. It runs without credentials. Its network access is read-only and limited to the Go module proxy, the Go checksum and vulnerability databases, the npm registry, and the recorded upstream repositories. The script is `scripts/verify/check-dependencies.mjs`, with behavioral tests like the other verification scripts. Run it alone with `node scripts/verify/check-dependencies.mjs`.

The implemented Go checks cover configuration, exact pins, `go mod verify`, vulnerabilities, exception validation, and licenses and notices for the root and kit modules. Both `go.mod` files and `go.work` must declare the same Go version and exact toolchain release; missing or differing declarations fail before Go runs. Every `require` version and every versioned side of a `replace` must be exact, including workspace replacements. Local replacements resolve relative to their manifest and must stay inside a repository module or a vendored module under `third_party/`, including after resolving symlinks. Go parses the manifests with `go mod edit -json` and `go work edit -json`, without rewriting them. The check runs with an isolated home, user configuration disabled, public proxy and checksum defaults, and `-mod=readonly`; its tests inject every Go command and use no network.

The root `go.mod` declares `govulncheck` and `go-licenses` as module tools with exact requirements and committed checksums. After module verification, each LINA module runs the vulnerability scan, `go list -deps -json ./...`, and `go tool go-licenses report` with a temporary JSON report template. The license check covers the non-test import closure of LINA's commands and libraries, including kit and transitive imports; a requirement used only by a verification tool is not included code. The standard library and LINA's own modules do not require third-party notices. Each license reported for third-party code must be MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, BSD-4-Clause or ISC. Unknown licenses, missing module coverage, malformed output and scanner failures fail the check.

Each included third-party Go module needs a nonempty section in `THIRD-PARTY-NOTICES.md` headed `## <module-path>`, containing the notices required by this policy. An incidental mention of a module does not count as an entry. For a versioned replacement the heading names the replacement module; for an in-repository vendored replacement it retains the original module path. This check establishes entry presence; it does not establish the legal completeness of the notice text or the integrity of vendored files.

A change to either module's `go.mod` or `go.sum`, or to `go.work` or `go.work.sum`, selects `dependencies` and every active component. Adding, modifying or deleting `scripts/verify/dependency-exceptions.json` or `THIRD-PARTY-NOTICES.md` selects `docs` and `dependencies`, without selecting automation or components solely for that change. Full verification and automation or shared-configuration changes also select it when Go manifests or sums are present or removed. Other prose-only and component-source-only changes skip it. The gate requires success when selected and an explicit skipped result otherwise; missing or malformed results fail. Foundation records the result, duration, and observed toolchain in its evidence.

`go mod verify` checks downloaded module content against the cache's recorded hashes; it does not itself establish that every required checksum is present in `go.sum`. The component check continues to verify modules before its readonly builds, which enforce the module and sum inputs needed by those builds. Dependency-change counts, npm checks (including application of install-script exceptions), non-Go component notices, vendored-tree integrity and upstream-fetch checks below remain future work; the current Go checks do not establish those rows.

| Check | Fails when |
| --- | --- |
| Configuration | The `go.mod` files and `go.work` lack matching Go versions and exact toolchain releases, `.npmrc` lacks a required setting, or `.node-version` is not one exact version. |
| Exact pins | A Go `require` or versioned `replace` is not exact, an unversioned replacement points outside the repository's own modules and vendored modules, a `package.json` dependency is not an exact registry version, or an `overrides` entry is not exact. |
| Module sums | `go mod verify` fails, a module is missing from `go.sum` or fails the checksum database, or a `-mod=readonly` build would change `go.mod` or `go.sum`. |
| Vulnerabilities | `govulncheck` reports a vulnerability in a LINA module that the exceptions file does not list. |
| Lockfile sources | A lockfile entry resolves outside `registry.npmjs.org` or lacks a `sha512` integrity value. |
| Install scripts | A LINA `package.json` defines an install lifecycle script or a `pre` or `post` hook, or a lockfile entry has `hasInstallScript` without an exception. |
| Signatures | `npm audit signatures` after `npm ci --ignore-scripts` reports a missing or invalid signature or an invalid attestation. |
| Vendored trees | A file under `third_party/<name>/upstream/` is missing from `vendor.json` or does not match it, a patch record is incomplete, or the notices entry is missing or names another commit. |
| Upstream fetch | For a changed `vendor.json`, the recorded upstream commit or generator output does not match the recorded upstream hashes, or the recorded patches do not reproduce the tree. |
| Exceptions | An exceptions entry is malformed, uses a wildcard, or lacks a reason. |
| Licenses | A Go module linked into a LINA binary, or an npm package that ships, has a license other than MIT, Apache-2.0, BSD or ISC, as reported by a license scanner such as `go-licenses`; or an included component has no entry in `THIRD-PARTY-NOTICES.md`. |

The `foundation` output and its evidence note list the Go modules and npm packages that the change adds, removes, or updates, their versions, the total module count, and the total transitive package count.

## Deferred

- The baseline `go.sum` and module count, and the Claude adapter's lockfile and its transitive package count, are recorded by the supply-chain baseline (V6) in the first implementation issue. Exact pins are deferred in [runtime.md](../design/runtime.md).
