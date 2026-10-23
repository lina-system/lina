# Headless acceptance harness

`m3/non-competition.json` runs installed Core's record tools through a local
fixture provider and its public client socket. It checks a real saved record,
refusal of an out-of-scope path used as an id, and preservation of an external
edit against an old revision while Core stays running. It is supporting
protocol evidence; the [Linux non-competition ladder](../evidence/non-competition/README.md)
still requires human concurrent-session measurements.

Run `go test -parallel=4 ./e2e/m1/...` (or `go test -count=1 -parallel=4 -v ./e2e/...` for uncached scenario
output). Node.js and the workspace's Go toolchain are required. The harness uses
native Linux and macOS pseudo-terminals; it needs no window, display server,
provider account, external network service or human input. Conversation screens
use the pinned terminal library and a VT emulator.

For the colour TUI, the PTY recorder reassembles renderer writes using the
existing cursor-preserving scroll-margin reset at the end of each write.
A PTY read can split or combine writes; it is not a completed screen. Plain
and connection-only output retain their streaming path. Deterministic recorder
tests split ANSI, UTF-8 and the write trailer, combine writes in one read, and
reject a truncated redraw. Screen predicates still wait for the relevant UI
state: a completed turn on an observer socket does not prove the TUI has
rendered it. LONG-QA waits for the idle viewport before stitching PgUp pages.

The suite builds each command once per test run and removes the shared binaries
after all scenarios finish. These test binaries omit DWARF debugger data to reduce
bundle copying and archiving. Test helper processes share a private SQLite Wasm
compilation cache, with the default runtime limits and canon's digest verification.
Only compiled code is shared; databases and results remain private to each scenario.
The parent removes the cache after all helpers join. Installed command binaries
and release builds retain their ordinary SQLite initialization.

Foundation enumerates the acceptance tests and runs
three disjoint batches under its shared components budget. This keeps a growing
corpus from exhausting one package watchdog while preserving every scenario,
assertion and operation deadline. Independent scenarios call `t.Parallel()` and
foundation limits each batch to four concurrent tests, including parallel
subtests. Each scenario owns its state, sockets, provider fixtures and cleanup;
the shared command-build cache is locked and removed after `m.Run`.
Same-bot Telegram scenarios, the Claude credential update scenario, environment-mutating tests, live-provider checks and subprocess entry points stay
serial. A direct `go test -parallel=4 ./e2e/...` still runs the complete suite in
one process. Each scenario starts real Core with a synthetic
installed identity and a private temporary state root, and connects a generated-type JSON-RPC client
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

`TestM1TUIFirstRunInstalledCore` uses a fresh installed Core and a 100x34 PTY
in Korean and English. It checks the wordmark and three catalog suggestions,
their removal on the first input while the fixture reply is held, and their
absence from Core snapshots and a reopened TUI after Core restart. The typed
input reaches the provider exactly once; no sleeps establish display state.

`m1/tui-speaker-gutter.json` runs two exchanges against installed Core and a
fixture provider in a 100x34 PTY. Parsed cells check speaker marks, foregrounds,
header bold and exactly one empty row between messages. ASCII runs retain words;
`NO_COLOR` runs retain ASCII speaker identity and emit no terminal escapes.

`TestM1TUIStreamingCodeInstalledCore` holds a fixture provider at an open
200-line code fence, its closing delta, and final completion through installed
Core and a real PTY. Result Details shows one muted notice plus six trailing
rows while open and all 200 rows on close, before completion. The test checks
the retained result text and restores the terminal without timed sleeps.

`TestM1TUISpeakerBoundariesInstalledCore` creates and selects a long-named agent
through the real TUI, then requests a fenced Korean reply from installed Core
in a 40x34 ASCII PTY. It checks provider routing, usable reply width, literal
empty code boundary rows, and stable completion metadata across draft redraws.
Both untagged and Go-tagged fences run through the same path, so syntax
highlighting must retain the code's leading and trailing empty rows.

`TestM1TUIIdentityFromAgentStore` installs a synthetic named agent with four
distinct persona layers, types "who are you" through the real TUI, and checks
the provider stub's request for that stored identity, owner and persona, plus
the exact enabled tool list. It checks request assembly, not model compliance
with the persona. The current boot conversation has no connected executors,
so its enabled list is explicitly empty.

