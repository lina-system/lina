# Headless acceptance harness

Run `go test ./e2e/...` (or `go test -count=1 -v ./e2e/...` for uncached scenario
output). Node.js and the workspace's Go toolchain are required. The harness uses
native Linux and macOS pseudo-terminals; it needs no window, display server,
provider account, network service, terminal library or human input.

The scenario builds `lina`, starts real Core with a synthetic installed identity
and a private temporary state root, and connects a generated-type JSON-RPC client
to its Unix socket. A second process runs real `lina tui` with a controlling PTY
at 80 by 24 cells. The script waits for the `live` connection-state code, sends an
`input` request through the protocol client, checks the correlated
`capability_not_negotiated` / `refused` reply, and types `q` followed by Enter.
Another handshake and request prove that TUI quit leaves Core running. Core
then stops gracefully and removes its socket and run sentinel. Additional runs
exercise terminal EOF and an absent Core without creating state.

This is the connection-only increment of [T-01 and T-05](../docs/design/tui.md).
Core grants no conversation capability and the schema reserves the input body,
so the test sends `input` with an empty typed payload. The TUI displays connection
state; the protocol client observes the refusal. This does not establish a TUI
conversation, model reply, rendered cell layout, terminal-library spike results,
or full phase acceptance. Those scenarios extend this harness when their
capabilities ship, following the [phase rules](../docs/design/go-roadmap.md#phase-rules).
No product behavior or design contract changes in this increment.

Helpers are test-only Go files in this package, shared by scenario tests. Keeping
process creation in test files follows the architecture rule for command
acceptance harnesses. Future milestones can add scenario files here; new
subpackages under `e2e/` are also covered by the registered package pattern.

- State, environment and processes are isolated per scenario. Builds share only
  Go caches and tool locations; product processes receive no inherited account,
  provider, display or user configuration environment. All fixtures are synthetic.
- Readiness comes from socket connections and protocol/terminal observations.
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

`scripts/verify/scope.mjs` registers `e2e/` and `./e2e/...` with the `lina`
component. This makes Core/TUI changes select the suite even though their
subprocess dependency is invisible to Go imports. Changes to their kit
dependencies also select it through foundation's import graph. Foundation builds,
vets, analyzes and runs the suite uncached with the other component tests. The
selection regression exercises full runs, component-only diffs, transitive kit
changes and the documents-only exclusion in a disposable Git repository.
