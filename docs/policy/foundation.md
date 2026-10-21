# Foundation verification

`node scripts/foundation.mjs` classifies a change, runs the applicable checks, and reports one result: `foundation`. It runs from this repository on the developer's machine and in GitHub Actions, with Node.js and Git. [Integration](integration.md) requires a green pull request check before merging, or verifies the merge candidate through the owner's local script. A [release](releases.md) requires it for the exact release commit.

## Running it

```sh
node scripts/foundation.mjs [--range <base>..<head>] [--full] [--no-note]
```

- `--range <base>..<head>` classifies the diff between two commits. The head must resolve to the checked-out commit; otherwise selection fails and no checks run. Check out the candidate first. Without the option, the base is the commit the checked-out change was developed on: its merge base with `dev`, or the previous `dev` head when the checkout is `dev` itself.
- `--full` runs every current check, regardless of changed paths.
- `--no-note` skips writing the evidence note.

The script clears `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` and every other `GIT_*` environment variable on entry; it works on the repository it runs in and takes the range as an argument. A run that integration or a release relies on happens in a clean worktree of the exact commit, in an environment that holds no credentials.

## Pull request CI

The [CI workflow](../../.github/workflows/ci.yml) runs on pull requests targeting `dev` and pushes to `dev`, with one job named `foundation` on `ubuntu-24.04`. It checks out the event's `github.sha` with full history and without persisting Git credentials: the pull request merge commit from `refs/pull/<number>/merge`, or the pushed commit. It runs `node scripts/foundation.mjs --range <base>..<head> --no-note`, using the pull request's base SHA and that merge commit SHA, or the push's before and after SHAs. This verifies the proposed merge result even when the pull request branch is behind `dev`. A new branch push without a previous commit uses `--full --no-note`.

Node comes from `.node-version` when present, otherwise Node 24. When `go.mod` exists, Go comes from its `toolchain` line; a missing toolchain fails setup. Actions are pinned to commit SHAs. The workflow installs actionlint 1.7.12 from a checksum-verified archive and the Ubuntu Bubblewrap package before verification, so the sandboxed file and command acceptance tests run against the real launcher. CI has read-only repository permissions, writes no evidence note, and never publishes or integrates a change.

## Execution

```text
selection ----+---- docs --------+---- gate
              +---- automation --+
              +---- components --+
```

Everything runs in one process. `selection` records the exact base and candidate commits, the changed paths, the selected checks and the selection reason. It also checks Git whitespace errors: `git diff --check` over the range, or `git show --check` on the `HEAD` commit with `--full`. The selected checks run in parallel after selection. The gate only evaluates their results; it never reruns their commands. Each check has a bounded timeout.

## Evidence

When `foundation` passes on a clean checkout of the candidate commit, it writes its evidence as a JSON note on that commit under `refs/notes/foundation`: the head and base commits, the mode and selection reason, each check's result and duration, and the versions of the tools that ran. A run on a worktree with uncommitted changes reports its result and writes no note. A candidate other than the checked-out commit fails selection without running checks or writing a note.

The owner's local [integration script](integration.md) pushes the notes with `dev`. Pull request CI reports a GitHub check and writes no note. A note saves time: a [release](releases.md) reads it for the exact commit and, when it is missing or does not show a passing run of that commit, runs `foundation` again on that commit in a clean worktree.

## Change classification

The executable path map is `scripts/verify/scope.mjs`.

| Change | Selection |
| --- | --- |
| Only explicitly listed root documents, Markdown directly under `docs/policy/` or `docs/design/`, or JSON and Markdown records under `evidence/` | `docs`; no automation installation or test run. |
| A verification script, `.github/workflows/*.yml` or `*.yaml`, `.node-version`, or shared repository configuration | `docs`, `automation`, and every registered Go component. |
| Empty or unavailable diff | `docs`, `automation`, and every registered Go component; never assume documentation-only. |
| An unmapped path in the changed paths or candidate tree | `docs`, `automation`, and every registered Go component, but the gate refuses to pass until its verification is registered. |

Deleted paths and both sides of a rename participate. A `.md` suffix alone does not make a file prose: prompts, executable examples, and test inputs need their consumer's checks.

## Current checks

