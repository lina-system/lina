# Foundation verification

`node scripts/foundation.mjs` classifies a change, runs the applicable checks, and reports one result: `foundation`. It runs from this repository on the developer's machine and in GitHub Actions, with Node.js and Git. [Integration](integration.md) requires a green pull request check before merging, or verifies the merge candidate through the owner's local script. A [release](releases.md) requires it for the exact release commit.

## Running it

```sh
node scripts/foundation.mjs [--range <base>..<head>] [--full] [--no-note] [--jobs <count>] [--event <event>] [--e2e]
```

- `--range <base>..<head>` classifies the diff between two commits. The head must resolve to the checked-out commit; otherwise selection fails and no checks run. Check out the candidate first. Without the option, the base is the commit the checked-out change was developed on: its merge base with `dev`, or the previous `dev` head when the checkout is `dev` itself.
- `--full` runs every current check, regardless of changed paths.
- `--event pull_request` excludes e2e from a range plan unless `--e2e` is also given. CI supplies the event and derives the opt-in from the PR label `e2e`. Local selection is unchanged and `--full` always includes all acceptance scenarios.
- `--no-note` skips writing the evidence note.
- `--jobs <count>` bounds local child processes; the default is at most four and never more than the available CPUs. At most one local e2e shard runs at a time, retaining the four-scenario Core/PTY bound while other checks overlap it.

The script clears `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` and every other `GIT_*` environment variable on entry; it works on the repository it runs in and takes the range as an argument. A run that integration or a release relies on happens in a clean worktree of the exact commit, in an environment that holds no credentials.

The legacy component runner (`check-components.mjs`, phase `all`) compiles
acceptance packages once per candidate with `go test -c` into the
check's disposable directory. The produced binary enumerates its tests and runs
up to four disjoint batches in the package's directory with `-test.count=1` and
`-test.timeout=6m` and Go's `-test.paniconexit0` guard against early successful
process exit. Results are never cached. Verbose batch output identifies the
active scenario if the shared components deadline expires. Packages without
test files still receive their normal build, vet and analyzer checks.

PR #559's [CI run 39825375534](https://github.com/thisisjun786/lina/actions/runs/39825375534)
exhausted the 895-second components budget: the first acceptance batch passed
in 240.206 seconds, and the second was terminated after its remaining 331.116
seconds. Reusing the candidate's compiled test binary removes the repeated Go
package loading and linking for enumeration and each batch. A real regression
requires one compilation while executing every test, subtest, example and fuzz
seed once. The component and per-batch deadlines remain unchanged.