`m1/provider-failure-history.json` runs HTTP 503 and SSE EOF after a delta
against installed Core, with input and history displayed through the real TUI
in a 120x60 PTY. It checks the failure notice and partial before and after a
graceful Core restart, no provider replay, and a new successful turn whose model
context excludes the failed partial. Notice wording is captured from the live
protocol rather than fixed in the scenario.

`m1/tui-interrupted-reply.json` holds a loopback Responses stream after one delta,
stops it with Esc in the installed TUI, reopens the TUI, and restarts installed
Core both gracefully and forcibly while that TUI reconnects. Stored snapshots
and terminal cells must retain the partial and its stopped outcome exactly once.
Provider call counts rule out restart replay; an explicit follow-up checks that
aborted partial text stays outside model context.

`m1/tui-stopped-order.json` stops a held Responses stream with `/stop`, completes
a second turn, and keeps the 120x60 TUI open across an installed Core restart.
The stopped outcome must remain between the first input and the second input
after reconnect, a resize to 121x60 and a fresh TUI. A direct Core snapshot checks
the authoritative order independently; exactly two provider calls rule out replay.

`m1/tui-resize.json` exercises the colour conversation screen at 80x24, then
40x12 and 120x35 through PTY resize signals. It retains a synthetic draft,
checks header, status and composer, and compares all rows and the cursor with
a fresh client at each size to detect stale cells. It clears the draft and
completes another exchange after resizing, then checks terminal restoration.

`m1/tui-channels.json` opens the installed channel controls and drives token
entry, private-chat pairing, binding and disconnect through the real TUI and
boot composition. A separate Core test process retains the production peer
ancestry checks; only Telegram HTTP replies are scripted. Boot package tests
also reopen the poller against durable connection and secret state.
`m1/telegram.json` starts production boot in process against a saved channel and
secret, without a model route. Only the platform HTTP transport is replaced:
the restored bot's identity lookup stays blocked until shutdown. READY and a
socket channel listing must succeed while it is blocked, the channel must show
unavailable without exposing the token, and Core must cancel and join the
request before stopping.
`m1/tui-talk-log.json` seeds a durable exchange, restarts installed Core, and
checks one summary row and its explicit ledger expansion without a model call.

The M1 acceptance map is [m1/README.md](m1/README.md). Helpers are test-only Go files in `m1/`, shared by scenario tests. Keeping
process creation in test files follows the architecture rule for command
acceptance harnesses. Existing persona-growth extensions retain the same harness and their `m2/` fixture; future milestones can add scenario files here; new
subpackages under `e2e/` are also covered by the registered package pattern.

`m3/websocket.json` runs installed Core through an HTTP upgrade on its authenticated
Unix client socket. A headless WebSocket client discovers and subscribes to the
conversation, submits an input, and receives the committed acknowledgement, a
live delta and the completed result. The fixture provider requests the real
`change_persona` tool and checks its execution result. Core restart must restore
the conversation rows and apply the saved tool effect to the next turn without
replaying the provider request. Its companion runs the same journey through production Core boot and the
Tailscale-authenticated listener over TCP, substituting only LocalAPI responses
and physical addresses. That companion installs the executable hosting boot so
normal build-digest checks remain enabled. Both compare stored input/result
identities and contents across restart. Neither claims a physical mobile device
or two-host tailnet test. All identities, model responses and stored values are
synthetic.

`m2/update-preflight.json` checks separate `core --preflight` processes against
compatible, older, newer, corrupt and WAL-backed stores while another owner holds
the Core lock. It compares every installed file's bytes and metadata, including
the WAL index, and checks normal old-Core refusal before control-file writes.
Its preparation companion hashes and unpacks a real command bundle, runs its
candidate preflight alongside a live Core, checks the durable preparation result,
rejects digest/artifact/peer failures and starts the prepared Core through the
public socket. Preparation keeps the idle and backup gate cells unknown.

`m1/import-reconciliation.json` runs the installed `lina import-hermes` command
on synthetic LCM and host databases. It reconciles every source table, including
explicitly omitted indexes and operational state, and checks each retained row
against both legacy-reference directions and the reverse mapper's hash. It also
checks rehearsal, repeat-import row counts, source bytes and metadata, unknown
schema refusal, and empty recall before promotion with a positive control after
promotion. `m1/import-promotion.json` additionally covers sidecars and backups.