- **docs:** validate local file targets in all tracked Markdown and `LICENSE`, including links from unchanged documents to deleted targets (`scripts/verify/check-docs.mjs`). This does not validate external URLs, heading anchors, or the meaning of prose.
- **automation:** run `actionlint` on workflow files when present, check JavaScript syntax and run the Node behavioral suite of the scripts under `scripts/`. A missing actionlint executable or invalid workflow fails the check. Tests that use Git run in disposable repositories with every `GIT_*` variable cleared. Integration and release tests run against a disposable bare remote; they never push to the real remote or publish a real release.
- **components:** build, vet, run the pinned `exhaustive` analyzer, and run uncached tests for selected Go components (`scripts/verify/check-components.mjs`). Command builds go to a temporary directory; the check executes each produced command without arguments and verifies its usage error. Temporary outputs and the isolated home directory are removed when the check ends.

The scripts use Node 20 or newer and its built-in modules, Git, and actionlint on `PATH` for workflow validation and its regression tests. Use actionlint 1.7.12 to match CI; ShellCheck is used by actionlint when installed. Go components also need the exact Go toolchain named in the workspace.

## Timing and budget

| mode | commit | machine | cache | wall s | bound s |
| --- | --- | --- | --- | ---: | ---: |
| full | e86f7b2 | lina-os (x86-64, 20 threads) | warm | 3.6 | 20 |
| docs | e86f7b2 | lina-os (x86-64, 20 threads) | warm | 0.1 | 20 |
| full (Go components) | 230383f + #24 worktree | Linux x86-64, 24 threads | warm | 4.1 | 20 |
| full (import, vulnerability and license checks) | b9ddd15 | Linux x86-64, 16 CPUs | warm | 7.239 | 30 |

The bound is U(x) = 10 * ceil(max(1.5x, x + 15) / 10) seconds of wall time, measured from the start of `foundation.mjs` to the gate's result. A mode without a measurement has no bound. The development-loop check V7 records the first local measurement of each mode ([runtime.md](../design/runtime.md#runtime-acceptance)).

A run above its bound is investigated before the bound is changed. A bound changes only together with the new measurement, in the same change.

## Passing and failing

The gate (`scripts/verify/gate.mjs`) evaluates the selection and the check results in process, after the selected checks finish. Selection must succeed, every selected check must report `success`, and an unselected check must report `skipped`. Missing, failed, cancelled, malformed, or unexpectedly skipped results fail the gate.

The output names the candidate, the selection reason, and each check's result. A passing `foundation` means the registered checks passed, not that unimplemented product behavior or model quality was established.

## Iteration and extension

During iteration, run the affected test file, such as `node --test scripts/verify/scope.test.mjs`, rather than the entire suite, and run `node scripts/foundation.mjs` before integrating. Pure prose needs reading and link and format checks, not tests pinning its wording.

Introduce a new component together with its real verification commands, path mapping, and gate expectations. Tests must exercise the behavior being changed; bug fixes need a regression that fails without the fix. Build and install checks use the produced artifact. `foundation` runs the same commands a developer runs by hand, and never runs the same suite again in an aggregator. Cache downloads and reproducible build inputs, not previous pass/fail results.

## Go components

Product components are Go packages in the repository's two Go modules: the root module and the LINA kit in `kit/`, each with its own `go.mod` ([runtime.md](../design/runtime.md)). A `go.work` file at the repository root lists both modules; the first change that adds Go code creates it. The `toolchain` lines of both `go.mod` files and of `go.work` name the same Go release, the foundation checks verify that they match, and components are built with that toolchain. `go build` produces each component's static binary; there is no other build step. The [dependency policy](dependencies.md) governs what the modules may require and defines the `dependencies` check.

### Registration

Register a component in `scripts/verify/scope.mjs` in the change that adds it. Its entry names:

- its directory, so that every path under it, including Markdown, belongs to the component;
- its packages, which `foundation` verifies with `go build`, `go vet`, the `exhaustive` analyzer and `go test`, the same commands a developer runs by hand.

The protocol JSON Schema in `protocol/schema/` and the conformance fixtures in `protocol/fixtures/` are registered with the component that generates types from them, so a schema or fixture change runs type regeneration and the fixtures ([host-protocol.md](../design/host-protocol.md)).

The initial registry is:

| Component | Owned paths | Module | Package patterns |
| --- | --- | --- | --- |
| `kit` | `kit/` | `kit/` | `./...` |
| `lina` | `cmd/lina/`, `internal/archtest/`, `internal/buildinfo/`, root `doc.go` | root | `.`, `./cmd/lina/...`, `./internal/archtest/...`, `./internal/buildinfo/...` |
| `lina-node` | `cmd/lina-node/` | root | `./cmd/lina-node/...` |
| `moirai-worker` | `cmd/moirai-worker/`, `internal/moirai/` | root | `./cmd/moirai-worker/...`, `./internal/moirai/...` |

The scope script reads package imports and test imports with `go list` using the pinned toolchain, so a change to one component also selects every component that depends on it, transitively. It includes uncommitted changes and untracked files when checking the current checkout. Foundation rejects a candidate that is not checked out before invoking the scope script. Component-only changes run `docs` and the selected components; changes to verification scripts, shared configuration, module manifests or workspace files select every component. Selection and the evidence note record the selected component names. Until a component is registered, its paths remain unmapped and the gate refuses to pass. Register a vendored tree under `third_party/<name>/` the same way as any other component. `go test` runs its upstream tests unchanged.

### Architecture imports

Whenever a Go component is selected and `internal/archtest/` exists, `components` also includes `lina`, which runs the architecture test. Its source scan has no Go import edge to the packages it checks, so selection adds that dependency explicitly. It adds no component name or gate. Run it directly with `go test ./internal/archtest/...`.

