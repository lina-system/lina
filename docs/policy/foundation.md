# Foundation verification

`node scripts/foundation.mjs` classifies a change, runs the applicable checks, and reports one result: `foundation`. It runs from this repository on the developer's machine, with Node.js and Git. [Integration](integration.md) runs it on every merge candidate before `dev` moves, and a [release](releases.md) requires it for the exact release commit.

## Running it

```sh
node scripts/foundation.mjs [--range <base>..<head>] [--full] [--no-note]
```

- `--range <base>..<head>` classifies the diff between two commits. Without it, the base is the commit the checked-out change was developed on: its merge base with `dev`, or the previous `dev` head when the checkout is `dev` itself.
- `--full` runs every current check, regardless of changed paths.
- `--no-note` skips writing the evidence note.

The script clears `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE` and every other `GIT_*` environment variable on entry; it works on the repository it runs in and takes the range as an argument. A run that integration or a release relies on happens in a clean worktree of the exact commit, in an environment that holds no credentials.

## Execution

```text
selection ----+---- docs --------+---- gate
              +---- automation --+
```

Everything runs in one process. `selection` records the exact base and candidate commits, the changed paths, the selected checks and the selection reason. It also checks Git whitespace errors: `git diff --check` over the range, or `git show --check` on the `HEAD` commit with `--full`. The selected checks run in parallel after selection. The gate only evaluates their results; it never reruns their commands. Each check has a bounded timeout.

## Evidence

When `foundation` passes on a clean checkout of the candidate commit, it writes its evidence as a JSON note on that commit under `refs/notes/foundation`: the head and base commits, the mode and selection reason, each check's result and duration, and the versions of the tools that ran. A run on a worktree with uncommitted changes, or on a candidate other than the checked-out commit, reports its result and writes no note.

[Integration](integration.md) pushes the notes with `dev`. A note saves time: a [release](releases.md) reads it for the exact commit and, when it is missing or does not show a passing run of that commit, runs `foundation` again on that commit in a clean worktree.

## Change classification

The executable path map is `scripts/verify/scope.mjs`.

| Change | Selection |
| --- | --- |
| Only explicitly listed root documents or Markdown directly under `docs/policy/` or `docs/design/` | `docs`; no automation installation or test run. |
| A verification script or shared repository configuration | Both `docs` and `automation`. |
| Empty or unavailable diff | Both `docs` and `automation`; never assume documentation-only. |
| An unmapped path in the changed paths or candidate tree | Both `docs` and `automation`, but the gate refuses to pass until its verification is registered. |

Deleted paths and both sides of a rename participate. A `.md` suffix alone does not make a file prose: prompts, executable examples, and test inputs need their consumer's checks.

## Current checks

- **docs:** validate local file targets in all tracked Markdown and `LICENSE`, including links from unchanged documents to deleted targets (`scripts/verify/check-docs.mjs`). This does not validate external URLs, heading anchors, or the meaning of prose.
- **automation:** check JavaScript syntax and run the Node behavioral suite of the scripts under `scripts/`. Tests that use Git run in disposable repositories with every `GIT_*` variable cleared. Integration and release tests run against a disposable bare remote; they never push to the real remote or publish a real release.

The scripts use Node 20 or newer and its built-in modules, and Git.

## Timing and budget

| mode | commit | machine | cache | wall s | bound s |
| --- | --- | --- | --- | ---: | ---: |
| full | not measured | | | | |
| docs | not measured | | | | |

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

The scope script reads the imports between components from the Go packages, so a change to one component also selects every component that depends on it. Until a component is registered, its paths remain unmapped and the gate refuses to pass. Register a vendored tree under `third_party/<name>/` the same way as any other component. `go test` runs its upstream tests unchanged.

### Path mapping

| Change | Selection |
| --- | --- |
| A path under a registered component directory | `components` for that component and its dependents. |
| A path under `third_party/<name>/` | `dependencies`, plus `components` for that tree and its dependents. |
| A `go.mod`, `go.sum` or Go workspace file, or the root `package.json`, `package-lock.json`, `.npmrc` or `.node-version` | `dependencies`, plus `components` for every component. |
| `scripts/verify/dependency-exceptions.json` | `dependencies`. |
| `THIRD-PARTY-NOTICES.md` | `docs` and `dependencies`. |

Markdown under `third_party/` is upstream content. The docs check skips it because its relative links point into the upstream repository.

### Checks

`dependencies` and `components` run after `selection`, in parallel with `docs` and `automation`. Selection lists the selected components. The gate evaluates both checks like the others: a selected check must report `success`, and an unselected check must report `skipped`.

- **dependencies:** runs the checks listed in the [dependency policy](dependencies.md#foundation-enforcement), including `go mod verify` and `govulncheck`. It runs without credentials and reads only the Go module proxy, the Go checksum and vulnerability databases, the npm registry, and recorded upstream repositories.
- **components:** uses Go from the shared `toolchain` line and runs `go build`, `go vet`, the `exhaustive` analyzer and `go test` with `-mod=readonly` for each selected component, in the directory of the module that holds it. The analyzer is a module tool pinned in `go.mod`, like `govulncheck` ([dependencies.md](dependencies.md)), and fails a switch over a closed set of variants that misses one. Build checks use the produced binary. Short component checks share one step and its module download. A component gets its own parallel step only when a measured run shows that the shared step is the bottleneck.

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