`m2/persona-growth.json` uses a real Moirai worker process against Core's job
socket with a synthetic precomputed growth candidate. It checks forged proposal
refusal, next-turn persona application, a committed learning notice and one-action
undo through the authorized Core tool registry, and suppression of an undone
snapshot after reopening canon. The service harness uses the same private state,
process deadlines and cleanup as the M1 worker scenario; it needs no model or
personal persona text. Its installed-Core companion negotiates the growth capability,
adopts during an active reply, checks actual provider instructions on subsequent
turns, receives one conversation notice, dispatches undo, and reopens Core to
check suppression, history and job socket cleanup. No test-only Core service
composition is used in that companion.

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
go test -count=1 -v ./e2e/m1 -run 'TestResponsesRoundTrip|TestRealModelHeadlessConversation'
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
endpoint or a broken stream fails the journey. Ordinary fixture tests still run.
Builds and ordinary product subprocesses retain their isolated environment;
only the opted-in probe and the headless journey's Core receive the explicit
provider configuration.

`TestRealModelHeadlessConversation` installs the built binaries in private
temporary state and starts actual Core with the configured route. A typed
headless client negotiates conversation over Core's Unix socket, discovers and
subscribes to the conversation, and sends one synthetic greeting request. It
requires durable input acknowledgement, nonempty live deltas, completed assistant
items replacing the streamed identities, and an inactive turn. It then stops and
restarts Core and compares restored canon history with the live input and reply,
including text, order and item identities, under the same conversation id and a
new Core epoch. Output reports only counts and outcomes, never configuration or
reply text. The model call and restart have a two-minute deadline; cleanup
removes the installation, canon and child processes, including on failure.

`TestHeadlessConversationFixture` runs that same client journey without external
credentials. Its loopback Responses server waits for the client to receive the
first delta before completing the reply, proving streaming without timing sleeps.
Exactly one provider request across restart rules out replay.
`TestRealModelOptIn` checks that either missing opt-in or missing key skips before
configuration or model work. The real journey also reports **SKIP** in ordinary
credential-free CI; a successful provider probe alone remains insufficient
evidence of a client-level persisted turn.

`m1/tui-memory-agents.json` covers management commands in the installed Core and
real TUI. A loopback Responses fixture detects command leakage, verifies memory
inclusion before and after correction/forget, and checks privacy scopes. The
same scenario round-trips agent create/choose/rename/delete by id, sends a turn
in the chosen lane and verifies persistent deletion after Core restart.

The Core boot crash-test binary shares a private, invocation-scoped SQLite Wasm
compilation cache with its exec children. Canon still verifies the module bytes,
and each child opens fresh database/runtime state. Only compiled code is reused;
all tests still run uncached. The parent removes the cache after joining children.
This test-only setup reduces repeated compiler work on small CI runners.

`m1/tui-cards.json` exercises two clients racing one approval and stale group
membership through the production listener/authority path. The same scenario's
replay count is consumed by the Bubble Tea recording-writer test.
`m1/tui-questions.json` checks exact typed form answers and terminal publication.
`m1/tui-approvals-installed.json` uses the installed Core and TUI binaries, seeded
synthetic producer data, socket discovery/decisions, and a separate-key PTY approval.
These scenarios create no second product decision store and use no timing sleeps.

`m1/tui-syntax.json` runs `TestM1TUISyntaxInstalledCore`: an installed Core and
fixture provider stream an open Go fence, then its closing fence and final reply
into a 100×34 PTY. Parsed cells prove that open code remains unstyled and closed
code gains theme colours in dark/light; high-contrast and `NO_COLOR` remain
plain. The provider is released by a channel after the open screen is observed,
without timing-based assertions.

The M2 Discord scenario is `m2/discord.json`, executed by `TestM2DiscordCore`.
It installs the test host with a matching build manifest, runs production Core
boot in a separate process, and controls it through the generated socket
protocol. Synthetic outbound Gateway/REST and Responses endpoints demonstrate
channel input, read tools, background results, deduplication, shaping, blocked
state and restart without live credentials. `m2/discord-recovery.json` repeats
the journey across Gateway 4007/4009 closes, including command and approval-button
deduplication and Resume with the exact replacement session and committed sequence
after Core restart.