The test enforces the import rules in [go-core.md C-40](../design/go-core.md#processes-binaries-and-packages) on production Go source in both modules, including inactive platform and build-tag variants. It parses imports and syntax without loading imported dependencies. Test harnesses (`*_test.go`), `testdata`, vendored trees, hidden directories and `node_modules` are excluded. Command smoke tests can therefore build and launch their binaries independently of the product's process wrappers.

- Core package families and their descendants use the C-40 layer table. A package under `internal/core` absent from that table fails as unmapped. Imports between different families must go strictly downward, including the ban on imports between L3 stores. Imports within one family are allowed; Go checks cycles.
- Moirai follows the separate C-40 worker layers: job API, read-only reader, runtime and command. It cannot import Core, providers or secret packages; SQLite imports belong only to its reader. Core may import only the worker job API contract.
- Kit cannot import the root module. Charm imports (`github.com/charmbracelet/` and `charm.land/`) belong only to `internal/tui/term` and `internal/tui/ui`.
- `os/exec` belongs only to `kit/proc` and `kit/sandbox`; references to `os.Environ`, including aliases, dot imports and function values, belong only to `kit/proc`.
- `internal/node` and `cmd/lina-node` may import the Go standard library and the C-35 kit families `grant`, `lease`, `sandbox`, `proc`, `protocol`, `rpc`, `diag` and `secret`. The command entry point `cmd/lina-node` may also import the exact package `internal/buildinfo`, which may itself import only the standard library.

Diagnostics name the source file, line and violated rule. Behavioral fixtures exercise forbidden and permitted imports; a disposable module proves that a seeded forbidden import fails the ordinary Go test command and that removing it restores success. This import check does not establish the separate C-40 purity, secret-reveal or view-field constraints.

### Acceptance evidence

[V6](../../evidence/v6-supply-chain.json) records the root `go.sum` digest, the workspace module count (including and excluding the two local modules), the selected module versions and the current Go dependency-gate result. There is no Claude adapter or npm lockfile yet; its supply-chain baseline remains unmeasured. Cached `go mod verify` does not independently revalidate every sum against the checksum database.

[V7](../../evidence/v7-dev-loop.json) records budgets declared before measurement, an edit followed by `go build`, `go vet` and uncached `go test` for the affected Go package, and `node scripts/foundation.mjs --full`. The initial budgets are 20 seconds for the affected-package loop and 60 seconds for full verification including vulnerability and license scans. Records identify the source, tool versions, cache conditions, commands, durations and outcomes. An over-budget measurement remains a failure; it is not hidden by raising the budget after the run.

JSON and Markdown under `evidence/` are documents paths. Updating an evidence record alone selects `docs`; the record is historical evidence, not a replacement for running checks on a changed candidate. Executable files in that directory remain unmapped.

### Path mapping

| Change | Selection |
| --- | --- |
| A path under a registered component directory | `components` for that component and its dependents. |
| A path under `third_party/<name>/` | `dependencies`, plus `components` for that tree and its dependents. |
| A `go.mod`, `go.sum` or Go workspace file, or the root `package.json`, `package-lock.json`, `.npmrc` or `.node-version` | `dependencies`, plus `components` for every component. |
| `scripts/verify/dependency-exceptions.json` | `docs` and `dependencies`. |
| `THIRD-PARTY-NOTICES.md` | `docs` and `dependencies`. |

Markdown under `third_party/` is upstream content. The docs check skips it because its relative links point into the upstream repository.

### Checks

`dependencies` and `components` run after `selection`, in parallel with `docs` and `automation`. Selection lists the selected components. The gate evaluates both checks like the others: a selected check must report `success`, and an unselected check must report `skipped`.

- **dependencies:** checks matching Go and exact toolchain declarations in both module manifests and `go.work`, exact `require` and `replace` versions, local replacement boundaries, `go mod verify`, `govulncheck` findings, exception validation, and Go licenses and notices in both modules. It validates the exceptions file before running Go and rejects vulnerability findings without a matching exception. License scanning covers the non-test import closure of LINA's commands and libraries, rejects licenses outside the allowed list, and requires a nonempty notice entry for each included third-party Go module. Module and workspace manifest or sum changes select it and every active component; full verification and automation or shared-configuration changes select it when Go manifests or sums are present or removed. Changes to `scripts/verify/dependency-exceptions.json` or `THIRD-PARTY-NOTICES.md` select `docs` and `dependencies`. Other prose-only and component-source-only changes skip it. The gate requires success when selected and an explicit skipped result otherwise, and the evidence records its result, duration and toolchain. Coverage and remaining checks are listed in the [dependency policy](dependencies.md#foundation-enforcement). It runs without credentials, with an isolated home and user configuration disabled, and keeps Go's public proxy and checksum defaults. Its behavioral tests inject every Go command and use no network.
- **components:** uses Go from the shared `toolchain` line and runs `go build`, `go vet`, the `exhaustive` analyzer and `go test` with `-mod=readonly` for each selected component, in the directory of the module that holds it. The analyzer is a module tool pinned in `go.mod`, like `govulncheck` ([dependencies.md](dependencies.md)), and fails a switch over a closed set of variants that misses one. Build checks use the produced binary. Short component checks share one step and its module download. A component gets its own parallel step only when a measured run shows that the shared step is the bottleneck.

The component check requires matching `go` and exact `toolchain` directives in both module manifests and `go.work`, sets `GOTOOLCHAIN` to that release, and checks the Go version actually used. It verifies module sums before building, including the root module that supplies the analyzer when only kit is selected. Commands sharing a module run together with their package patterns. Go subprocesses use an isolated home directory and an allowlist of environment variables, with user Go/Git configuration disabled; proxy and checksum settings retain Go's defaults. All Go builds and analyzer package loading use `-mod=readonly`, and builds use `CGO_ENABLED=0`.

Component registration implements the `components` gate. The separate `dependencies` check implements the current Go dependency policy checks, including vulnerability scanning, exception validation, and license and notice checks. Dependency-change counts, npm checks (including application of install-script exceptions), non-Go component notices, vendored-tree integrity and upstream-fetch checks remain future work. Notice checks establish entry presence, not the legal completeness of the notice text. `go mod verify` checks downloaded module contents against cached hashes; readonly component builds also enforce the manifest and checksum inputs needed by those builds.

Both checks reuse the machine's Go module and npm download caches, which `go.sum` and lockfile integrity verify, and never cache `node_modules` or results: `go test` runs with `-count=1`, so no cached test result counts. Adding either check changes the full-mode run, so the change that registers the first component records a new full-mode measurement and bound in the timing table.

The Go commands run in the directory of each module they cover (the repository root or `kit/`), with `<dir>` relative to that module. The npm and Node commands run once, at the repository root.

```sh
go mod verify
govulncheck ./...
npm ci --ignore-scripts
npm audit signatures
node scripts/verify/check-dependencies.mjs
go build ./<dir>/...
go vet ./<dir>/...
go tool exhaustive ./<dir>/...
go test -count=1 ./<dir>/...
```

## Boundaries

Verification runs in a clean worktree without credentials: no Git or GitHub credentials, tokens, account secrets, personal data, or production services, and no write access to any remote. Actual model and provider calls and subjective quality evaluations run separately in explicitly authorized environments.

[Integration](integration.md) decides how a passing candidate joins `dev`. A passing `foundation` does not authorize a [release](releases.md) or a deployment.
