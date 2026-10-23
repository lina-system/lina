# Headless acceptance harness

Run `go test -parallel=4 ./e2e/m1/...` (or `go test -count=1 -parallel=4 -v ./e2e/...` for uncached scenario
output). Node.js and the workspace's Go toolchain are required. The harness uses
native Linux and macOS pseudo-terminals; it needs no window, display server,
provider account, external network service or human input. Conversation screens
use the pinned terminal library and a VT emulator.

Scenario notes live in the doc comments of their `Test` functions. The M1
acceptance map is [m1/README.md](m1/README.md). Helpers are test-only Go files in
`m1/`, shared by scenario tests. Keeping process creation in test files follows
the architecture rule for command acceptance harnesses. Later milestones can
reuse these helpers and add fixtures or subpackages under `e2e/`; the registered
`./e2e/...` package pattern covers them.

For the colour TUI, the PTY recorder reassembles renderer writes using the
existing cursor-preserving scroll-margin reset at the end of each write.
A PTY read can split or combine writes; it is not a completed screen. Plain
and connection-only output retain their streaming path. Deterministic recorder
tests split ANSI, UTF-8 and the write trailer, combine writes in one read, and
reject a truncated redraw. Screen predicates still wait for the relevant UI
state: a completed turn on an observer socket does not prove the TUI has
rendered it. Scrollback capture waits for the idle viewport before stitching
PgUp pages.

The suite builds each command once per test run and removes the shared binaries
after all scenarios finish. These test binaries omit DWARF debugger data to reduce
bundle copying and archiving. Test helper processes share a private SQLite Wasm
compilation cache, with the default runtime limits and canon's digest verification.
Only compiled code is shared; databases and results remain private to each scenario.
The parent removes the cache after all helpers join. Installed command binaries
and release builds retain their ordinary SQLite initialization.

The Core boot crash-test binary shares a private, invocation-scoped SQLite Wasm
compilation cache with its exec children. Canon still verifies the module bytes,
and each child opens fresh database/runtime state. Only compiled code is reused;
all tests still run uncached. The parent removes the cache after joining children.
This test-only setup reduces repeated compiler work on small CI runners.

Foundation selects acceptance scenarios from their Go imports, helper and fixture
inputs, and the command packages they launch. It balances scenario-file shards
by measured time, keeping each file together. Files with indistinguishable test
names are grouped so every selected declaration runs once. Shared or unmapped
inputs select all scenarios. See the [foundation execution policy](../docs/policy/foundation.md#execution)
for selection, deadlines and result checks.

Pull requests exclude e2e by default; the `e2e` label opts into reached scenarios.
Pushes to `dev` and nightly runs execute the full corpus. Local foundation runs
one e2e shard at a time with `-parallel=4`; CI gives each shard its own runner.
Independent scenarios call `t.Parallel()`, including parallel subtests. Each
scenario owns its state, sockets, provider fixtures and cleanup. The shared
command-build cache is locked and removed after `m.Run`.

Same-bot Telegram scenarios, the Claude credential update scenario,
environment-mutating tests, live-provider checks and subprocess entry points stay
serial. A direct `go test -parallel=4 ./e2e/...` runs the complete suite, with a
separate test process per package.

- State, environment and processes are isolated per scenario. Scenarios share
  immutable command binaries built from the current source; installation tests
  copy them into private bundles. Builds inherit only Go caches and tool locations;
  product processes receive no inherited account, provider, display or user
  configuration environment. All fixtures are synthetic.
- Readiness comes from socket connections and protocol/terminal observations.
  Composer scenarios wait for both the composer controls and Connected status;
  controls alone can appear while initial conversation discovery is pending.
  Socket startup retries are bounded; there are no fixed sleeps. A recovering
  Core may explicitly refuse an input probe as `unavailable`; only that refusal
  allows a fresh probe. Malformed frames, wrong identities or correlations, and
  other replies fail immediately.
- Protocol frames and terminal lines have bounded readers. Operations and child
  lifetimes have deadlines. Cleanup closes connections and PTYs, kills and reaps
  remaining child processes, joins the terminal reader, and removes state even
  on failure. Ordinary quit checks keep the PTY open until the child exits.
- Assertions use state codes, protocol kinds, identities, epochs, effect states,
  exit codes and filesystem presence. They do not pin diagnostic prose or ANSI
  renderer bytes. The person still assesses UI and UX.

Installed-Core fixtures wait for command admission after connecting the socket.
An empty input must be refused as malformed (or capability-not-negotiated for an
unconfigured Core), rather than unavailable during recovery, before the scenario
continues. This probe cannot create a conversation event or model request.

The process harness keeps the spawning OS thread alive until the child is
reaped. Restored-memory and replay startup paths use the same readiness check
as installs.

`scripts/verify/scope.mjs` registers `e2e/` and `./e2e/...` with the `lina`
component. Foundation builds, vets, analyzes and runs selected tests uncached.
Its scenario planner follows subprocess command dependencies as well as Go
imports, so Core/TUI and transitive kit changes reach their acceptance scenarios.
The selection regression exercises full runs, component-only diffs, transitive
kit changes and the documents-only exclusion in a disposable Git repository.

## Real model mode

For QA and acceptance, use the intended real model (currently `claude-opus-5-5`),
not a cheaper substitute. Configure these variables outside the repository:

| Variable | Meaning |
| --- | --- |
| `LINA_QA_REAL_MODEL` | Set to `1` to opt into external model calls. |
| `LINA_RESPONSES_BASE_URL` | The endpoint's API prefix; the adapter appends `/responses` or `/models`. |
| `LINA_RESPONSES_MODEL` | The exact model to test. There is no default or fallback model. |
| `LINA_RESPONSES_API_KEY` | The endpoint's API key, supplied by the environment. |

After setting the endpoint, model and key privately, run:

```sh
export LINA_QA_REAL_MODEL=1
export LINA_RESPONSES_BASE_URL LINA_RESPONSES_MODEL LINA_RESPONSES_API_KEY
go test -count=1 -v ./e2e/m1 -run 'TestResponsesRoundTrip|TestRealModelHeadlessConversation'
```

Keep endpoint values and credentials out of shell tracing, logs, reports and
commits. HTTPS uses normal certificate verification. Custom endpoints accept
API keys only; ChatGPT OAuth credentials cannot be redirected to them. No request
retries, redirects or substitute billing paths are enabled.

Real tests skip when the opt-in switch or key is absent, including credential-free
CI and foundation. With both present, invalid configuration, an unavailable
endpoint or a broken stream fails the journey. Ordinary fixture tests still run.
Builds and ordinary product subprocesses retain their isolated environment;
only the opted-in probe and the headless journey's Core receive the explicit
provider configuration.

## Worker fixture stabilization

The shared harness passes its current numeric Go CPU budget (`GOMAXPROCS`) to
child commands while keeping their environment isolated. A subprocess regression
checks parent budgets of 1 and 3 against the child's actual runtime setting.

The host-capacity scenario observes its original dispatch's durable running claim
and area lease within the operation deadline while a fixture barrier holds worker
completion. It cancels that wakeup context before releasing the worker and checks
completion and restart within the scenario deadline. This keeps the wakeup check
independent of Git and manager delivery time while retaining exactly-once checks.
