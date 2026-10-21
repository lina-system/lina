# Headless acceptance harness

Run `go test ./e2e/...` (or `go test -count=1 -v ./e2e/...` for uncached scenario
output). Node.js and the workspace's Go toolchain are required. The harness uses
native Linux and macOS pseudo-terminals; it needs no window, display server,
provider account, external network service or human input. Conversation screens
use the pinned terminal library and a VT emulator.

The scenario builds `lina`, starts real Core with a synthetic installed identity
and a private temporary state root, and connects a generated-type JSON-RPC client
to its Unix socket. A second process runs real `lina tui` with a controlling PTY
at 80 by 24 cells. The script waits for the `live` connection-state code, sends an
`input` request through the protocol client, checks the correlated
`capability_not_negotiated` / `refused` reply, and types `q` followed by Enter.
Another handshake and request prove that TUI quit leaves Core running. Core
then stops gracefully and removes its socket and run sentinel. Additional runs
exercise terminal EOF and an absent Core without creating state.

The original connection-only scenarios still run with no configured model
route and assert an empty input's refusal. Conversation acceptance lives in
`m1/tui-conversation.json` and `m1/tui-conversation-stream.json`: the former
installs the binaries, starts installed Core with a local fixture provider,
types through the real TUI and checks parsed screen cells, input deduplication,
Core restart and history restoration; the latter scripts the fake Core's
streaming completion and steering sequence through the same PTY lifecycle.
They require no personal credentials. The physical terminal spike and real-model
acceptance remain separate.

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
go test -count=1 -v ./e2e -run 'TestResponsesRoundTrip|TestRealModelHeadlessConversation'
```

Keep endpoint values and credentials out of shell tracing, logs, reports and
commits. HTTPS uses normal certificate verification. Custom endpoints accept
API keys only; ChatGPT OAuth credentials cannot be redirected to them. No request
retries, redirects or substitute billing paths are enabled.

`TestResponsesRoundTrip/fixture` and `/real` run the same Core-side journey through
`internal/core/modelpath.ResponsesFromEnv` and the Responses adapter: send a
synthetic user message, observe nonempty live deltas, receive completed assistant
items and a terminal event, release the model lease, and save and reread a private
probe record in a temporary state directory. Only final items are saved; deltas
are display data. The probe record is a test artifact, not a canon turn. Output
contains event counts and outcomes, never the endpoint, key or model reply. The
test removes its state on completion, including failure.

Real tests skip when the opt-in switch or key is absent, including credential-free
CI and foundation. With both present, invalid configuration, an unavailable
endpoint or a broken stream fails the provider journey. Ordinary fixture tests
still run. Builds and ordinary product subprocesses retain their isolated
environment; credentials stay in the explicitly opted-in Core-side probe.

`TestRealModelHeadlessConversation` is the pending client journey. With real mode
enabled it starts actual Core in temporary state, performs the client handshake,
verifies the current input refusal and shuts Core down, then reports **SKIP**.
Core currently grants no conversation capability and its payload fields are
reserved. #55 and #108 must wire conversation input, streaming and canon
persistence before this test can assert the full TUI/headless journey. At that
point it must replace the refusal/skip with those assertions and pass the same
explicit provider configuration to Core. A successful provider probe does not
establish a client-level streamed or persisted turn. This PR adds no turn loop,
conversation schema, or CLI conversation route.