`m2/work-area.json`, executed by `TestM2WorkArea`, drives `prepare_work` and
`retain_work_result` through the installed Core's conversation tools. Two real
linked worktrees share a parent clone at separately pinned source commits.
Worker Git subprocesses create and commit each assigned branch; cross-task
updates and deletion are rejected without moving the original result head.
Core retains the fixture file and exact branch bundle, and the test restores
them from backup after Core restart and loss of the work area. Worker lifecycle
and receipt observations are seeded while Core is stopped; vendor dispatch is
covered by the worker adapter scenarios. Run with
`go test -count=1 ./e2e/m1 -run '^TestM2WorkArea$'`.

`m2/work-dispatch.json`, executed by `TestM2DispatchedWorkArea`, drives installed
Core's `dispatch_work` through a synthetic worker and GitHub boundary. The worker
creates its assigned branch at the detached base, produces a real commit and
attempts ordinary writes to protected refs. Assertions check the shared
`work/tasks` allocation, independent parent clone, unchanged source checkout,
clean result, artifact provenance and live-card equivalence to ledger replay.
The shared installed-worker harness gives each Linux Core a private loadavg
fixture through a Bubblewrap read-only bind mount. Its default is idle, so host
load cannot park unrelated worker scenarios. The capacity case first observes
`queued / host_capacity` with no attempt or launch, then lowers load and sends
a new conversation input. The same work produces its result once and is not
replayed after restart. Failures include dispatch state, wait, attempts and
worker launch observations. Production sampling, limits and timeouts are unchanged.
Run it with `go test -count=1 ./e2e/m1 -run '^TestM2DispatchedWorkArea$'`.

`m2/tui-composer-identity.json` installs Core/TUI into a temporary root and creates
and chooses another agent through the real TUI. It checks that the empty composer,
header and reply speaker name that agent before and after Core reconnect, using
a loopback Responses fixture and a real PTY.

`m2/records-korean-search.json` runs the installed Core and a real PTY TUI
against a loopback Responses fixture. It saves a Korean memo, checks the
attached-word and list controls, searches by the noun before and after an edit,
restarts Core and checks the same id, revision and note through search and read.
Run `go test -count=1 ./e2e/m1 -run '^TestM2KoreanRecordSearchInstalledTUI$'`.

`m2/github-ci.json` starts the real Core binary with a synthetic `gh` executable
in its isolated PATH. It negotiates the GitHub capability, refuses an unnegotiated
read, observes rate-limited CI pending and its retry deadline, then observes head,
review threads and successful checks. It verifies conditional headers and one
review read per head, with no network credentials or GitHub service dependency.

`m2/github-merge.json` drives the installed Core's merge capability with a
synthetic `gh` process and real temporary parent clone. It proves head and
protected-ref changes prevent writes, the accepted head is pinned, confirmed
results survive restart without a second merge, and external merges require
commit/ref evidence and report failed checks. It uses no external credentials.

`m2/codex-session.json` runs installed Core, its real conversation model loop,
tool authority and stdio child ownership with a synthetic installed Codex. It
asserts version reporting for an unverified version, active steering, confirmed
interrupt, idle continuation, a file produced by the worker and terminal output
in the originating conversation before and after Core restart. The worker checks
that no sandbox, approval, model-provider or config override is sent and that
Core's dedicated provider credential is absent. Run it with
`go test -count=1 ./e2e/m1 -run '^TestM2InstalledCodexSession$'`.
The opt-in `LINA_CODEX_LIVE=1 go test -count=1 -v ./internal/core/worker/codex -run '^TestInstalledCodex$'`
uses the user's installed Codex and own configuration. It verifies control and a
model-generated result without changing approval or sandbox settings.

`m2/claude-adapter.json` starts installed Core with the external TypeScript adapter
and a test-only vendor runtime. The runtime calls Core's real `skill_index` tool,
checks its receipt, streams an assistant result, and the scenario reopens Core
to verify durable history. It uses Node but no SDK installation, external service
or personal credential; the SDK boundary has its own npm suite.

`m1/conformance-agents.json` runs every shared `application/agents/` fixture
against installed Core over its public socket. It checks refusals for missing
and deleted agents in conversation and management requests, plus channel
binding/scope replacement. A paired synthetic channel and an existing binding
exercise the actual target-agent checks. Read-only canon snapshots prove that
refused requests preserve agent revisions, actions, conversations and bindings.