[Run 39835581651](https://github.com/thisisjun786/lina/actions/runs/39835581651)
subsequently reached a batch's six-minute diagnostic timeout while
`TestM3WebSocketInstalledCore` had run for two seconds. The growing corpus now
uses four disjoint batches to leave more room below that deadline. Compilation
still occurs once, and every test, subtest, example and fuzz seed runs once.
A controlled-clock regression executes a 720-second corpus after 150 seconds
of preparation: the former two-batch layout reaches the diagnostic timeout,
while four batches each take 180 seconds and finish in the same total 870
seconds. This regression explicitly retains the original 895-second budget, independently
of the later shared-budget change on dev described below. In
[run 39844541882](https://github.com/thisisjun786/lina/actions/runs/39844541882),
the four batches took 103.420, 62.245, 107.837 and 107.949 seconds; the full
foundation gate passed in 697 seconds within the original budget.

## Pull request CI

The [CI workflow](../../.github/workflows/ci.yml) checks pull requests targeting `dev` (and the owner-requested `codex/ln-369-*` stack), pushes to `dev`, a nightly schedule, and manual runs. Every job checks out the event's `github.sha` with full history and without persisting Git credentials. A pull request selects the diff from its base SHA to its merge commit from `refs/pull/<number>/merge`. PRs exclude e2e by default; the `e2e` label selects reached scenarios, and adding or removing it starts a fresh selection. Pushes, nightly runs and manual runs select the full suite. Post-merge runs have distinct concurrency groups so a newer push does not cancel running or pending verification.

Selection runs once and publishes a plan bound to the candidate head, tree and a digest of the entire plan. Independent jobs read that same artifact: docs, automation, dependencies, build/vet/exhaustive, unit packages balanced by measured time, and e2e scenarios balanced by source file. The growing m1 corpus is split further; m2 and m3 tests that share the m1 harness are grouped by their milestone test names. The default plan has up to four unit shards and six m1, two m2 and one m3 acceptance shards, omitting empty shards. The final job keeps the single required check name `foundation` and aggregates results without rerunning any command. Shard artifact names include the workflow run attempt and download into separate directories. The gate reads the highest numbered attempt available for each shard, retaining earlier results for jobs that were not rerun. An earlier failure cannot overwrite a newer success, and a newer failure cannot fall back to an earlier success. A missing, failed, cancelled, skipped, stale or duplicate selected result fails the gate, as does a failed upstream job.

A documentation-only plan invokes no Go work. Node comes from `.node-version` when present, otherwise Node 24. Selection uses the exact workspace Go toolchain when Go inventory is needed; Go jobs also install the `toolchain` named in `go.mod`. Missing or inconsistent toolchain declarations fail verification. Actions are pinned to commit SHAs. Only automation installs checksum-verified actionlint 1.7.12. Its unsupported-key diagnostic for GitHub's `concurrency.queue` is narrowly excluded; the QA workflow regression checks `queue: max` and keeps cancellation disabled. Verification jobs have read-only repository permissions, write no evidence note, and never publish or integrate a change. Only the separate post-merge QA job has issue-write permission; PRs never execute its reporter.

Jobs that exercise Linux sandboxing install Ubuntu's Bubblewrap package and the [checked AppArmor profile](../../scripts/verify/bwrap.apparmor) permitting `/usr/bin/bwrap` to create user namespaces. The Linux automation tests compile that profile with `apparmor_parser --skip-kernel-load --skip-cache`; they do not load it into the developer's kernel. A direct namespace launch probes CI setup before running tests. These settings apply to disposable CI runners; missing sandbox support is a verification failure.

## Post-merge QA issues

After a full run on `dev` (push, nightly or manual), the independent QA job
reads the exact plan and newest shard result artifacts, including failures.
`scripts/verify/post-merge-qa.mjs` opens or updates a `qa` issue for each failing
Go top-level test, with nested failure lines, the tested head, first failing
commit range and workflow run. A package timeout or panic also fails each
unfinished test with an active leaf, an explicit nested failure, or a test named
in Go's timeout running-test diagnostics, with bounded package diagnostics.
The running-test list distinguishes a parent still executing after starting a
parallel child from a parent waiting on its children. Its identities are retained
per package even when a stack dump exceeds the diagnostic tail. Parallel tests
paused for an execution slot, including parents waiting only on paused subtests,
remain unknown and create no QA issue.
Unknown results cannot close an existing issue or make the foundation gate pass.
Completed tests retain their results; tests that never started are not reported
as failures. Configure the
repository's `QA_MILESTONE`
variable to the current milestone number; a missing setting fails reporting
instead of assigning findings to a guessed milestone.

Before opening the first issue for a test, the reporter checks retained artifacts
from completed later CI runs on the same branch. It validates each plan and result
against the run head and plan digest, selects the newest attempt per shard, and
compares commit ancestry. A latest terminal pass on a descendant head prevents
an older failure from creating an already recovered finding, even when the pass
was reported before any issue existed. A same-head failure takes precedence over
a pass. Missing, expired or skipped results cannot establish recovery; malformed
results fail reporting. This uses existing Actions artifacts within their retention
period, without creating issues for tests that have only passed. The reporter
needs `actions: read` for cross-run artifacts and `unzip` to read their JSON members.

Issue identity includes branch, package and test name in a machine-readable
body marker. Repeated failures update the same issue, including reopening a
resolved finding. An explicit pass on a later descendant head closes it.
Same-head reruns, skipped or missing tests, invalid artifacts and older heads
do not prove recovery. Each test is reconciled independently of unrelated test
failures. The first failing range survives updates. Reporting is serialized
with `queue: max`, preserving up to 100 pending jobs in GitHub's FIFO queue.
The queue is bounded: GitHub cancels additional jobs when those slots are full.
Its token is available only to the reporter step, never to verification.
PR runs perform only the QA job's scope check and cannot write issues.

For validation before landing, dispatch CI on an explicitly named
`qa/e2e-*` branch. It uses the same full plan and reporter, with issue identity
isolated from `dev`. A temporary push trigger on that validation branch can
exercise the push path; it is not part of the production workflow. Retain the
failure and recovery run links as acceptance evidence. Post-merge findings
never enter another PR's foundation selection or gate.

## Execution

```text
selection ----+---- docs / automation / dependencies ----+---- foundation
              +---- build / vet / exhaustive ------------+
              +---- unit package shards -----------------+
              +---- e2e (post-merge / PR opt-in) ---------+
```

`node scripts/foundation.mjs` remains the single local entry point. It selects the same plan and starts bounded child processes, giving each Go process a share of the available CPUs through `GOMAXPROCS`. The shared e2e harness passes its current Go CPU budget to child commands when constructing their isolated environment, so parallel Core processes retain that share. Locally, one e2e process at a time uses `-parallel=4`; in CI, each e2e shard has its own runner. All local checks share a 1200-second execution deadline. Each Go shard also keeps the existing component command budget, and `go test -timeout=6m` bounds package execution and emits stuck-test diagnostics. A unit shard runs one test package at a time (`-p=1`), so its test binaries together stay within the shard's `GOMAXPROCS` share instead of multiplying it; every selected package and test still runs once. Individual operation and scenario deadlines are unchanged. A failed shard does not cancel independent shards.

QA issue #1232's changed-range run exhausted the six-minute package watchdog while `internal/core/boot` and `internal/core/records` were progressing in unit shards 1 and 2: the active tests had run for one and two seconds, and no assertion failed. Both tests pass in isolation. On a host carrying other verification work, the store writer waited in SQLite's `Fdatasync` while four shards each ran four test packages with four parallel tests. Unit shards now run one package at a time, retaining the package and shared deadlines and every selected test.

Selection records the exact base and candidate commits, changed paths, selected components, checks and shards, and the selection reason. It checks Git whitespace with `git diff --check` over the range, or `git show --check` on `HEAD` with `--full`. CI uses the same entry point with `--select <plan.json>`, `--plan <plan.json> --shard <id> --out <result.json>`, and `--plan <plan.json> --results <directory> --upstream <status> --no-note`. Worker results must match the selected head, tree and plan digest. Go JSON results must account for every selected package and e2e test; a zero exit code cannot hide an omitted test.

Package and scenario weights in [test-times.json](../../scripts/verify/test-times.json) come from uncached `go test -json` measurements. They only affect placement. New packages and scenarios receive a default weight, and stale timing entries never omit work or cache a passing result. A scenario file stays together. Files with identical Go test names are joined only where `-run` cannot distinguish them; unrelated files remain separate. TestMain-only packages still receive an invocation.

The Go parser discovers active Test, Example and Fuzz declarations and follows helper declarations and package initialization. Before executing acceptance tests, selection rejects a package importing Core boot or `kit/proc` unless its `TestMain` dispatches `proc-supervise` to `os.Exit(proc.SuperviseMain(...))` before `m.Run()`. This prevents supervisor re-execution from recursively running the suite. Scenario imports and explicitly launched command packages are connected to the dependency graph from `go list -deps -test`. Changes to a source file, fixture, package testdata or embedded input select its consumers. An input without a known consumer that maps to an acceptance package selects every scenario in that package, including when a runtime fixture was deleted or renamed. Shared harness changes, unresolved dynamic commands and unmapped inputs select all scenarios. Go package build constraints determine the active source files. Full mode always runs the entire corpus, exactly once per top-level declaration, including subtests and fuzz seeds. The shared command-build cache inside each e2e process is locked and cleaned after its scenarios finish.

## Evidence

When `foundation` starts and finishes on a clean checkout of the same candidate commit and passes, it writes its evidence as a JSON note on that commit under `refs/notes/foundation`: the head and base commits, the mode and selection reason, each check's result and duration, and the versions of the tools that ran. A run on a worktree with uncommitted changes reports its result and writes no note. A candidate other than the checked-out commit fails selection without running checks or writing a note.

The owner's local [integration script](integration.md) pushes the notes with `dev`. Pull request CI reports a GitHub check and writes no note. A note saves time: a [release](releases.md) reads it for the exact commit and, when it is missing or does not show a passing run of that commit, runs `foundation` again on that commit in a clean worktree.

## Change classification

The executable path map is `scripts/verify/scope.mjs`. All paths under `internal/core/` belong to `lina`, whose Go checks include `./internal/core/...`. New Core families need only a layer entry in `internal/archtest`; they need no foundation path-map entry. The architecture check still rejects unknown Core families and disallowed imports.

| Change | Selection |
| --- | --- |
| Only explicitly listed root documents, Markdown directly under `docs/policy/`, `docs/design/` or `docs/research/`, JSON and Markdown records under `evidence/`, or PNG captures directly under `evidence/brand/` or `evidence/generated-ui/` | `docs`; no automation installation or test run. |
| A verification script, `.github/workflows/*.yml` or `*.yaml`, `.node-version`, shared repository configuration, or a file under `native/` | `docs`, `automation`, and every registered Go component. |
| Empty or unavailable diff | `docs`, `automation`, and every registered Go component; never assume documentation-only. |
| An unmapped path in the changed paths or candidate tree | `docs`, `automation`, and every registered Go component, but the gate refuses to pass until its verification is registered. |

Deleted paths and both sides of a rename participate. A `.md` suffix alone does not make a file prose: prompts, executable examples, and test inputs need their consumer's checks.

## Current checks

- **docs:** validate local file targets in all tracked Markdown and `LICENSE`, including links from unchanged documents to deleted targets (`scripts/verify/check-docs.mjs`). Local `*.md#fragment` links must match a GitHub-style heading anchor in the target document, including numbered suffixes for duplicate headings. ATX and setext headings count; a multiline setext heading uses the entire paragraph above its underline, not just the final line, and leading link reference definitions are not part of its text. Code examples and comments do not count. Custom HTML IDs are not supported. External URLs, bare `#fragment` links, non-Markdown fragments and the meaning of prose are outside this check.
- **automation:** run `actionlint` on workflow files when present, check JavaScript syntax and run the Node behavioral suite of the scripts under `scripts/`. When `native/` exists it also compiles and runs the portable cores of the locked-Mac native parts — the unlock plug-in's decision core and the lock guard with its install, uninstall and rollback against a fake authorization database — (`scripts/verify/check-native.mjs`) with `CC`, or the first of `cc`, `clang` and `gcc`. A missing actionlint executable, a missing C compiler or an invalid workflow fails the check. Tests that use Git run in disposable repositories with every `GIT_*` variable cleared. Integration and release tests run against a disposable bare remote; they never push to the real remote or publish a real release.
- **components:** build, vet and run the pinned `exhaustive` analyzer in one shard, then run uncached unit and acceptance shards for selected Go components (`scripts/verify/check-components.mjs`). Command builds go to a temporary directory; the check executes each produced command without arguments and verifies its usage error. Temporary outputs and the isolated home directory are removed when the check ends.

The scripts use Node 20 or newer and its built-in modules, Git, actionlint on `PATH` for workflow validation and its regression tests, and a C compiler for the native decision core. Linux release-assembly tests also require bubblewrap and working unprivileged user namespaces; CI installs bubblewrap using only the runner's official Ubuntu sources (unrelated preinstalled vendor repositories are excluded), permits unprivileged user namespaces on its disposable Ubuntu runner and probes namespace setup before testing. Actual builds retain filesystem, PID and network isolation. Those platform-specific release tests are skipped on other hosts; the ordinary cross-build and Go tests still run. Use actionlint 1.7.12 to match CI; ShellCheck is used by actionlint when installed. Go components and the scenario-parser regression tests also need the exact Go toolchain named in the workspace.

## Timing and budget

| mode | commit | machine | cache | wall s | bound s |
| --- | --- | --- | --- | ---: | ---: |
| full | e86f7b2 | lina-os (x86-64, 20 threads) | warm | 3.6 | 20 |
| docs | e86f7b2 | lina-os (x86-64, 20 threads) | warm | 0.1 | 20 |
| full (Go components) | 230383f + #24 worktree | Linux x86-64, 24 threads | warm | 4.1 | 20 |
| full (import, vulnerability and license checks) | b9ddd15 | Linux x86-64, 16 CPUs | warm | 7.239 | 30 |

The bound is U(x) = 10 * ceil(max(1.5x, x + 15) / 10) seconds of wall time, measured from the start of `foundation.mjs` to the gate's result. A mode without a measurement has no bound. The development-loop check V7 records the first local measurement of each mode ([runtime.md](../design/runtime.md#runtime-acceptance)).

PR #370 also exhausted the former components deadline at 595.4 seconds
while tests were progressing. With the 900-second component watchdog, its
subsequent full CI run completed successfully in 681.4 seconds. Build-only and
automation limits remain independent; no checks are skipped or test results
cached.

A run above its bound is investigated before the bound is changed. A bound changes only together with the new measurement, in the same change.

## Passing and failing

The gate (`scripts/verify/gate.mjs`) evaluates the selection and the check results in process, after the selected checks finish. Selection must succeed, every selected check must report `success`, and an unselected check must report `skipped`. Missing, failed, cancelled, malformed, or unexpectedly skipped results fail the gate.

The output names the candidate, the selection reason, and each check's result. Successful runs also print Go's package timings for the executed shards; CI result artifacts retain package and top-level test timings. A passing `foundation` means the registered checks passed, not that unimplemented product behavior or model quality was established.

## Iteration and extension

During iteration, run the affected test file, such as `node --test scripts/verify/scope.test.mjs`, rather than the entire suite, and run `node scripts/foundation.mjs` before integrating. Pure prose needs reading and link and format checks, not tests pinning its wording.

Introduce a new component together with its real verification commands, path mapping, and gate expectations. Tests must exercise the behavior being changed; bug fixes need a regression that fails without the fix. Build and install checks use the produced artifact. `foundation` runs the same commands a developer runs by hand, and never runs the same suite again in an aggregator. Cache downloads and reproducible build inputs, not previous pass/fail results.

## Go components

Product components are Go packages in the repository's two Go modules: the root module and the LINA kit in `kit/`, each with its own `go.mod` ([runtime.md](../design/runtime.md)). A `go.work` file at the repository root lists both modules; the first change that adds Go code creates it. The `toolchain` lines of both `go.mod` files and of `go.work` name the same Go release, the foundation checks verify that they match, and components are built with that toolchain. `go build` produces each component's static binary; there is no other build step. The [dependency policy](dependencies.md) governs what the modules may require and defines the `dependencies` check.

### Registration

Register a component in `scripts/verify/scope.mjs` in the change that adds it. Its entry names:

- its directory, so that every path under it, including Markdown, belongs to the component;
- its packages, which `foundation` verifies with `go build`, `go vet`, the `exhaustive` analyzer and `go test`, the same commands a developer runs by hand.

Core package families belong to `lina`. Add each new `internal/core/<package>/` family to `coreDirectories` in `scripts/verify/scope.mjs`, keeping entries sorted by path with one entry per line. This lets additions at separate positions merge without editing a shared line; registration still selects only the listed families and their descendants.

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

- Memory importer add-ons stay outside production Core and default package dependencies. The sole installed-entry exception is `cmd/lina/import_hermes.go` importing the exact `internal/addons/importer/hermes` package; other command files and other adapters remain refused.
- Core package families and their descendants use the C-40 layer table. A package under `internal/core` absent from that table fails as unmapped. Imports between different families must go strictly downward, including the ban on imports between L3 stores. Imports within one family are allowed; Go checks cycles.
- Moirai follows the separate C-40 worker layers: job API, read-only reader, runtime and command. It cannot import Core, providers or secret packages; SQLite imports belong only to its reader. Core may import only the worker job API contract.
- Kit cannot import the root module. Charm imports (`github.com/charmbracelet/` and `charm.land/`) belong only to `internal/tui/term` and `internal/tui/ui`. Only `internal/tui/ui` may import the pinned Goldmark root, `ast`, `text` and `util` packages for semantic Markdown projection; the exception does not admit other external libraries or Goldmark subpackages.
- `os/exec` belongs only to `kit/proc` and `kit/sandbox`; references to `os.Environ`, including aliases, dot imports and function values, belong only to `kit/proc`.
- `internal/node` and `cmd/lina-node` may import the Go standard library and the C-35 kit families `grant`, `lease`, `sandbox`, `proc`, `protocol`, `rpc`, `diag` and `secret`. The command entry point `cmd/lina-node` may also import the exact package `internal/buildinfo`, which may itself import only the standard library.

Diagnostics name the source file, line and violated rule. Behavioral fixtures exercise forbidden and permitted imports; a disposable module proves that a seeded forbidden import fails the ordinary Go test command and that removing it restores success. This import check does not establish the separate C-40 purity, secret-reveal or view-field constraints.

### Acceptance evidence

[V6](../../evidence/v6-supply-chain.json) records the root `go.sum` digest, the workspace module count (including and excluding the two local modules), the selected module versions and the current Go dependency-gate result. The Claude adapter now has its own lockfile and npm verification; the historical V6 record predates that addition. Cached `go mod verify` does not independently revalidate every sum against the checksum database.

[V7](../../evidence/v7-dev-loop.json) records budgets declared before measurement, an edit followed by `go build`, `go vet` and uncached `go test` for the affected Go package, and `node scripts/foundation.mjs --full`. The initial budgets are 20 seconds for the affected-package loop and 60 seconds for full verification including vulnerability and license scans. Records identify the source, tool versions, cache conditions, commands, durations and outcomes. An over-budget measurement remains a failure; it is not hidden by raising the budget after the run.

JSON and Markdown under `evidence/`, and PNG captures directly under `evidence/brand/` and `evidence/generated-ui/`, are documents paths. Updating an evidence record alone selects `docs`; the record is historical evidence, not a replacement for running checks on a changed candidate. Executable files in that directory remain unmapped.

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
- **components:** uses Go from the shared `toolchain` line and runs `go build`, `go vet`, the `exhaustive` analyzer and `go test` with `-mod=readonly` for each selected component, in the directory of the module that holds it. The analyzer is a module tool pinned in `go.mod`, like `govulncheck` ([dependencies.md](dependencies.md)), and fails a switch over a closed set of variants that misses one. Build checks use the produced binary. Build and analysis share one job. Measured package times distribute unit tests, and source files distribute acceptance tests across independent jobs; all use the same selection. A unit shard passes `-p=1` so one test package runs at a time and the shard's `GOMAXPROCS` share bounds the whole shard.

The component check requires matching `go` and exact `toolchain` directives in both module manifests and `go.work`, sets `GOTOOLCHAIN` to that release, and checks the Go version actually used. It verifies module sums before building, including the root module that supplies the analyzer when only kit is selected. Commands sharing a module run together with their package patterns. Go subprocesses use an isolated home directory and an allowlist of environment variables, with user Go/Git configuration disabled; proxy and checksum settings retain Go's defaults. All Go builds and analyzer package loading use `-mod=readonly`, and builds use `CGO_ENABLED=0`.

Component registration implements the `components` gate. The separate `dependencies` check implements the current Go dependency policy checks, including vulnerability scanning, exception validation, and license and notice checks. Dependency-change counts, npm checks (including application of install-script exceptions), non-Go component notices, vendored-tree integrity and upstream-fetch checks remain future work. Notice checks establish entry presence, not the legal completeness of the notice text. `go mod verify` checks downloaded module contents against cached hashes; readonly component builds also enforce the manifest and checksum inputs needed by those builds.

Each Go shard shares a 1195-second command budget across its commands, leaving five seconds for diagnostics and cleanup. The local executor also bounds all shard execution together at 1200 seconds. Go environment queries, token checks and command usage checks have a 30-second limit; module verification and protocol generation have a 120-second limit. Builds, vet, the exhaustive analyzer and uncached tests may use the remaining shared budget, so a progressing module test run can exceed 120 seconds as its package count grows. Every command's limit is capped by the remaining budget; it never resets between commands or modules. Output does not extend the overall deadline. A subprocess timeout fails verification and reports the command, its arguments, elapsed time and assigned budget.

On PR #298, [CI run 39659531710](https://github.com/thisisjun786/lina/actions/runs/39659531710) exhausted the former 595-second components budget after about 200 seconds of setup, builds and analysis and 395 seconds of module compilation and tests. Completed packages passed, including exchanges; end-to-end verification was still unfinished. That change used an 895-second budget while retaining its then-current four-minute package timeout and individual operation deadlines. Build-only and automation budgets remain unchanged.

On PR #485, [CI run 39783991019](https://github.com/thisisjun786/lina/actions/runs/39783991019)
reached the six-minute e2e package limit while the 108-test suite was still
progressing through shortcut fixtures (the current subtest had just started).
No assertion failed before the package watchdog fired. The component check used
548.3 seconds of its 895-second budget. The acceptance batching introduced then
ran that corpus in disjoint groups without raising the six-minute
package limit or changing individual operation deadlines and the shared
895-second command budget. The regression simulates a progressing 390-second
e2e suite after 200 seconds of setup and requires every scenario to run once
while the enclosing component guard holds.


On PR #398, [CI run 39682857682](https://github.com/thisisjun786/lina/actions/runs/39682857682) reached the four-minute end-to-end package limit while an installed-Core scenario had run for nine seconds and its current subtest for one second. That change raised the package-wide diagnostic deadline to six minutes while preserving individual operation deadlines and the then-current fifteen-minute guard. A controlled-clock test verifies that a progressing five-minute package fits after compilation, while the existing hung-command tests retain the shared guard.

Build regression fixtures also share a 595-second allowance across their build CLI calls, including both cross-build targets, under automation's 600-second watchdog. They pass the remaining budget to `scripts/build.mjs --timeout-ms <milliseconds>`, reserving one second for the CLI to report failures before its wrapper expires. The build script defaults to 595 seconds when invoked directly. Its inner Go builds share the remaining deadline, with environment queries capped at 30 seconds and module verification and metadata generation at 120 seconds. Inner commands and the fixture wrapper use the same timeout diagnostics.

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

PR #628's local full run on `2b9ad8a7` exhausted the default 30-second
scenario context in file-landing and first-run acceptance: the affected cases
ended at 30.7, 30.1 and 31.5 seconds while reconnecting after earlier successful
steps. File landing performs five Core boots plus worker observation, result
inspection, interrupted delivery and restart verification. It now has a
100-second scenario allowance after installation. First-run acceptance covers
two Core boots, two TUI connections, the first conversation turn and restored
history in 60 seconds. Core connection and process waits retain their existing
10-second bounds; package and enclosing foundation deadlines are unchanged.
Neither scenario retries failed observations or substitutes a sleep for a
protocol or process-completion barrier.

Compaction acceptance keeps forty retained turns across two Core boots to
exercise successive summaries; the harness default of three operation
timeouts covered about thirty seconds of that work and the post-merge run
failed at 30.25 seconds. The scenario now has a 120-second allowance. Its
observations, per-turn waits and Core connection bounds are unchanged.

Manual work-turn acceptance waits follow the exact client input's durable turn.
A failed or aborted turn fails the scenario immediately with its input id, turn
id and state; it cannot later complete. An input with no turn yet, or an active
turn, retains the scenario deadline. `TestM2QueuedWorkTurnTerminal` exercises
these states in canon, including completed turns belonging to other inputs,
sources and conversations. This prevents a known terminal failure from consuming
the package budget while unrelated worker scenarios wait for execution slots.

Verification runs in a clean worktree without credentials: no Git or GitHub credentials, tokens, account secrets, personal data, or production services, and no write access to any remote. Actual model and provider calls and subjective quality evaluations run separately in explicitly authorized environments.

[Integration](integration.md) decides how a passing candidate joins `dev`. A passing `foundation` does not authorize a [release](releases.md) or a deployment.

### External Claude program

Changes under `adapters/claude/`, `adapters/claude-acp/`, root npm manifests, `.npmrc` and `.node-version`
select automation, dependencies and Core acceptance. The dependencies check
validates exact npm versions, public registry sources, SHA-512 integrity and
install-script exceptions, then copies each npm project to a disposable directory
with an isolated home and no inherited credentials or npm configuration. It runs
`npm ci --ignore-scripts`, `npm audit signatures` and `npm test`. Adapter tests
use scripted runtime events and the real SDK's MCP registration without model
calls. Every command must succeed; missing signatures and invalid attestations
fail the check. The root and external program npm projects have independent lockfiles. The Claude ACP project runs `npm ci --ignore-scripts --omit=dev --omit=optional` and signature audit; its integration tests live in Go under `e2e/m2/`.

PR #523's [CI run 39785658738](https://github.com/thisisjun786/lina/actions/runs/39785658738)
reached the six-minute package limit while the current TUI lifecycle scenario
had run for two seconds. That branch initially used a ten-minute package diagnostic deadline. The
legacy acceptance batching from #127 covers that corpus with four disjoint
six-minute batches inside the shared components budget described above.
Individual operation deadlines and acceptance assertions remain in force;
timeout or missing observations still fail verification.

On PR #569, [CI run 39831123978](https://github.com/thisisjun786/lina/actions/runs/39831123978)
passed its first acceptance batch in 245.6 seconds, then reached the second
batch's six-minute diagnostic deadline during `TestM2UpdateDataOperations`;
the current `slow-backup` subtest had run for 24 seconds. No assertion failed.
Components used 800.9 seconds of the existing 895-second command budget.
That branch introduced three disjoint batches to leave room for uneven scenario
durations. The legacy component runner now uses four batches, preserving every
test, example and fuzz seed exactly once. A virtual
clock regression models a 400-second group of long scenarios alongside shorter
ones and requires the full corpus to finish under the unchanged package and
shared deadlines.

The next [CI run 39834045143](https://github.com/thisisjun786/lina/actions/runs/39834045143)
passed the first two batches in 251.7 and 234.3 seconds. Cold setup, builds,
analysis and ordinary tests had already consumed about 300 seconds, so the
895-second command budget left only 103.9 seconds for the final batch and
interrupted it before its own six-minute diagnostic deadline. No assertion
failed. That change set the shared command budget to 1195 seconds within a 1200-second
outer guard, retaining the five-second cleanup reserve and every individual
operation limit. A virtual-clock regression requires 300 seconds of setup
plus batches of 252, 234 and 300 seconds to finish in the same shared budget;
the existing exhausted-budget and hung-command regressions still require failure.
The GitHub job allows 25 minutes so checkout, tool setup and post-job cleanup
have room around that 20-minute component guard.