`m3/operational-switch.json` exercises the owner-only switch on the real Core
socket: missing approval, a failed transfer with explicit service restoration,
a held external task, successful transfer, and a real Core-launched fixture
worker output and receipt. It covers locally refused worker input, a confirmed
terminal provider connection failure, restart with an outstanding execution,
rollback refusal until drain, and preservation of originals, Git branches,
worktree registrations, receipts and journals after explicit rollback. The
external coordinator is synthetic; its call log checks that approval precedes
stop and durable LINA release precedes external restart. Imported tasks and receipt facts use the engine API while Core is stopped,
as in the M2 ledger scenario; worker start, interrupt, resume and result
observation use the running Core tool registry and a separate stdio process.

Installed-Core fixtures wait for command admission after connecting the socket.
An empty input must be refused as malformed (or capability-not-negotiated for an
unconfigured Core), rather than unavailable during recovery, before the scenario
continues. This probe cannot create a conversation event or model request.
Restored-memory and replay startup paths use the same readiness check as installs.

`go test ./e2e/m1 -run '^TestM2UpdateActivation$' -count=1` runs
`m2/update-activation.json`, which drives `lina update --activate` against
the installed Core. A test-owned service manager launches real service processes;
Core drains through its client protocol, snapshots and swaps, runs a real
conversation and bundled-worker readiness check, and resumes with a verified
tool result. Start refusal rolls back, while provider failure and updater death
after READY hold the new state. Package SIGKILL tests acknowledge each rename
boundary without fixed startup delays.

`go test ./e2e/m1 -run '^TestM2UpdateDataOperations$' -count=1` runs
`m2/update-data-operations.json`. It injects a 31-second delay into the installed
backup or restore command before executing the real CLI, crossing the installer's
30-second probe budget. The backup case completes activation, worker readiness
and a resumed tool result; the restore case recovers from a candidate start
failure and verifies a conversation on the restored Core.

`go test ./e2e/m1 -run '^TestM2UpdateClaudeCredential$' -count=1` uses the same
installed update harness with a synthetic Claude token and an empty login home.
The production Claude adapter must complete smoke using that token; the missing
token case must hold after READY. A bundled-worker probe checks its environment
before running the real readiness check, and outputs and durable state are
checked for credential disclosure.

`m3/crw-takeover.json` runs the operator import binary against an installed Core's
stopped state root, checks exclusive ownership and idempotent retry, removes the
source snapshot, then exercises the running Core's record tools. Imported evidence
is found through search and read by LINA id, source-id lookup fails, a new result
is saved, and restart keeps the imported identity. The board shows canonical
states for complete and incomplete issues and milestones, including earlier
milestone publications interrupted before journal acknowledgement. Retry and
restart preserve those records' saved revisions and projected deadline sources.
This operator-only test and
fixture are excluded from release archives together with the takeover tool.

`m2/claude-worker.json` runs `dispatch_work` with `worker=claude` through
installed Core. A headless fixture worker writes and commits a real artifact,
opens a synthetic PR and reports a final result while staying alive. Core must
reap it, retain the result and PR in an adapter-derived receipt, and preserve the
conversation result on restart. Other cases cover permission denial, exit zero
without a result and pending PR checks. No personal account or GitHub write is
used. Run with `go test ./e2e/m1 -run '^TestM2ClaudeWorkerThroughInstalledCore$'`.

`m3/antigravity.json` dispatches through installed Core's conversation tool,
starts its supervised headless worker in a new worktree, produces a real file
and commit, and verifies a recorded PR at that head. Separate cases cover soft
denial despite SUCCESS, exit zero without result, pending checks, and durable
conversation results without execution replay on restart. No personal worker
account or GitHub write is used.

`LINA_CODEX_LIVE=1 go test -count=1 -v ./e2e/m1 -run '^TestM2InstalledCodexLive$'`
also drives installed Core's normal tool path into the actual installed Codex,
then waits on the worker's conversation result before reading its snapshot. Only
Core's conversation provider is synthetic; the worker uses its own model setup.

`go test ./e2e/m1 -run '^TestM2CodexWorkTools$' -count=1` runs
`m2/codex-work-tools.json` through the built Core, its Unix client protocol and a
separate synthetic installed worker on stdio. It checks all four declared LINA
work tools, authenticated owner approval and question answers, worker-written
artifact contents, reported quota windows, and restored completion history.
Worker settings remain omitted. This scenario uses no real account or provider
credentials and does not measure live Codex quota.

`TestM1WebUnconfigured` checks installed Core's explicit missing-provider status, absence of web declarations and one-round notice. `TestM1WebSearchExtractAndCitation` runs the production Core composition in a separate test helper with a synthetic manifest recording that executable, and injects only the Firecrawl HTTP transport. The real TUI saves a hidden key; the protocol client approves each network request; the real provider adapter searches and extracts against stub responses; TUI cells show both running activities; the scripted model cites the source URL. No real provider account is used, and model compliance beyond the scripted boundary is not claimed.

`m3/worker-lanes.json` uses installed Core and a real headless TUI to show two
sessions of the same worker as separate folded lanes. A fixture writes a real
artifact and records its receipt, terminal observation and content verification
through the work service before startup. The second unfinished execution becomes
unknown during Core recovery. The scenario checks protocol identities, explicit
expansion with stage provenance, refresh, folding and reopening. It exercises
Core's read path and TUI. `TestM3WorkerLanesLiveSession` additionally starts the
installed stdio worker fixture through Core's conversation tools, makes it write
a real artifact, and expands its runtime lane to show the worker output without
calling the model. Vendor transport is synthetic; no personal account is used.

`m2/progress-waiting.json` uses the installed Core and synthetic stdio worker
journey to read the owner progress capability and full waiting list during an
approval and a question. It checks unknown plan values, display lines without
internal identities, removal after the original authenticated answers, a real
worker-written artifact, empty resolved sources after Core restart, and external record edits/restoration arriving on an already-open progress subscription without a new query. The
worker and provider are fixtures; Core, tools, protocol and persistence are real.

`m2/progress-seen-review.json` checks the installed Core's progress and running
views with work-service records and content-checked fixture artifacts. Opening
or dismissing a ready-for-review result removes its progress line and publishes
the change. Restart preserves the acknowledgement without changing the work
card, marking it applied or deleting the artifact.

`m2/tui-home-waiting.json` adds a 120x36 TUI to that worker journey. An outside
record change appears on Home alongside the worker's authenticated decisions.
Resolving the sources clears the summary and preserves the typed draft. The
scenario checks parsed terminal cells, the worker-written artifact and cleanup.

`m2/progress-controls.json` is exercised by `TestM2ProgressControlsSurfaces` and
`TestM2ProgressDigestWorker`. The built TUI and paired Telegram gateway send
explicit controls to running Core and inspect the durable target runs; the worker
scenario observes its approval, question and real artifact through the digest.
Only external provider and platform wires are scripted.

`m2/tui-board.json`, `m2/tui-note.json` and `m2/tui-usage.json` exercise the
installed Core and real PTY TUI. The board combines an outside-change waiting
request with a background run, updates its groups on landing and opens the
result. The note journey holds a background provider response after ten
foreground tool rounds, then checks one scoped follow-up in the next request.
The account journey checks unknown quota independently of status and reported
plan at 100 and 40 columns. External model wires and account data are synthetic.

`m2/work-definition.json` drives `dispatch_work` through installed Core. It
refuses missing done conditions, write areas and base-commit design sections,
including headings inside code examples, before a worker is queued. Valid work
receives the full packet and produces a committed file, observed PR and durable
receipt. Moving the source base during manager admission leaves the allocated
worktree and packet pinned to the validated commit. Worker and GitHub boundaries
are synthetic; Core, Git and the work ledger run normally.

`m2/worker-choice.json` runs installed Core through the conversation protocol.
Synthetic Antigravity and Codex streams prove unknown quota, blocked-worker
exclusion, one quota handoff with identical packet bytes, a second quota stop,
repository choice rules, and overlapping-area waits across restart. The worker
creates the real result file and Git commit; GitHub observations are synthetic.
The first quota-stopped process deliberately remains alive until Core reaps it,
and the replacement checks that it is gone before touching the workspace.

`m2/worker-quota-windows.json` drives a Codex quota stop with an exhausted
five-hour window and a healthy weekly window through installed Core. The
replacement worker commits a real result. Persisted quota keeps the windows
independent before and after restart, and selection at the reported short-window
reset does not wait for the healthy weekly window. Reset-boundary assertions use
explicit times rather than waiting for a provider window.
