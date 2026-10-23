# Runtime

This contract fixes what LINA's components are built from and how they run: the language and the build, the vendored source, the LINA kit, the conversation loop and its model path, the non-chat adapters, the tools and their sandbox, the tools LINA uses without owning them, storage, packaging, updates, recovery and remote access. It is normative: implementations must follow it, and any change to it is made in this file and joins `dev` under [integration.md](../policy/integration.md). Finishing this document does not mean any component is built or accepted; runtime proof belongs to the implementation issues that consume it.

## Scope

In this document, Node is LINA's device execution component and Node.js is the JavaScript runtime. The two names are never interchangeable.

This contract covers:

- the language, the build and the processes of every component LINA builds
- the vendored source and the LINA kit
- the conversation loop with its turn budget and the replay command, the model path through provider adapters, model-provider accounts and sign-in, and the non-chat adapters
- the conversation tools, plugin tools and the OS sandbox
- installed tools that LINA uses without owning them, including the coding tools LINA runs as workers
- storage engines, packaging, the `lina` command, the release manifest and the packaging and resident budget
- updates, recovery, failure injections and remote access

It does not cover the envelope and wire formats ([host-protocol.md](host-protocol.md)), grants, epochs, receipts and the approval policy ([main-authority.md](main-authority.md)), turn preparation, request assembly, persona, memory and compaction ([conversation-and-memory.md](conversation-and-memory.md)), the work engine, the kind-of-work rule and worker adapters ([work-and-delegation.md](work-and-delegation.md)), plugins ([integrations.md](integrations.md)), platforms and product families ([product-families.md](product-families.md)), the state root, backup generations and restore ([filesystem.md](filesystem.md)), screens ([surfaces.md](surfaces.md)) or dependency and vendoring rules ([dependencies.md](../policy/dependencies.md)).

## Language and build

LINA Core, `moirai-worker`, the `lina` TUI, the worker adapters, the provider adapters inside LINA Core, Node and the LIFE world engine are Go. They are built as static binaries per platform, and these components need no separate language runtime on any device. Apart from the LINA app (see below), the one component LINA writes that is not Go is the Claude adapter, a small TypeScript program on Node.js that is an external component (see below and Provider adapters). The LIFE world engine is part of the LINA Core family; its packaging is a backlog item ([cognition-and-life.md](cognition-and-life.md)).

Go fits these components because:

- LINA Core, the TUI and device Node each ship as a single static binary per platform, cross-compiled from one source tree.
- Code that workers write gets a fast edit, build and test loop, measured as V7 (see Runtime acceptance).
- An always-on main keeps a low resident cost.
- It suits a main that may later serve many people.

The LINA app is one React codebase in TypeScript, drawn with React DOM, and macOS usability comes first. On desktop it runs in a Wails v3 shell, which is Go and uses the operating system's own web engine: WKWebView on macOS, WebView2 on Windows and WebKitGTK 6.0 on Linux. Electron is the drop-in fallback behind the same bridge interface, and a different shell for an operating system is allowed. On iOS and Android the app runs in a thin WebView host, Capacitor by default ([surfaces.md](surfaces.md#app-front-end)). The shell's runtime is part of the app artifact. It is not a runtime that the person or another LINA component installs, and it is never called Node, which is LINA's device execution component. The TUI is not part of the app: it stays Go on Bubble Tea.

The conversation engine is LINA's own Go loop, built on the [Codex](https://github.com/openai/codex) tool contracts, with a structure that follows the [pi](https://github.com/earendil-works/pi) agent loop and conversation patterns from [Hermes Agent](https://github.com/NousResearch/hermes-agent) (see Conversation loop). pi and Hermes Agent are design references only: no code from either is vendored or translated line by line.

Each function has exactly one path. A function never switches to a second transport when the first one fails. A missing piece makes its function unsupported, and the function says so.

External components (document parsers, OCR, the local transcription component, the Claude adapter and similar) may be written in any language and need any runtime, Python, Node.js and a JRE included, and carry any license that is compatible with LINA's dependency rules in [dependencies.md](../policy/dependencies.md): copyleft code only because the component runs as a separate process, and never a non-commercial or source-available license. They run only as separate processes at a version pinned by the release, and are never linked or loaded into a LINA process.

## Installed tools

LINA uses some tools that the user installs and that LINA neither ships nor pins: coding tools such as Codex, Claude Code, Cursor, Gemini CLI, Grok or Antigravity, which LINA runs as workers ([work-and-delegation.md](work-and-delegation.md)), the command-line tools that plugins rely on, such as `gh`, the local server commands of third-party plugins ([integrations.md](integrations.md)), the official `grok` command that the Grok adapter drives, and the Node.js that the Claude adapter runs on. Each is a separate project with its own releases and repository.

- LINA uses the installed tool found on `PATH` or at a path the user configured, and records the version it observed.
- LINA lists the versions of each such tool that it has verified. A version not on the list is reported to the user as unverified, with the version found and the verified versions, and LINA still uses the tool. A function becomes unsupported only when its tool is missing, can't be started or connected to, or fails its protocol handshake, and LINA says why.
- LINA never installs, updates or pins the tool on its own, and installs nothing inside it.
- Nothing in this section applies to the Claude Agent SDK package. LINA installs that package itself, on first use of a Claude account, under Provider adapters.
- Nor does it apply to the `claude-agent-acp` program that lets the ACP adapter drive Claude Code ([work-and-delegation.md](work-and-delegation.md#claude-code-through-acp)). The release pins it with its own lockfile, and LINA installs it on first use of the Claude Code worker, under the same rules as the Claude adapter's lockfile, without its optional packages. It always runs the person's installed `claude`; LINA still never installs, updates or pins Claude Code itself.

## Processes

| Process | Built from | Runs as | Needed for |
| --- | --- | --- | --- |
| LINA Core | Go, a static binary | A service under the user's own OS user | Everything; it is the only canon writer. Agents are data that this one LINA Core runs; there is no process per agent ([product-families.md](product-families.md)). The message gateway for external channels runs inside it, with one adapter per platform ([integrations.md](integrations.md#channels)). |
| `moirai-worker` | Go, a static binary | An optional background service of the LINA Core install | Background memory (the maintenance pass and imports included) and planning |
| `lina` TUI | Go on Bubble Tea, a static binary | A user process connected to LINA Core over its local socket or the tailnet | The terminal surface ([surfaces.md](surfaces.md)) |
| LINA app | TypeScript on React, in a bundled shell: Wails v3 (Go) on macOS, Windows and Linux, a WebView host on mobile, Capacitor by default | A user process on the person's device, connected to LINA Core over its local socket or the tailnet | The app surface ([surfaces.md](surfaces.md)) |
| Node | Go, a static binary | A service on each device | Device execution under grants |
| Worker | The coding tool the user installed or signed in to, with the user's own configuration; Codex runs as `codex app-server`; Claude Code, Cursor, Gemini CLI, Grok and added agents run through the Agent Client Protocol, Claude Code under the pinned `claude-agent-acp` program; Antigravity runs as a headless child process ([work-and-delegation.md](work-and-delegation.md#workers)) | A stdio child of LINA Core, or of Node on a remote device | Workers, one child per task in flight |
| Claude adapter | LINA's own TypeScript on Node.js, an external component pinned by the release, with the Claude Agent SDK installed on the person's machine (see Provider adapters) | A stdio child of LINA Core, started on first use of a Claude account and stopped after an idle period | Model calls through a Claude account |
| Grok command | The official `grok` command the user installed | A stdio child of LINA Core, started for a turn and driven by the Go Grok adapter | Model calls through a Grok account |
| Sandboxed commands | Whatever a tool call runs | Children of LINA Core inside the OS sandbox | Shell and file tools |
| Background runs | Go, on the loop core of the LINA kit | Goroutines inside LINA Core, with no process of their own | Background research and analysis by LINA's sub-agents ([work-and-delegation.md](work-and-delegation.md#background-runs)) |
| Plugin processes | Local MCP server commands of installed plugins, and the command-line tools that plugins rely on when LINA Core runs them for its own reads | Children of LINA Core with the environment of sandboxed commands. A local server the person added by command runs inside the OS sandbox with the allowance the person granted (see Plugin servers). First-party plugins, the command-line tools LINA Core runs for its own reads and catalog entries that need more run under the user's OS user outside it, the entries only after the person accepts the card. | Plugins ([integrations.md](integrations.md)) |
| External components | Any language, pinned by the release | Separate processes | Parsing, OCR, local transcription |

The optional worker command is `moirai-worker --canon <path> --socket <path>`. Both paths are supplied by installation. It opens only an existing canon database in read-only mode and connects to Core's private job socket. `--once` runs one due source task per job and exits; otherwise `--poll <duration>` (default one second) discovers due work while maintaining at most one active step per job. SIGINT and SIGTERM cancel its work and join its tasks. Its environment is cleared before opening state, and its binary has no provider or secret-store dependency. Core's job service owns the 0600 socket in a 0700 directory and closes it and its connections on shutdown. On restart, while holding the Core state-root lock, it reclaims a stale private socket only after a refused connection probe; live listeners and unsafe paths remain untouched.

The worker reads its committed watermark and Core-owned deterministic/model policy on every attempt. Deterministic jobs submit the supplied proposal directly to Apply and need no model account or token allowance; model jobs continue through Aux. Core applies a proposal and its watermark atomically and retains idempotency receipts; a killed worker therefore resumes the next uncommitted task. An admitted auxiliary call belongs to Core and can finish after the worker disconnects, leaving a cached result for restart. A reservation with an unknown result after Core itself stops stays charged and is not automatically retried. Jobs use Core's shared budget and have independent switches, deadlines, schedules and adoption records. [C-56](go-core.md#processes-binaries-and-packages) defines the job API, accounting and write boundary. The Core job service and worker can be hosted independently of the client service; losing the worker or its socket affects only background memory and planning. The connection-only Core command does not yet install concrete memory/planning jobs or service units; those stay with their existing roadmap increments.

Provider adapters are never registered with the service manager: they start with LINA Core and end with it. The ChatGPT and Gemini adapters are Go inside LINA Core and open no process.

The current runnable Core increment is `lina core --state-root <path> --socket <path>`. After parsing its arguments and before boot changes state, it verifies and initializes the embedded SQLite engine and probes the pinned sqlite-vec version; a digest or initialization failure exits 78 without opening a client socket. It requires an existing private local state root with the installed main id, holds its non-blocking lock, durably advances its epoch and serves the client 1.0 handshake with that identity and epoch. The socket is 0600 in a 0700 directory. Handshakes work during RECOVERING; READY follows reconciliation. SIGTERM or SIGINT closes clients, removes the socket and run sentinel and exits 0, preserving the epoch. Tests use connection and startup-boundary acknowledgements rather than fixed startup delays. Concrete event, canon and reconciliation services attach through the startup coordinator; an existing canon store is refused until its implementation is linked. The install command supplies the identity and user service as described under The lina command. [go-core.md C-01, C-03, C-04 and C-42](go-core.md) define the control-file formats, start order and handshake behavior.

## Vendored source

Upstream code enters the LINA repository only as vendored source or generated upstream code, such as the Go types of the Codex app-server protocol, generated for each verified Codex version ([work-and-delegation.md](work-and-delegation.md)). How vendored code is kept unchanged, wrapped, patched, pinned and synced is defined in [dependencies.md](../policy/dependencies.md); the source, files and license notices are listed in [THIRD-PARTY-NOTICES.md](../../THIRD-PARTY-NOTICES.md).

## LINA kit

The LINA kit is the stateless Go module that LINA Core, the TUI and Node share. It lives in `kit/` of the LINA repository with its own `go.mod`, under the module path `github.com/thisisjun786/lina/kit`. LINA's own module uses it through a `replace` to that directory. It holds:

- the provider adapters of the model path: the Responses adapter (Go standard-library HTTP/SSE with `store=false`, the encrypted reasoning round trip and the stream assembly rules of this document), the Gemini adapter (the Go genai client), the Grok adapter (an Agent Client Protocol client over stdio) and the Go side of the protocol that the Claude adapter speaks
- the loop core of the conversation loop (see Conversation loop)
- the tool executor and the sandbox wrapper
- the protocol types of the `client` and `node` connections, generated from the JSON Schema in [host-protocol.md](host-protocol.md)
- document parsing, passage anchors, citation checks and the web fetch tool
- validation and plain-text rendering of the typed result bodies ([host-protocol.md](host-protocol.md#typed-results))

The web fetch tool is stateless: it takes one URL and hands the fetched document to the kit's document parsing. Its network access follows the caller's grant.

The provider adapters, the loop core, the tool executor with the sandbox wrapper and the protocol types are built as kit packages from their first implementation. Document parsing, passage anchors, citation checks and the web fetch tool are a separate stateless part of the kit: LINA's materials features use it, and it is not tied to any one materials feature ([materials-and-knowledge.md](materials-and-knowledge.md)).

The kit never holds a default state path, a persona, skills, an agent's knowledge scope or a database writer. Callers explicitly pass state locations, the speaking agent's persona and skills, and the knowledge that agent may read, and each caller stores its own records. What differs per host (writer fencing, queues, the layer that owns retries) stays in the component that uses the kit. The kit has its own version, separate from product and protocol versions, and every build records it in its release manifest. A provider adapter receives its credential from its caller and never reads the secret store.

## Conversation loop

The conversation engine is LINA's own Go loop inside LINA Core, built on the kit's loop core. One loop serves every agent. Each conversation runs with the persona and personal knowledge of its agent and the common knowledge ([product-families.md](product-families.md), [conversation-and-memory.md](conversation-and-memory.md)). The Codex app-server never runs a conversation, and a Codex home never holds conversation state. LINA only reads the user's Codex home: for discovery ([work-and-delegation.md](work-and-delegation.md)) and as external evidence ([conversation-and-memory.md](conversation-and-memory.md)).

The loop's structure follows the pi agent loop (`packages/agent`):

- a stream function at the model boundary
- a transform of the context before each request
- a hook before each tool call, which can approve or block it, and a hook after it, which can rewrite its result
- steering input during a turn and follow-up input after it
- cancellation through the whole turn
- parallel tool calls

The loop behaves as follows:

- Admission first commits input to canon's durable inbox, unique on conversation, source and native id. A duplicate keeps the original bytes and row id. The storage acknowledgement follows the commit and does not wait for a turn; a failed commit sends no acknowledgement. Turn execution uses one lane per conversation, in submission order, while other conversations can proceed independently. Shutdown cancels active turns and waits for them to return before releasing their services. The inbox and lane packages supply these boundaries; client admission, turn claims and steering integration are separate flows ([go-core.md C-09](go-core.md#conversation)).
- If the work engine fails, Core joins it and continues conversation cleanup: owner-view watchers, worker dispatch, worker sessions and stores. Shutdown returns the engine failure together with later cleanup errors. A timeout while joining a still-running engine does not permit closing the resources it uses.
- The inner loop runs tool calls and input that arrives while a turn runs (steering). The outer loop runs follow-up input after a turn ends.
- Tool calls of one response run in parallel, and their results return in the original call order. Argument validation and pre-tool checks run in order, before any call starts.
- A tool call in a response cut off by the output limit is never executed. The loop returns a result that asks the model to call again with complete arguments.
- On abort, calls that never started receive an `aborted` result. Started calls retain their executor result, including a success or unknown outcome already committed by authority, so the recorded history agrees with the effect receipt.

`kit/loop` implements these boundaries with the provider stream interface, a request transform, ordered before hooks, result-rewriting after hooks, and a caller-owned steering queue. It exposes streaming events to the caller while holding tool dispatch until the terminal event and clean end of stream. A stream failure returns without retry; incomplete-call results ask for complete arguments. Completed output items remain byte-for-byte evidence, while visible partials belong only to interrupted or failed history and never become completed assistant context. A response runs at most eight tools concurrently; writes to one schema-normalized target run in call order without blocking other targets. Each result is committed on completion, then ordered for the model. Custom-call text is normalized by the registered schema before dispatch; function-call arguments and all normalized dispatch arguments must be valid JSON.

`Run` closes steering admission atomically on every normal or error return and hands pending input back in `Outcome.FollowUps`. This includes input accepted between the final empty drain and the handoff. Once closed, `Inputs.Push` returns false; the lane keeps that input and routes it to a fresh queue for the next turn. Input is never left in a closed steering queue.

Core's `turn.Store` claims the inbox and creates the turn atomically, records preparation before the first request, and supplies the loop's commit callbacks. Its startup recovery closes unresolved calls and refuses to replay accepted turns, including a crash between a done output item and the complete assistant call message. Tool intents and receipts use the existing authority ledger through `turn.Tool`; uncertain effects remain unknown across restart. Approval suspension returns immediately, and `turn.ResumeApproval` dispatches the stored, hash-bound arguments under a fresh deadline after the answer. The caller installs the schema through canon migrations and runs recovery before admitting work.

The lane's steering and stop controls target only its active turn. Steering is consumed at the next round boundary; queue closure or cancellation returns ownership of later input to admission for a follow-up. The conversation question tool receives the authenticated ledger event at that boundary, so an answer can resolve its pending card within the active turn. Questions created during the turn become eligible only when later owner input is consumed; the decision keeps the answering event as its source. A stop preserves the typed issuer and waits for started tools to return before releasing the lane. Preparation failure also closes steering admission and returns its pending input.

For a turn bound to its input ledger event, the stop or restart interruption is one plain system line in that conversation. The ledger append and terminal turn state commit together, so a failed append leaves recovery retryable. Recovery after a process kill closes open calls, keeps completed receipts, records the restart line once and never reissues the accepted input. The surface and speaking agent come from the durable input event. This is exercised through the real lane, turn store, ledger and a killed child process; client conversation routing remains with the client-service increment.

When a streaming reply is stopped, its visible partials in the existing turn store commit with the aborted state and interruption line through the same atomic completion transaction as successful replies. Core seals the streamed block identities from that stored outcome and restores them before the interruption line after a restart. These partials are masked before storage and remain display-only aborted history, outside subsequent model requests. A failed terminal transaction cannot acknowledge a durable stop. The installed Core and headless TUI scenario `e2e/m1/tui-interrupted-reply.json` checks Esc, client reopen, graceful and forced Core restart, and no model request replay.

Before publishing a streaming text update, Core checkpoints its cumulative masked display prefix in that same turn store. A process kill during streaming therefore retains the already displayed text even though terminal cleanup never ran. Updates preserve each block's first publication order and identity; restart restores the same short-text or result-card projection before the interruption line. Checkpoints remain recoverable if the terminal transaction fails and are removed atomically with successful reply completion. They never enter subsequent model context. `e2e/m1/interrupted-partial-history.json` kills the installed Core while text is visible in the real TUI and compares exact live and restored rows, with one new restart notice and no accepted request replay.

The #55 tests exercise the loop with the Responses adapter over a scripted HTTP wire and reopen SQLite at interruption boundaries. Live provider access is not part of those tests. This increment has no client-protocol or TUI entry point; admission routing, startup composition and notifications remain with their consuming flows.

Provider failures also keep one plain system notice bound to the input event. The notice and failed turn state commit together through the same turn control and conversation ledger path as interruption notices. A failed notice write leaves the turn recoverable. History restores the notice and any streamed partial after Core restarts, without replaying the request or promoting unfinished text into a completed assistant message. Raw provider diagnostics do not enter the notice. The installed-Core headless PTY scenario in `e2e/m1/provider-failure-history.json` covers HTTP 503 and SSE EOF after a text delta, then a new successful turn after restart.


Background runs use the same loop core with a different tool set and request (see [work-and-delegation.md](work-and-delegation.md#background-runs)). There is one loop implementation, not a second engine, and the person's input reaches a run only as a follow-up to that run.

Retries and failover have one owner: LINA Core. The loop core and the SDK never retry on their own.

Conversation quality follows patterns from Hermes Agent (Nous Research):

- the person's standing preferences in every turn
- a brief about the person, synthesized from memory after each answer and used in the next turn
- memory written as statements, never as instructions to the agent
- memory review as one background maintenance pass, outside the request path
- a tool that searches past conversations in all of an agent's sessions and expands a hit or a summary to its exact content
- a compaction summary written as a handoff for reference
- older tool outputs condensed into one-line notes
- full-text search that finds Korean words
- a bounded query for what is known about the person
- a tool that reads a skill body the turn did not receive

Where each applies is defined in [conversation-and-memory.md](conversation-and-memory.md). Recall is prepared outside the request path, apart from a time-capped query on the person's current input, and a compaction summary, written with the conversation model, is prepared before the request that needs it; only an in-turn compaction that must also cover the earlier verbatim tail, and a continuation turn, build their summary when they need it.

The conversation ledger, turn preparation (including the executable hash and account readiness checks), request assembly, persona injection, compaction and resume are defined in [conversation-and-memory.md](conversation-and-memory.md).

### Turn budget

The conversation turn does only small, bounded analysis, so that the agent stays free to answer while the person talks. Heavy or long work never runs inside it. Coding work goes to a worker through the work engine, and research, analysis, multi-file reading, drafting and read-only checks go to a background run ([work-and-delegation.md](work-and-delegation.md#background-runs)). The person is never made to wait for either.

The budget decides only where work runs, in the turn or in the background. It never decides how much work is done or how well. The quality bar and the done condition of a piece of work are the same in the turn and in the background. Work that moves to the background runs to its real done condition with the same checks, and it has no shorter depth, fewer sources or skipped verification because it moved. No answer is shortened, summarized away or left unchecked to meet the budget.

A turn is small while it stays inside this budget. LINA Core measures it from the receipt of the person's input and enforces it itself, because instructions only steer the model. The values are starting values for the hand-off point and nothing else. They are tuned as the last bullet below says.

| Measure | Starting value |
| --- | --- |
| Acknowledgement | The first output delta of the turn within 3 seconds; the working state is shown at receipt |
| Soft budget | 6 tool round trips or 30 seconds of tool wall time |
| Hard budget | 10 tool round trips or 60 seconds of tool wall time |
| Conversation tool deadline | 15 seconds |

- A round trip is one model response with its tool calls and their results; the parallel tool calls of one response are one round trip. Tool wall time is the union of intervals spent inside tool executors, so overlapping calls are not added twice. Model generation time is not counted. Rejected tool batches still count as round trips.
- At receipt LINA Core sends the working state to every connected client, so the person never sees silence. When the request is obviously heavy, the agent hands it off in its first round trip and answers with one acknowledging line and a card, instead of spending the budget first.
- At the soft budget LINA Core runs no further tool call except one that starts a background run or dispatches work. It answers any other call with a typed result that says the budget is reached and nothing ran, so the declared tools and the cached prefix stay as they are. The agent then hands the rest off and the work continues in the background. It never presents what it has gathered as the answer.
- A partial result may be shown in the turn only when it is labelled partial, says what is missing and says that the work continues in the background. The reply that hands off is short: one line that says what moved to the background and that it continues, and a card.
- At the hard budget LINA Core ends the loop and hands the work off itself. If the turn has made no effect, LINA Core starts a background run from the person's input and the turn's tool outputs so far, with the standard read-only tool set. The run continues from the state gathered so far to the done condition of the whole request. If the turn has already made an effect, the reads and analysis that remain go to a background run in the same way, and further effects wait on a question card before the work engine dispatches them under its normal authorization checks. This promotion does not authorize an unspecified future effect, and an effect with an unknown outcome is never run again (see Execution rules). The turn ends with one short line that says what is done, what continues and what waits. Gathered state is never discarded, and the person is never asked to type "continue". Run admission and publication of its handle to the source turn commit together. Recovery starts an admitted but unreleased promotion once, without replaying the turn. Structured input and tool evidence are masked without corrupting their JSON.
- Installed Core wires the conversation hand-off to its shared background-run service. `start_run` stays in the offered tools at Soft, and Hard produces a durable hand-off notice and one run handle. The `running` capability exposes that work through the running topic and Ctrl+R. Completed results are retained as run materials; their notices enter the conversation at a lane boundary, including after restart, without replaying a completed provider call.
- The budget is checked again just before a queued tool starts. A rejected call returns `budget_reached`, meaning nothing ran, rather than `aborted` or `unknown`. The budget applies to reads and analysis. It never cuts a tool whose effect is running; the rule of Execution rules, "outcome unknown, do not retry", still governs that tool.
- A conversation tool that can take longer than its deadline returns a handle and continues in the background, as a shell command does with its session. It never holds the turn.
- Steering input during a turn does not reset the budget. A new turn starts a new budget, and a continuation turn continues the budget of the turn it continues. A report turn declares no tools and never sends tool results back for another model request; a provider attempt is recorded as rejected ([conversation-and-memory.md](conversation-and-memory.md#background-results)). A turn from an external channel follows the same budget as one from the app.
- Core applies the reply budget of [Reply shape](conversation-and-memory.md#reply-shape) to every reply that goes to a person: while a chat reply streams it stops showing the reply at the hard ceiling, and it counts breaches. A tool's output and the model's reasoning are not replies. The reply budget shortens the presentation only; the work behind the reply is complete.
- The budget does not apply to a background run. A run has no fixed time or call cap, and it stops only when its done condition is met, when it fails for real, or when the person stops it ([work-and-delegation.md](work-and-delegation.md#limits)).
- The starting values are tuned by measurement during implementation acceptance of the conversation engine. The measurement tracks waiting time and completion quality together: how often the person corrects, repeats or reworks a result, or says that it is not what they asked for, after a hand-off and after a reply that stayed in the turn. A change that lowers waiting time and raises those counts is a regression. Waiting time alone never tunes the budget.

### Replay command

LINA Core can re-run a recorded turn, sandboxed and without effects, so that a changed set of harness files can be compared with what the turn actually did. The harness files are the persona text, guardrails, memory guidance, tool guidance, sub-agent definitions and skills that a release ships. The command is `lina replay` ([The `lina` command](#the-lina-command)). It exists for development runs: a developer's loop calls it to score candidate files. LINA never runs a replay on its own, never in a conversation turn and never on a schedule, and a conversation never waits for one.

- Input. An episode written by the episode export ([conversation-and-memory.md](conversation-and-memory.md#episode-export)) or the id of a recorded turn of the install the command runs on, and optionally a directory of candidate harness files that stand in for the persona layers, skill bodies and instruction text that the turn recorded.
- Output. The replayed reply, its tool calls and its usage, written as files in a directory that the caller names. A replay scores nothing and judges nothing; the caller compares.
- A replay rebuilds each selected packet item from its recorded ID and revision and verifies the original request digest before applying candidates. Skills come from the recorded skill commit; the four persona-owned state projections come from their content-addressed ledger materials. Personal-memory text is never retained for reconstruction: it is re-read under current scope and removal rules. If memory is missing, forgotten or retracted, the whole turn is excluded as `forgotten`; if its revision or scope changed, the reason is `source_changed`. The manifest counts the exclusion. The packet clock is the recorded preparation clock, and memory source validity is checked at that instant. Unknown or insufficient assembly evidence remains `missing_assembly`.
- Remembered statements preserve the recorded profile order and use the live profile's single-line whitespace projection of the masked content. A correction successor also excludes the original memory as `source_changed`, even if its recorded revision remains available. Replay never substitutes the correction into the original request.
- Request assembly evidence that exceeds the ledger's inline limit is read from its exact material revision outside the canon read snapshot. Replay then rechecks current scope, retraction and material visibility before accepting it, as it does for other material-backed sources. If that material cannot be read, the turn is excluded as `missing_assembly` and export continues with valid turns. Cancellation and deadline failures still stop the operation.
- A replay uses the kit's loop core with the candidate files, on the model and effort that the turn used unless the caller names others. It runs in the sandbox with network blocked and with a scratch directory of its own, and it never touches the working directory of a conversation.
- A turn that only answered replays in full. A turn that read replays with LINA's local read tools live and the results of its network reads taken from the ledger. A turn whose tool call had an effect replays with that call answered by the recorded result, and is left out of the replay when no result was recorded. A replay never runs an effect and never writes the effect ledger or the conversation ledger.
- A replay's usage is recorded in its output as a run kind of its own, with the model account it used and each provider round. The installed development command writes no canon state, including budget reservations or settlements; its usage artifacts are separate from the install's budget totals ([Usage and cost](#usage-and-cost)).

The installed invocation is `lina replay <episode-file|turn-id> --socket <path>
--out <new-directory>`. `--model` and `--effort` override the recorded choices;
`--turn <id>` selects one episode from a multi-line export. An existing output
directory is refused before making a model call. The output contains
`replay.json`, `reply.json`, `tool-results.json`, `usage.json` and `manifest.json`.
Usage records identify `kind=replay`, the account and each provider round. An
excluded turn produces only the manifest, including its reason and count.

`--candidates <directory>` accepts UTF-8 `core.md`, `default-voice.md`,
`user-voice.md` and `growth-snapshot.md` text. Present files replace those pinned
persona layers; absent files retain the original layer. Candidate files are
instruction text, never executable code. Records from producers without exact
history, model or reconstructible context/skill assembly are counted as
`missing_assembly`. The command does not invent missing evidence. An episode
file names evidence of the connected install and must still pass its current
scope and removal checks. The original ledger clock is UTC; it is retained in
the episode and used for deterministic local recall.

The Core client protocol adds `export_request`, `export_result`,
`replay_request` and `replay_result` under the optional `replay/1.0` capability.
These operations are unavailable during recovery. The CLI owns external input
and output paths; Core owns export staging and never accepts a replay output
path or a candidate directory from the wire. No command starts Core implicitly.


## Model path

Every model call of LINA's own engines goes through a provider adapter. A provider adapter is the one place that knows a provider's wire, its sign-in and its limits; the loop, the ledger and request assembly see only LINA's own types. There is no model proxy: LINA runs no service of its own between its engines and a provider, and no adapter serves more than one account or passes a credential on. The only exceptions are the non-chat adapters. A function whose provider has no usable account is unsupported and says so. It never starts a billing path the person did not allow, and it moves new requests between connected accounts only by the routing of [Connected accounts and automatic setup](#connected-accounts-and-automatic-setup).

### Providers

The person signs in to each service themself, one account per service ([Accounts and sign-in](#accounts-and-sign-in)). There are four providers, in two fidelity classes.

| Provider | Class | Adapter | Account |
| --- | --- | --- | --- |
| ChatGPT | Model API | Go, in LINA Core: the Responses API with the person's ChatGPT plan | Sign in with ChatGPT (OAuth). It is also LINA's login and the default provider. |
| Claude | Agent runtime | The Claude adapter, an external component on the Claude Agent SDK ([Provider adapters](#provider-adapters)) | The person's Claude subscription |
| Grok | Agent runtime | Go, in LINA Core, driving the official `grok` command the person installed. Best effort. | The person's Grok account, signed in through that command's OAuth login |
| Gemini | Model API | Go, in LINA Core: the Gemini API through the Go genai client | An API key the person enters |

- **Model-API providers** (ChatGPT, Gemini). LINA Core owns the loop, request assembly, tool dispatch, the ledger and compaction. Every request carries the full input it needs, and the ledger records the exact request input.
- **Agent-runtime providers** (Claude, Grok). The vendor's process runs the inner tool loop of a turn, and LINA Core can only constrain it. Core still owns the turn, the ledger, the persona and context, compaction, and the authority of every tool. For each turn the adapter:
  - turns the runtime's built-in tools off and offers LINA's tools to it as MCP tools, so every tool call comes back to LINA Core and runs under the checks, approvals and effect ledger of this contract;
  - replaces the runtime's system prompt with LINA's, and loads no settings, skills, hooks, memory or MCP servers from the person's own setup of that tool;
  - turns off the runtime's own session persistence and compaction, or treats them as opaque;
  - records what the runtime reports with the request: usage, an estimated cost and a session id.
  The guarantees that need the whole request in LINA's hands are not available from a runtime: the exact request input as sent, and encrypted reasoning carried back unchanged. The ledger names the class that served each request. Claude and Grok may serve as the conversation engine and as background sub-agent engines, with this reduced fidelity.

Providers differ in what cuts cost and waiting time: prompt caches, cache marks and lifetimes, reasoning state, server state and discounted batch work. Each adapter declares which of these it can use for each model, and Core uses them under [Provider capabilities](#provider-capabilities). A request is never reduced to what every provider shares, and no provider feature is required for a request to succeed.

### Requests

The ChatGPT adapter calls the Responses API at `https://api.openai.com/v1` with the person's access token as the bearer, directly with Go's standard-library HTTP client and small request/stream JSON types. It never sends that token to a ChatGPT backend endpoint.

An explicitly configured OpenAI-compatible endpoint uses the same Responses
transport with its own API key. Core's configuration reader takes
`LINA_RESPONSES_BASE_URL`, `LINA_RESPONSES_MODEL` and
`LINA_RESPONSES_API_KEY` together. No setting selects a cheaper substitute model;
missing or invalid settings refuse the configuration. With none set, this route
is disabled. The base includes the API prefix; the adapter appends `responses`
or `models`, preserving that prefix. HTTP and HTTPS are supported; HTTPS uses
normal certificate verification. User information, query strings and fragments
are refused. A custom endpoint accepts only an API-key credential, never the
ChatGPT account's OAuth token. Redirects and retries remain disabled, and neither
the credential nor the endpoint appears in configuration diagnostics or the
release manifest. This configuration boundary does not itself implement the
conversation protocol, turn execution or persistence.

Real-model QA uses this Core configuration boundary and the Responses adapter
with `LINA_QA_REAL_MODEL=1`; the [headless harness](../../e2e/README.md#real-model-mode)
documents the opt-in environment and evidence. The Core-side probe checks the
provider stream and a temporary probe record. The separate headless conversation
journey runs installed Core with the same explicit route and checks a client's
durable input acknowledgement, live deltas, completed reply and turn completion.
Restarting Core must restore the same canon conversation and item identities,
order and text. A loopback fixture runs these assertions in CI and holds completion
until the client observes a delta. The real journey skips only when opt-in or the
API key is absent; configured failures fail acceptance. A provider probe alone
does not establish this client-level journey.

- `store` is `false` and `stream` is `true`, as the Sign in with ChatGPT surface requires. Every request carries the full input it needs.
- The adapter sends only fields that the Sign in with ChatGPT request surface accepts. `input` is an array. Instructions go in `instructions` or in developer-role items, never in system-role items. The fields that surface lists as unsupported (among them output limits, sampling parameters, truncation, `user`, `metadata`, `prompt_cache_retention`, and `previous_response_id` over HTTP) are not sent on it. Function and custom tools are declared in the form that surface requires. A field the surface does not document enters the request field list only after V1 shows it is accepted.
- Each request carries its conversation's cache identity as `prompt_cache_key` once V1 shows the surface accepts it ([Provider capabilities](#provider-capabilities)).
- Stateless requests include `reasoning.encrypted_content` regardless of whether an explicit reasoning effort is set. The [reasoning guide](https://developers.openai.com/api/docs/guides/reasoning) requires at least every reasoning item, tool call and tool output since the last user message to go back in the next request unchanged. LINA never sends an item of the current turn altered or dropped. When compacting earlier turns does not bring a follow-up request below the compaction threshold, LINA ends that turn and continues in a continuation turn whose request carries a compaction summary and the person's input once, so the round trip starts fresh ([Compaction and resume](conversation-and-memory.md#compaction-and-resume)). When no compaction runs, LINA sends every earlier item back unchanged as well, because the conversation history carries them all ([Compaction and resume](conversation-and-memory.md#compaction-and-resume)).
- Server state (`previous_response_id`, `store: true`, a provider's conversation or interaction objects), provider-side compaction, WebSocket mode and batch or flex tiers are provider capabilities. Each is used only when [Provider capabilities](#provider-capabilities) admits it for the route: LINA can recover without it, the ledger records its use, and the provider replay shows a gain. None is admitted today.
- The request fields LINA sends form a list per model-API provider in the release manifest. A new field enters the list before it is sent.
- Auxiliary reasoning (internal inference without tools) is a separate request with no tool list and no persona. It runs as its own run kind, can cause no external effect, and its output is never recorded as part of the conversation. A compaction summary is the one exception. It carries the conversation's fixed instructions and tool declarations unchanged, so it reuses the cached prefix, and Core runs none of its tool calls ([Compaction and resume](conversation-and-memory.md#compaction-and-resume)).
- The Gemini adapter follows the same rules wherever the Gemini API has an equivalent: stateless requests, every provider state item (such as thought parts) returned as received, and the same typed failures.

### Provider capabilities

Each provider adapter declares a capability set for each model it serves: the provider features that can cut billed input or waiting time. Core decides which to use, by fixed rules, from the request layout, the capability set, the ledger's times and the usage that providers report. No model makes this choice. The measurements behind these rules are in [provider-optimization.md](../research/provider-optimization.md).

A capability is used only under all of these rules:

- **Recoverable.** Core still assembles the full request from the ledger and records its digest before sending it. A capability changes only how the request travels or what the provider keeps, never what LINA records as the request. A cache miss, expired cache or provider state, a refused field or an outage costs only tokens or time. The request then goes in full without the capability, and a started request is never sent again ([Failures and retries](#failures-and-retries)).
- **Recorded.** The request evidence names each capability used, with its value: the cache identity's digest, the marked boundaries and their lifetimes, a server-state handle, a batch id. The usage record keeps what the provider reports: tokens read from the cache, tokens written to it by lifetime, and reasoning tokens. An unreported value is unknown.
- **Admitted per route.** A capability is used on a route only after the route's acceptance check (V1 for ChatGPT, V4 for the others) shows it is accepted. The provider replay must also show it lowers plain input or first-token time with no new failure. Its fields enter the request field list before they are sent, and the release manifest records the capabilities admitted for each route.

| Capability | What it does | ChatGPT | Claude | Gemini | Grok |
| --- | --- | --- | --- | --- | --- |
| Prefix cache | Reuses the unchanged start of a request | Automatic, written at the end of the latest user message or tool result | Only where marked | Automatic | Automatic, inside the `grok` command |
| Cache identity | Keeps one conversation on one cache | `prompt_cache_key`, after V1 | None | None | The command's own |
| Cache marks | Says where to cache | None on the plan surface | Up to four. On the runtime, on prompt blocks | None | None |
| Cache lifetime | How long an entry lives | The route's default | 5 minutes or 1 hour per mark. On the runtime, one setting per query | The provider's | The provider's |
| Reasoning state | Opaque state to return | Encrypted reasoning | The runtime's own | Thought signatures | The runtime's own |
| Server state | The provider keeps the conversation | Not offered on the plan route | Runtime session, not used | Interactions, not used | Runtime session, not used |
| Batch and flex | Deferred work at a discount | Not offered on the plan route | Not offered on the runtime | Batch and flex on the API key | Not offered on the command |

The rules for each capability:

- **Request order.** Requests follow the [request layout](conversation-and-memory.md#request-layout): fixed parts first, history, the person's input, then the ContextPacket. Within a conversation the tools and tool choice stay unchanged.
- **Cache identity.** Core derives one identity per conversation: a digest of the main id, the agent id and the conversation id. A background run uses its run id. The identity holds no name or text, and it stays for the life of the conversation, across compactions.
- **Cache marks.** Core marks four boundaries of every request:
  - after the fixed part (items 1 to 4 of the request layout, without plugin tools loaded on demand);
  - after the person's input of the previous turn;
  - after the person's input of the current turn;
  - in a follow-up request, after the newest item of the turn.
  An adapter whose provider needs explicit marks places them there; the others ignore them. The Claude adapter sends each earlier turn as its own text block of the runtime prompt and marks the last one, then sends the person's input and the ContextPacket.
- **Cache lifetime.** Claude marks use 1 hour, set once per query on the runtime. A 1-hour write costs more only on the tokens added since the last request, while an expired 5-minute mark makes the next request write the whole history again. ChatGPT, Gemini and Grok keep their route's default, and LINA sends no lifetime field that V1 or V4 has not accepted.
- **Reasoning state.** Every reasoning item, thinking block and thought signature returns unchanged in later requests, earlier turns included. Removing one changes the cached prefix as well as the model's state.
- **Compaction.** Compaction and its summary request reuse the cache as [Compaction and resume](conversation-and-memory.md#compaction-and-resume) says.
- **Server state and provider compaction.** Neither is admitted today. The plan routes refuse server state. On OpenAI's API, a chained request is billed for all earlier input again. xAI refuses instructions together with a chained request, so the chain cannot carry LINA's instructions on every request. A provider's own compaction returns a summary LINA cannot read, expand, check against retraction or delete with its sources.
- **Batch and flex.** A background job (not a turn, a continuation or a background run) may use a provider's batch or flex tier when its account's route offers one and the job's deadline is longer than the tier's window. Each submission is recorded. A batch that fails or passes its window runs as a normal request.
- **Provider replay.** One fixed, synthetic replay of LINA-shaped turns runs on each provider, with real accounts on opt-in. It reports, for every request, the input, read, written and plain tokens and the time to the first token. A change to a capability or to these rules carries its before and after from this replay. The episode export can feed the same replay from the person's own conversations on their machine, and those results stay off the repository.

### Stream assembly

The Responses adapter assembles the server-sent event stream itself:

- Only `response.output_text.delta` enters the visible text stream and partial history. Function-call argument and custom-tool input deltas are not assistant replies; their authoritative arguments remain in the done item, including when a response fails after streaming input. The failed-tool case in `e2e/m1/interrupted-partial-history.json` checks that both kinds stay out of live and restored conversation rows.
- An output item is final only at `response.output_item.done`. The recorded item, and the item sent back in the next request, is the done item. Encrypted reasoning is taken from `response.output_item.done`, never from `response.output_item.added`, whose value may be incomplete.
- Tool calls are dispatched from final items only, after `response.completed`. Until that terminal event the adapter buffers tool-call readiness; a cut-off, failed or incomplete response discards it. Deltas and done items may still be shown or recorded before completion.
- `response.completed` closes a response. It is never evidence that a product effect happened.

The Gemini adapter assembles its own stream by the same rules: deltas drive live display only, an item is final only when the provider marks it done, tool calls are dispatched from final items only, and a completed response is never evidence that a product effect happened.

### Failures and retries

- Automatic retries of an SDK are 0 for model-API providers, and the value is recorded in the release manifest. An agent runtime retries on its own and cannot be told not to; LINA Core records the retries the runtime reports and sets the runtime's turn and budget limits.
- Once output has started, a request is never sent again. An accepted turn is never replayed automatically; a turn whose outcome is unknown stays unknown.
- Failures are typed from the provider's own answer: account needs sign-in (authentication failed, refresh failed), not eligible, usage cap reached, provider unavailable (a failed connection, 503, 529), and request not accepted (an unsupported field or capability). A usage cap names no reset time that the provider did not give. None of these makes LINA retry on another account, another provider or another billing path.
- LINA Core reads the request id, the error type and the usage that the provider reports from every response and records them in the ledger with the agent id of the request.

### Models and roles

- Each adapter provides its own model list: for ChatGPT, the entries that the service lists for the sign-in; for Claude, the models the SDK reports as supported; for Gemini, the models the Gemini API lists; for Grok, the models the command reports. LINA Core records the adapter and the time of the list. A list is for display and is never capability or entitlement evidence. A completed turn is.
- The model policy has one entry per model role: `conversation`, and `background`, which serves background sub-agents and the model calls of background jobs. By default `background` uses the conversation entry. An entry names a provider account, a model and an effort, and every agent uses it.
- Model and effort are fixed at request boundaries: per conversation request, at worker thread creation and at the next turn boundary. A running request or an active turn never switches model or effort.
- LINA changes the model or the provider of a role only through the setup the person can see: the automatic setup and the quota-aware routing of [Connected accounts and automatic setup](#connected-accounts-and-automatic-setup), each told in one line and changed in one action. Outside them a switch is an explicit decision of the person and takes effect from the next request. When an account is capped or needs sign-in and no other connected account can serve the role, LINA says so and offers the switch; it does not start a billing path the person did not allow.
- A worker reaches its models through its own configuration ([work-and-delegation.md](work-and-delegation.md)). A worker's model calls never use LINA's provider credentials.

### Usage and cost

- Usage comes from what each provider reports: the `usage` of a model-API response, the usage an agent runtime reports with a turn, and the usage events a worker reports, such as Codex's `thread/tokenUsage/updated`. It keeps the tokens read from and written to the provider's cache, with writes split by lifetime where the provider reports that. An unreported value is unknown, never zero.
- The total budget covers conversation requests (turns in external channels and turns of agent exchanges included), background runs, the model calls of background jobs, worker threads, the sub-threads a worker starts, auxiliary reasoning. Development replays report their usage in output artifacts without changing canon or its budget totals. Every agent draws on the one budget, and each usage record names the agent it served, the account it used and, for a background run, the run.
- Budget totals count tokens. A cost figure is shown only when the provider or runtime reports one, and is labelled an estimate when it is computed on the client (the Claude runtime's is). A plan route has no cost. Catalog prices and zero values are not actual cost.
- LINA Core reserves and settles the budget. At the limit the conversation stops sending requests, a background run waits with its checkpoint and says what is done and what is not instead of being wrapped up, and workers are interrupted under the budget policy of [work-and-delegation.md](work-and-delegation.md). The Codex goal token budget is not used. This budget is the person's usage; it is not the turn budget, which only decides where work runs ([Turn budget](#turn-budget)).
- A worker's model, route and cost are known only as far as the worker reports them.
- A worker's remaining subscription quota is read from its adapter as quota rows (see Quota). Where a worker reports none, its rows are `unknown`, and LINA Core infers from the worker's quota errors and its own usage records.

**Quota.** Each account has quota rows, one per window the provider reports. A row holds:

- the provider and the account;
- the reported limit id and bucket, when available, independent of duration;
- the window: five-hour, weekly, daily, per-minute, spend cap or unknown;
- the share used, or unknown;
- when the window resets, or unknown;
- the state: ok, warning, blocked or unknown;
- the source: a response header, an event, an endpoint, an error code or LINA's own count;
- when it was observed.

An unreported value is `unknown`, never zero, and LINA never invents a reset time that the provider did not give.

Parking a worker preserves each window's reported state. An exhausted five-hour
window does not block a healthy weekly window; only exhausted windows determine
when the worker becomes eligible again. A stop without an identified exhausted
window adds an unknown-window block with no reset, preserving the reported
measurements rather than assigning the stop to a healthy window.
Quota freshness is checked for each provider, account and bucket independently.
Codex retains the limit id and primary, secondary or individual-limit bucket in
optional `LimitID` and `Bucket` fields. A newer observation for the same bucket
replaces an unknown duration when that duration becomes known. Different
buckets remain separate even when their durations match. Rows without bucket
identity keep their window-based matching only when the correspondence is
unambiguous; historical bucket ids are never inferred. A measured legacy block
without a matching identity remains blocked until its stated reset or a matching
observation. Old JSON remains readable and older code ignores the added fields.
A newer observation replaces only its matching bucket; an unchanged, older or
missing sibling cannot discard that update or clear another window's block.
An unknown-window stop without a measured bucket, inferred from an error, clears
on a newer observation for that account, without requiring every reported
window to be refreshed.
When a failed read leaves both provider and account unknown, the stop belongs
to the worker. A newer observation from that worker clears it; observations
from another worker do not. Known providers and accounts remain isolated.

| Path | What the provider reports | Source of the rows |
| --- | --- | --- |
| ChatGPT plan, LINA's own calls | No number. Usage-limit, eligibility and capability error codes only | The error code and LINA's own token count. A usage-limit answer makes the state blocked, with no reset time. |
| ChatGPT plan, a Codex worker | Primary and secondary windows, credits and plan | The worker's rate-limit read and update notifications |
| OpenAI API key | Rate-limit headers | The headers |
| Claude subscription | The window type, utilization and reset, when the limit changes; a usage report that may come back empty | The event, and the report where the token supports it |
| Grok login | Denial states only | The error code |
| xAI and Gemini API keys | Error codes; Gemini limits are per project in the provider's console | The error code and LINA's own count |

The ChatGPT rows for LINA's own calls and for a Codex worker are two rows, because the weekly cap that the person sets per app in ChatGPT applies to LINA's client only. Quota rows feed the quota view of the power settings ([surfaces.md](surfaces.md#settings)) and the choice of worker ([work-and-delegation.md](work-and-delegation.md)). A row informs a choice; it grants nothing.

### Request regression

A regression test compares LINA's requests with a baseline for each model-API provider: for ChatGPT, the request surface that the issuer documents for Sign in with ChatGPT and the requests recorded from the accepted turns of V1; for Gemini, the shape the Gemini API documents. It compares fields, `include`, the provider-state resend, the cache identity and marks, and the built-in tool declarations. The parts that differ by design are listed with the baseline and left out of the match: Codex's base instructions for coding work, which a LINA request never carries, LINA's developer-role persona and context items, and LINA's own tools ([conversation-and-memory.md](conversation-and-memory.md)). It runs again whenever a provider SDK pin, the request field list or a baseline moves.

## Accounts and sign-in

A model-provider account is the person's own account with one service. The person signs in themself, one account per service. LINA adds no terms gate: "not eligible" and "usage cap reached" are technical states shown on the account, not a gate. Agents own no accounts; every agent uses the person's accounts through the model policy.

### Credentials

- LINA Core keeps one credential record per provider account in LINA's secret store, which is the OS keychain, or files with mode 0600 in the secrets area of the state root where no keychain is available ([integrations.md](integrations.md#credentials-and-sign-in), [filesystem.md](filesystem.md)). The record belongs to the person and is kept under the person's id. It holds the provider, a label, the kind of sign-in (loopback OAuth, long-lived token, API key, or the provider command's own login observed), the client id and host id that the provider issued where it issues them, the scopes granted, when the access token expires, when the refresh token expires where that is known, the account's status (ok, needs sign-in, not eligible, usage capped) and the time of the last success. It holds no conversation content.
- Only LINA Core refreshes a token, and one refresh runs at a time per account. A replacement for a rotating refresh token is stored before it is used. A token is refreshed when about ten minutes of its life remain, and an idle LINA Core refreshes often enough that a refresh token never lapses. When a refresh fails, the account shows needs sign-in. LINA does not retry forever and does not fall back to another account.
- A token goes only into the one adapter process that serves its account, through the variable that adapter documents. It never enters a model request, a log, the sandbox or a worker, and a Node or a remote client never receives one. LINA never copies another tool's credential files, such as a worker's `auth.json` or the credential file of the Claude or Grok command, into its secret store. Where a tool keeps its own login, LINA reads the status of that login from the tool.
- Signing out revokes the token where the provider offers revocation, clears the record, and says "not confirmed" when revocation failed.
- Credentials are not part of a backup generation ([filesystem.md](filesystem.md)). After a restore, each account shows needs sign-in.

### Connected accounts and automatic setup

The person connects accounts one by one, and LINA sets up the rest. This is a default, not a gate: it asks the person for no decision and blocks no conversation.

- **Connected accounts.** One view lists every connection: the service, the plan it is on (for example ChatGPT Plus or Pro, Claude Pro or Max, SuperGrok, or a Gemini API key), whether it is signed in, and its remaining quota where the provider exposes it (see Quota). A plan is read from what the provider or its command reports, is unknown where nothing reports it, and the person may state it. General settings show it in plain words with no model id ([surfaces.md](surfaces.md#settings)).
- **Automatic setup.** When an account connects, and when its plan changes, LINA fills the model policy and the worker preference itself: which account, model and effort serve conversation and background work (the design-conformance review of delegated work is background work), and which worker takes coding work ([work-and-delegation.md](work-and-delegation.md#choosing-the-worker)). It uses what a plan already includes first, so that the person pays nothing extra. A pay-per-use API-key connection serves a role only where no connected subscription covers it and only within the budget the person set.
- **Quota-aware routing.** When the quota rows of a subscription show its limit near, LINA Core routes new requests and new work to another connected account that can serve the role, and says so in one line. It never starts pay-per-use usage beyond what the person set. This moves new requests only: a request that has started is never sent again on another account (see Failures and retries).
- **One line, one action.** After a setup LINA shows one line, such as "ChatGPT Pro + Claude Max connected: chat = Claude, coding = Codex", and one action changes it. Power settings show and override every choice. A general user never has to.
- **Plan catalog.** What each plan includes, its limits and its best uses are data in a catalog that ships with a release and is recorded in the release manifest. They are not logic in the code, and a release updates them.

### ChatGPT

ChatGPT is the default provider and LINA's login. The sign-in is the documented Sign in with ChatGPT flow for open-source and locally hosted apps. It is not the sign-in of the Codex tool, and LINA never uses the Codex tool's client id, scopes, credential file or backend endpoint.

- OAuth 2.0 with PKCE (S256) against the OpenAI auth issuer. On the first sign-in of a host, LINA registers itself dynamically (`dynamic_agent_client`, the app name hint "LINA", and one stable host id per host). It keeps the client id that the issuer returns with the account, and reuses the client id and the host id whenever the same account signs in again on that host.
- Initial registration requires the callback's issued `client_id`; `dynamic_agent_client` is never used in the code exchange or stored as an issued registration. Returning authorization omits the app name hint and uses the saved client id. A returned client id must match it, and the verified subject must match the selected account before credentials can change. Callback `iss`, when present, must match the fixed issuer; the ID token's issuer is always required and verified. These rules follow the issuer's [registration and sign-in contract](https://developers.openai.com/siwc/token-sharing-open-source/sign-in/).
- Scopes: `openid profile email offline_access resource.invoke chatgpt.tokens.use.direct`, with the resource `https://api.openai.com/v1`. Before any inference, LINA Core checks that the granted scopes include `chatgpt.tokens.use.direct`; an ID token alone does not authorize plan usage. LINA Core verifies the ID token against the issuer's JWKS: issuer, audience, expiry and nonce.
- The redirect is a loopback address, `http://127.0.0.1:<port>/<fixed path>`; the path is fixed and only the port varies. The listener starts before the system browser opens. The listener belongs to the client the person is using, and the sign-in runs through the sign-in relay ([integrations.md](integrations.md#consent-through-the-client), [host-protocol.md](host-protocol.md#client-connection)): LINA Core builds the URL and keeps the PKCE verifier and the state, the client relays `code`, `state` and `iss`, and LINA Core makes the token exchange. Where a client cannot listen, the person pastes the redirected URL.
- The fixed callback path is `/auth/callback`. A pasted URL must match the exact redirect URI of its attempt, including the port; duplicate fields, fragments, user information and mixed pasted/structured callbacks are refused. An attempt belongs to the initiating client connection, expires after ten minutes and is consumed before exchanging its code. Replacing the attempt or disconnecting cancels it. Token exchange and JWKS reads are bounded, do not follow redirects and do not retry; endpoint diagnostics never reach the client.
- The issuer-bound refresh callback uses the saved client id, rotating refresh token and API resource, omitting scope to retain the grant. Returned scopes replace the prior scope list only after the token record is stored. The ID token is verified in Core and discarded after admission; it is never put into an authorization URL returned to a client. Reauthorization uses the saved client id and validates the newly returned subject.
- Tokens: the access token lasts one hour. The refresh token lasts 30 days and rotates. Core's account actor stores the access and refresh tokens together before returning the new access credential. It refreshes about ten minutes before expiry, including while idle, and waits for active turns before exchanging. A durable needs-sign-in marker precedes each exchange so a restart cannot replay a possibly consumed refresh token. Failed refresh or persistence requires sign-in; there is no automatic account switch. Successful refresh preserves an existing eligibility or usage-cap state. Re-sign-in stores the verified granted scopes with the replacement. A temporary keychain read failure before exchange reports credentials unavailable without invalidating the account; idle refresh retries the storage read after one minute.
- Inference is the Responses API of Requests, with models from the service's list. A usage-limit answer is a state of the account, not an error to route around: it is shown with a "Manage usage" action, and no request moves to another billing path.
- The plan route reports no number for quota, only error codes (see Usage and cost).
- The flow has no device-code form. A LINA Core on a host with no browser takes the sign-in through a client's browser, by the relay. If the issuer does not accept the relay's redirect, the person adds an OpenAI API-key account instead. That is a choice the person makes in "add account", never an automatic fallback.
- The sign-in covers the plans the issuer lists (Plus and Pro today). A person on another plan sees "not eligible" and may add an OpenAI API-key account.

### Claude

Claude is the person's Claude subscription, used through the Claude Agent SDK. The SDK exists for TypeScript and Python only, so the Claude adapter ([Provider adapters](#provider-adapters)) is a small Node.js process that wraps it. It serves one account, speaks LINA's provider-adapter protocol over its standard input and output with one JSON message per line, and passes no credential on. It is an adapter, not a proxy.

- Sign-in, in this order. LINA first looks for the login that the person already has with the vendor's command, by the command's status report; if there is one, the adapter uses it and LINA stores nothing. Otherwise LINA guides the person through the vendor's token command, which prints a one-year token for model requests only. The person pastes it, LINA keeps it in the secret store and gives it to the adapter as `CLAUDE_CODE_OAUTH_TOKEN`. LINA runs no OAuth client of its own against claude.ai. LINA records the day the token was made and warns before the year ends; a failed call shows needs sign-in.
- The adapter's environment holds none of the variables that outrank the subscription token: an API key, an auth token, a key helper, and the cloud-provider variables.
- The vendor's bare mode never reads a subscription login, so the adapter cannot use it. Isolation is explicit instead: the options named under Providers, an empty working directory of the adapter's own, a pinned version, and a check of the runtime's start-up message, which lists the tools, MCP servers and plugins it loaded. The adapter refuses to serve a turn when that list holds anything LINA did not put there.
- The product names it "Claude" or "Claude Agent", never "Claude Code" or in that tool's styling.
- Quota comes from the rate-limit events the runtime emits (window, utilization, reset) and from the SDK's usage report where it returns one for the token the adapter holds. The cost the runtime reports is an estimate.
- A Claude API-key account can stand in for the token without any change to this contract.

The #167 adapter is in [adapters/claude](../../adapters/claude/README.md), pinned
to SDK 0.3.308 with a separate npm lockfile. Node 24.20.0 runs its TypeScript
without a bundled compiler or runtime. Explicit Core route configuration uses
absolute Node/program paths and an exact model; ambiguous provider configuration
is refused. Each request owns its child and its lease until the child is joined.
The stub acceptance covers tool/result correlation, init rejection, cancellation,
missing versus reported usage, and a persisted reply through installed Core.
This is not live V4 entitlement evidence; account installation and idle pooling
remain #168. Source releases carry only the LINA program and lockfile, and binary
release assembly retains its explicit file allowlist without the SDK.

### Grok

LINA runs no OAuth client against the Grok service, because the service documents no registration for other applications. The Grok adapter drives the official `grok` command that the person installed ([Installed tools](#installed-tools)) over the Agent Client Protocol (`grok agent stdio`), on the same ACP client as the ACP workers ([go-core.md](go-core.md#work-engine), C-60), because only that interface carries tool approvals. The command's headless print mode is read-only streaming with fixed allow and deny rules, and is used only for one-shot calls.

- The person signs in with the command's own OAuth login: the browser, or a device code on a host with no browser. LINA stores nothing. It learns the state of the account from a cheap authenticated call, not from the command's files.
- The adapter sets the command's telemetry and trace-upload switches explicitly, to off, restricts its built-in tools and offers LINA's tools as for any agent runtime.
- A Grok API-key account is the alternative where the login route is unavailable. A Grok plan does not include API credits.
- The adapter is best effort. Its approval and tool-restriction behavior is verified by V4, and it sits behind the same interface as the others so that a direct OAuth client can replace it if the service documents one.

### Gemini

The Gemini account is an API key. The person enters it, LINA keeps it in the secret store and sends it in the `x-goog-api-key` header through the Go genai client. There is no OAuth sign-in for Gemini in this design: the documented OAuth route is a testing setup that needs the person's own cloud project, the login of Google's command-line tool is not offered to other software, and a Google AI plan does not cover API calls. Settings says so beside the key field. Quota is the project's limit in Google's console, which the API reports only by error code.

### LINA login

"LINA login" means one thing: the owner's ChatGPT sign-in. The first successful ChatGPT sign-in binds the issuer and the subject of its ID token to the owner's person id in canon ([product-families.md](product-families.md#people)). Every later ChatGPT sign-in made as LINA's login must present the same subject. A different ChatGPT account may be added as a provider account, but it is never taken for the owner and never changes the binding. The binding and the provider accounts are separate records, so the login can later be replaced without touching model credentials. A main whose owner has not signed in with ChatGPT has an owner with no binding and works the same way.

The binding is not remote access. It is made once, on the main. No client and no device presents it when it connects: Tailscale identifies devices, and every client connection acts for the owner (see Remote access). It opens LINA Core to nobody outside the tailnet, and LINA runs no account service for it.

## Non-chat adapters

The Jev judge, embedding and transcription each have their own adapter, separate from the provider adapters. Their keys, billing and failure diagnosis are separate from the chat accounts, and none of them appears in the chat model list or in automatic routing. Jev is called only through its own typed adapter, the judge adapter, which speaks to a hosted judge API (Jev by default) or to a local judge process ([cognition-and-life.md](cognition-and-life.md)).

The embedding adapter calls the embedding endpoint the person configures: an OpenAI-compatible embeddings API and its key. Each vector index records the model id and the dimension it was built with. When the model changes, the index is rebuilt in the background, and until the rebuild finishes recall uses full-text search (FTS5) without the vector index. With no endpoint configured, recall does the same. An embedding call sends no span flagged `secret`. A vector index of imported history is built with the embedding model of this adapter; vectors from a Hermes install are not imported. When history is embedded is defined in [conversation-and-memory.md](conversation-and-memory.md).

None of them blocks basic conversation:

- When embedding is off or failing, recall keeps working at lower quality, as defined in [conversation-and-memory.md](conversation-and-memory.md).
- Transcription defaults to a local transcription component pinned in the release, which needs no key and no setting. An external transcription API is optional, and the transcription adapter is that optional path. With no transcription at all, only recording-to-meeting-notes is unsupported.
- When no judge is available (Jev is off or has no key, and no local judge is running), rules and the main model produce the same function ([cognition-and-life.md](cognition-and-life.md)).

Each adapter reports its connection state, key presence and usage to settings. An adapter that is off reports its state and is never required at first run.

## Tools and sandbox

### Tool set

The conversation engine's tools are the same kind as a Codex worker's, declared with Codex's names and formats: shell execution (`exec_command`, `write_stdin`), file editing (`apply_patch`), `view_image`, `update_plan`, a question tool and a permission request. LINA adds its own tools: reading materials and memory, searching conversation history, summaries, memory and records (`recall`) and expanding a result or a summary to its exact content (`expand`), asking what is known about the person (`recall_person`), reading a skill body, the memory tools (write, correct and forget, defined in [conversation-and-memory.md](conversation-and-memory.md)), dispatching work, starting, following up and stopping a background run ([work-and-delegation.md](work-and-delegation.md#background-runs)), reading plans, presenting, reading and saving results (`present_result`, `read_result` and `save_result`), and querying its own events ([self-diagnosis-log.md](self-diagnosis-log.md)). Once LINA's materials take in web pages, LINA also declares the kit's web fetch tool as its own tool (see LINA kit). Under the default approval policy a fetch is a read and never asks ([main-authority.md](main-authority.md)).

The result tools are LINA Core's own tools and run in LINA Core, outside the sandbox, like the memory tools. `present_result` validates the body against the schema and the size limits of its type and returns the result id, the revision and the text that the model sees ([conversation-and-memory.md](conversation-and-memory.md#results)). `save_result` runs only on the person's request in the current turn and returns the effect state. None of them reaches the network or asks for approval. The `update_plan` checklist shows an agent's own steps and is never a result.

Plugin tools are the tools of the plugins that are on ([integrations.md](integrations.md)). Each is named `<plugin>__<tool>`, at most 64 characters, and plugin tools are declared in a deterministic order. When there are many, the conversation declares a subset and loads the rest on demand. The release manifest lists the built-in tools. Plugin tools come from the user's installed plugins, so the conversation ledger records the hash of the plugin tool declarations sent with each request. When a name would exceed 64 characters, LINA Core shortens it with a stable hash suffix and keeps the map to the server's own tool name. The effect ledger records the server's own name.

Tools are declared in `tools` on every request, and a change to the tool list applies from the next request. `apply_patch` is a custom tool where the provider accepts custom tools. Otherwise its provider adapter declares it as a function tool with one string argument that carries the same patch text, and the executor receives the same patch either way. For an agent-runtime provider, the tools are offered to the runtime as MCP tools (see Providers). LINA's `apply_patch` accepts the Codex patch grammar, and the grammar's test fixtures from Codex (Apache-2.0) run as regression tests with their notice kept.

Conversation and work are split by the kind of work, not by tools. The routing rule is in [work-and-delegation.md](work-and-delegation.md).

### Execution rules

- Before a tool runs, LINA Core checks the person the call acts for, the agent that makes it, scope, grant, target, revision and current epoch ([main-authority.md](main-authority.md)).
- Every tool has a deadline and always returns a result. The default deadline of a conversation tool is 15 seconds (see Turn budget). A failure before the effect starts returns the failure and its reason. When the deadline passes while the effect runs, or its outcome can't be known, the tool returns "outcome unknown, do not retry" and the effect ledger records `unknown`. Effect states are defined in [host-protocol.md](host-protocol.md).
- The effect ledger deduplicates tool effects by the idempotency key defined in [host-protocol.md](host-protocol.md), never by call ID. The same effect called again with a new call ID never runs twice; the call returns the first result, or `unknown`. The record and key of a plugin write are defined in [integrations.md](integrations.md).
- An effect outside the grant, and every plugin tool call, follows the approval policy of [main-authority.md](main-authority.md), including the rule for when no surface can present an approval request.
- A plugin call that can take long runs as a background job, bounded by the time the plugin's manifest states. A call that waits for the person's input is parked as a background item and never holds the conversation turn ([integrations.md](integrations.md)).

### Default scope

- The conversation working directory is `workspace/` under the state root, one directory that every agent uses ([filesystem.md](filesystem.md)).
- The user files LINA reads by default are the library scope: the set of connected sources ([filesystem.md](filesystem.md)). Nothing enters it unless the person connects it.
- The default grant, before any approval, is write access in the working directory and read access in the library scope.

### Shared writes

Every agent works in the same working directory, so two agents can change the same file. No agent locks another out; the checks below decide, and the record shows which agent changed what:

- `apply_patch` follows the Codex patch grammar and fails when its context lines no longer match the file. The agent reads the file again and retries on its current content.
- A shell write, such as one through `exec_command`, can't be checked before it runs. The effect ledger's record of the tool call holds the changed paths, their hashes before and after the call, and the agent id.
- Writes to materials, connected sources and work records keep their own revision checks: a write against a stale revision is refused with `stale_revision` ([main-authority.md](main-authority.md), [materials-and-knowledge.md](materials-and-knowledge.md)).

### Sandbox

Shell and file tools run inside an OS sandbox that LINA Core invokes directly through the kit's sandbox wrapper. Shell and file tools never run unsandboxed. Replays of recorded turns run in it as well (see Replay command). Plugin processes are not shell or file tools. A local server the person added by command runs in a sandbox of its own (see Plugin servers). First-party plugins and the catalog entries that need more run outside it. Every plugin process is also controlled at the tool call ([main-authority.md](main-authority.md)).

- The write roots are the conversation working directory and the grant's scope. Everything else is read-only, and `.git` directories inside a write root stay read-only.
- Network is blocked by default. A sandboxed command gets network access in one of two ways:
  - A command that an installed plugin's service profile names, such as `gh` or `git` under the GitHub plugin, gets it by default, and asks first only when the profile marks the command as outward or irreversible ([integrations.md](integrations.md), [main-authority.md](main-authority.md)).
  - Any other command gets it when the model asks for network for that command and the person confirms, as in Codex. The first request asks, with the choices once, always allow and deny. "Always allow" records a standing grant for commands with the same prefix, which the person revokes in settings at any time ([main-authority.md](main-authority.md), [surfaces.md](surfaces.md)).
- Sandboxed commands get LINA Core's environment without model-provider keys, so the user's own tools and settings, including the GitHub setup of [integrations.md](integrations.md), work there as they do for the user. LINA's secret store, which holds the provider credentials, and data keys are never visible inside the sandbox.
- The policies follow the Codex sandbox source (Apache-2.0), with provenance recorded. The active sandbox policy is recorded in the release manifest.

On Linux the sandbox is [bubblewrap](https://github.com/containers/bubblewrap) with seccomp. bubblewrap runs as a separate executable and is never linked. Commands get their own user and PID namespaces, their own network namespace while network is blocked, and `no_new_privs`. The seccomp filter is a compiled BPF program shipped with the release for each architecture and handed to bubblewrap by file descriptor.

LINA Core runs on the Linux main ([product-families.md](product-families.md)), so the conversation engine's shell and file tools always run in the Linux sandbox. The kit's sandbox wrapper also serves hosts other than Linux: the commands that Node runs on macOS and Windows.

On macOS the wrapper uses Seatbelt: `/usr/bin/sandbox-exec` with an SBPL profile derived from the Codex profiles. Apple marks `sandbox-exec` deprecated and offers no command-line replacement, so the wrapper checks that it works on every start.

On Windows the wrapper runs commands only inside a verified Windows sandbox. A verified Windows sandbox is part of Windows Node acceptance; until it passes, sandboxed commands report unsupported on Windows instead of running unsandboxed.

LINA Core checks on start, and before the first tool call, that the sandbox works on this host. Unprivileged user namespaces, AppArmor restrictions and hardened kernels can each disable bubblewrap. When the sandbox is missing or fails, conversation continues, only shell and file tools are unsupported, and the user sees why.

The Linux acceptance scenario in `e2e/m1/sandbox.json` exercises both a missing launcher and a failing probe through production Core composition over installed state with an explicit launcher path. An installed TUI message receives a provider reply, the turn completes and reopening the TUI restores the reply without another model call. The scenario also verifies the failure cause in Core's startup snapshot and the localized TUI setup notice without leaking private diagnostics.

The wrapper's `lina-sandbox-1` policy ships compiled seccomp programs for Linux amd64 and arm64, with separate blocked-network and authorized-network variants. Other Linux architectures fail closed. The source, pinned Codex provenance and regeneration instructions live in `kit/sandbox/policy/README.md`. The filter rejects alternate syscall ABIs, namespace and mount changes, process inspection, kernel keyrings, io_uring and VM sockets. It forces `clone3` callers to fall back to filtered `clone`. Authorized network permits IP sockets without removing filesystem masks or syscall restrictions. Host Unix-socket connections remain denied in both modes, including hard-linked socket aliases; local socket pairs remain available for subprocess IPC.

Core passes the canon, secrets, system, version, backup, materials, derived and staging areas and its client socket as protected paths. The wrapper resolves symlinks and refuses writable grants overlapping those paths. Existing Git metadata in writable roots, including nested repositories and gitdir indirections, is mounted read-only. Its writable ancestors are anchored against rename/replacement. Gitdir files are opened without following symlinks or blocking on special files, checked as regular files and read with a 4 KiB limit. The probe uses the actual launcher and a synthetic denied-read canary, verifies Linux seccomp and no-new-privileges, and has a five-second deadline. The first tool call repeats it; an unavailable wrapper never starts a command. A canceled caller or invalid/released lease does not latch a confinement failure or consume that first-call recheck. Causes are stable codes (`launcher_missing`, `unsupported_platform`, `unsupported_architecture`, `invalid_policy`, `probe_setup_failed`, `probe_failed`); explicit probes also report transient `probe_cancelled` or `invalid_lease` without changing the wrapper’s availability. Core exposes the status to its tool composition and reports `shell_file_tools_unsupported` with the cause while leaving the client service running. Linux discovery tries an absolute PATH result, then `/usr/bin/bwrap`, `/bin/bwrap`, `/usr/local/bin/bwrap`, `/opt/homebrew/bin/bwrap` and `/home/linuxbrew/.linuxbrew/bin/bwrap`; macOS uses `/usr/bin/sandbox-exec`. Empty PATH does not hide an installed launcher. Only executable absolute paths qualify; a trusted explicit launcher pin fails closed and does not fall back. After probing, Core publishes a boolean availability view on the core topic; TUI presents one localized setup notice when false. Raw cause codes remain in diagnostics.

`kit/proc` captures Core's environment, removes known provider credential families and any additional provider variable names supplied by the caller, and preserves the user's remaining tool configuration. It also removes loader and shell-startup injection variables. Each command has an explicit environment and requires a live activity lease. Cancellation, shutdown and leader exit clean up owned descendants before the command returns and releases its activity lease. Commands still inherit the service manager's cgroup when one exists; cgroup delegation is not an install requirement.

On Linux, `kit/proc` starts commands through the explicit `proc-supervise` subcommand of the current host executable, registered by both `lina` and `lina-node`. The helper sets `PR_SET_CHILD_SUBREAPER` before starting the command. It discovers immediate children through the parent PID in ordinary `/proc/<pid>/stat` records, kills and waits for them, and repeats for reparented descendants until `wait4` reports `ECHILD`. It does not require the optional per-thread `children` interface or `CONFIG_CHECKPOINT_RESTORE`. Session and process-group changes, intermediate-parent exit and a cleared environment cannot detach a descendant from this lifetime. The helper receives command arguments and environment over a private control descriptor, preserves the command's stdio and extra descriptors, and reports startup errors separately from exit status. Closing control, including when the owner process dies, requests cleanup. Each command has its own supervisor, so owners do not reap one another's commands or unrelated children. Hosts embedding the kit register `proc.SuperviseMain` explicitly; there is no automatic initialization hook.

On macOS, cleanup is best effort. Each command receives a random `LINA_PROC_OWNER` environment token inherited by ordinary descendants. The owner retains the unreaped leader until cleanup finishes, so its PID and group id cannot be recycled. It signals the group while the leader is live, then scans same-user processes with `KERN_PROCARGS2` and kills processes carrying that exact environment entry, rechecking process identity and credentials before signalling. It repeats the scan while tagged processes remain, with a bounded cleanup deadline. A Darwin `EPERM` is ignored only after reinspection proves that the same identity has ended or become a zombie; permission failure on a live child remains an error. A child which clears its environment or makes its token unreadable can escape. This is an acknowledged gap, not an unprivileged equivalent of a subreaper or Job Object. Seatbelt still inherits confinement across children and denies protected file and Unix-socket access even with network enabled.

On Windows, the process owner creates each command suspended, assigns it to a Job Object, and only then resumes its initial thread. Assignment failure kills and joins the suspended command before returning an error. The job disallows breakaway and is not inherited as a handle; descendants remain members even when they change console groups or detach from the console. Cancellation, owner shutdown and leader exit terminate the job. The call retains its lease until the leader is joined and the job reports no active processes, including descendants holding stdio open. Job handles use kill-on-close as a final resource cleanup guard. This process lifetime mechanism grants no filesystem or network confinement; Windows sandboxed commands remain unsupported until the separate sandbox acceptance passes.


The explicit internal `lina file-tool --root <directory> --versions <private-directory>` subcommand runs the importable `kit/tools.Main` implementation. Core invokes it directly with argv through the sandbox wrapper for one JSON request and reply; there is no hidden re-exec mode or additional binary. The file sandbox grants access to a dedicated private version directory while the command sandbox denies it. Existing protected Core paths stay denied in both instances. File operations and commands share one executor per workspace.

File requests select no host root, environment or network policy. Core supplies the sandbox's file root and a separate private directory for retained versions. Reads return a revision of the bytes and mode. Writes, deletes and restores compare the expected revision immediately before the filesystem operation; creation requires absence. Every changed path gets a receipt and a synced, recoverable before/after copy, including files removed by `apply_patch`. Restore checks retained digests and the current destination, so recovering a deleted file preserves its bytes without replacing a newer file. Patch context is exact rather than whitespace-fuzzy, and every hunk is checked before the first destination changes. An interruption during a multi-file commit is reported as unknown with retained recovery information.

For an existing file, publication first captures its inode in a reserved `.lina-recovery-*` directory on the same filesystem. The private saved-version record names this slot before the capture, and the worker verifies the captured bytes against the checked revision. New bytes are linked into place only while the destination is absent; an external replacement is never clobbered. A changed capture is restored only into an absent destination and the result is unknown. Any observed concurrent change or uncertain publication returns unknown with the retained version and recovery path. No cooperating-writer lock is required. The captured inode is kept even after success so later writes through an already-open editor descriptor remain recoverable; successful receipts describe the completion-time observation. Capture directories are excluded from workspace manifests and share workspace access, while immutable before/after versions remain in the separate private store. File reads can inspect a capture after the worker restarts; the retention workflow owns its eventual purge.

The network executor normalizes the installed command prefix and target into the approval scope. Neither a model-supplied prefix nor an outward-policy override is accepted. A pending request returns to the conversation without starting a command; the authenticated once, always-allow or deny answer goes through Core's durable approval service. A new dispatch after the answer receives a fresh tool deadline. Standing permission does not survive revocation at the next admission.

### Plugin servers

A local server that the person added by command runs in an instance of the OS sandbox that is separate from the sandbox of shell and file tools.

- It writes only in a data directory of its own under the state root ([filesystem.md](filesystem.md)) and in the folders the person granted on its card. Everything else is read-only.
- Network is blocked unless the person turns it on for that server on its card. Narrowing network to named hosts needs a filter that the sandbox wrapper does not have (see Deferred).
- It gets the environment of sandboxed commands plus the variables its manifest names. The values of credential variables come from LINA's secret store and are visible only to that server's process, never inside the sandbox of shell and file tools.
- The allowance is a grant recorded with the person who gave it. The card shows it, and the person revokes it there.
- Where the sandbox is unavailable, such a server does not start and the plugin shows failing with that cause. Conversation continues. There is no unsandboxed fallback for a server the person added by command.

## Workers

Workers are coding tools the user installed, driven through one worker adapter per tool; Codex is the first, through its app-server protocol over stdio. LINA uses the installed tool as it is (see Installed tools): its version, login, model provider, sandbox, approval mode, plugins and integrations are the user's own configuration. A worker runs on one task with a task packet (the work definition), writes the code and the pull request, and returns a completion receipt that names the pull request ([work-and-delegation.md](work-and-delegation.md)).

Several workers run at once, one per task in flight, each in its own worktree. The work engine decides how many ([work-and-delegation.md](work-and-delegation.md)). LINA Core, or Node on a remote device, starts every worker as a child process or protocol session it owns, so a worker's finish, failure or exit reaches LINA Core as an event with no polling. The one exception is a worker whose tool offers no such signal: its adapter reads the session at a short interval, only while a task is in flight.

The adapter boundary, the Codex adapter, compatibility, readiness, assignment, approvals, the development method and merges are defined in [work-and-delegation.md](work-and-delegation.md).

## Storage

- Structured state lives in SQLite, used from Go through `github.com/ncruces/go-sqlite3 v0.30.5`. This cgo-free binding runs its embedded SQLite 3.51.2 Wasm engine through wazero and builds with `CGO_ENABLED=0`. The binding version, Go module checksum, engine version and engine SHA-256 are recorded in every build manifest. No other database engine or SQLite binding is used. [The binding evidence](../../evidence/sqlite-binding.json) records the C-16 capability checks and the remaining V2 platform and residency work.
- Vector search uses [sqlite-vec](https://github.com/asg017/sqlite-vec) at a pinned version through that binding, linked into the binary or loaded as an extension as the binding requires. A sqlite-vec platform binary loaded at run time is verified against the digest in the release manifest before it loads.
- Canon databases run in WAL mode with `synchronous=FULL`. `internal/core/canon.OpenSQLite` applies both settings to every new physical connection and limits the pool to one connection; the canon writer owns transaction serialization and epoch fencing. `OpenSQLiteReader` opens existing files with `mode=ro` and `query_only`. These entry points accept filesystem paths, escaping URI metacharacters. Full-text search uses SQLite's FTS5. How recall uses FTS5 and sqlite-vec is defined in [conversation-and-memory.md](conversation-and-memory.md).
- The full-text index of the conversation ledger and the full-text index of memory each record their tokenizer and fallback. The nodes and links of the summary trees, and the legacy references and import batches of an import, are canon tables ([conversation-and-memory.md](conversation-and-memory.md)).
- A SQLite file that another application keeps, such as a database of a Hermes install, is read through a read-only connection with the online backup API into the import staging area. It is never copied as a file, WAL file included, and never opened for writing.
- Vector indexes are derived data, rebuilt from canonical text. Moving the sqlite-vec pin rebuilds the indexes and never touches canon.
- Backups use SQLite's online backup, never a copy of database or WAL files. `canon.BackupSQLite` copies committed pages through the binding's backup init/step/finish API, including from a read-only source with live WAL. It checks cancellation between bounded page batches, requires a new destination, and removes its own incomplete output on failure. What a backup generation holds is defined in [filesystem.md](filesystem.md).

The optional sqlite-vec path uses a custom Wasm engine with sqlite-vec linked into it, built against this binding's ABI. `canon.UseSQLiteWASM` accepts that engine only before storage initialization and verifies its bytes against the manifest's SHA-256 before use. It retains the verified byte snapshot, so a later file replacement cannot change the code loaded. Native SQLite shared libraries are not supported. Version 0.30.5 retains this configurable Wasm path; newer wasm2go releases require a different extension integration. Builds now embed a compatible SQLite 3.51.2 engine with sqlite-vec v0.1.6. Canon verifies the complete embedded engine against the pinned SHA-256 before initializing SQLite or creating a database. The build manifest records that same digest and the probed extension version. [Reproduction and notices](../../packaging/sqlite-vec/README.md) identify the exact sources and tools; ordinary builds and service startup need no extra download. FTS5 remains the recall path without an embedding endpoint. An unrelated upstream prebuilt Wasm module is not assumed compatible.

`canon.Open` takes an explicit database path, the already durable unsigned epoch, the held control-file lock and numbered migrations. One writer goroutine owns its dedicated connection, including initialization and migrations. On an empty database the first atomic statement installs `canon_meta` with the current epoch. Every later write transaction, including each migration and epoch mirror, starts with a conditional epoch update before any caller SQL. Epochs are canonical decimal text, compared by length and then lexically to preserve the full unsigned 64-bit range. A lower epoch returns `stale_epoch` and permanently latches that writer instance, even if the stored epoch later changes. Reads use a separate read-only pool. `OpenReader` and a read transaction reject writes with the closed `read_only` code. Transaction callbacks cannot end transactions, change the fence or schema-version metadata, or attach another database; callbacks must not retain their transaction, call the store recursively, or perform external work.

Migrations start at 1 with no gaps and run forward, each in its own transaction; `PRAGMA user_version` advances only with that migration's commit. A failed migration rolls back its DDL and version while preserving earlier committed migrations. Under the held Core lock, `Inspect` and `PeekEpoch` open all source files strictly read-only. With committed WAL present they let SQLite reconstruct a private temporary WAL index, leaving the original database, WAL and shared-memory files unchanged; they never ignore live WAL using `immutable`. Without WAL, immutable read-only inspection avoids creating sidecars. Before opening a writer, a schema newer than the supplied migration list returns `schema_too_new` with startup exit status 78, leaving all source files unchanged. The lock must exclude concurrent writers throughout inspection and startup.

`SQLITE_CORRUPT`, `SQLITE_NOTADB`, or `SQLITE_IOERR` followed by a failed read-only reopen/integrity probe latches writes as `store_write_halted`. The writer publishes `system/core/halt.json` through the locked control-file store's file-sync, rename and directory-sync path. Its JSON fields are `schema: 1`, `epoch` (canonical decimal string) and `cause` (`sqlite_corrupt`, `sqlite_notadb` or `sqlite_io`); it contains no SQL or filesystem paths. Marker publication failure still latches the writer and is returned with the original error. Disk full and an I/O error whose read-only probe succeeds do not halt. Reads of intact data remain available after a halt. Startup with a valid marker skips writer initialization and migrations and opens only readers; malformed markers are refused. Startup, shutdown and epoch mirroring never clear a marker; only verified restore may do so.

These are package service APIs for the startup coordinator's canon hooks. This increment adds no TUI or client-protocol operation and does not attach a canon service to the CLI. Package tests cover the writer and failure injections; a subprocess exercises the older-binary exit boundary, and snapshots verify that both closed and live-WAL newer stores remain unchanged. Protocol scenarios under `e2e/m1/` become applicable when a composition exposes canon behavior through that surface.

## Packaging

### Binaries

LINA Core and the `lina` TUI ship as one static binary per platform, and Node ships as its own static binary per platform, each built with the pinned Go toolchain. A device that runs only Node or only the TUI installs just that binary and needs no separate language runtime. The release manifest records each binary's version and digest and the Go toolchain that built it.

Supported platforms are defined in [product-families.md](product-families.md). A platform and architecture enter release builds, and with them the supported-combination table of a release ([host-protocol.md](host-protocol.md)), only after the packaging and resident measurement passes on them. Until then they are built only as development builds.

### The `lina` command

`lina import-hermes` is an explicit optional-import entry point. It takes a source directory and confirmed profile/agent/person maps, prints rehearsal without a model call, and separately confirms import and promotion. Core must be stopped for canon writes. `--rehearse` writes nothing to canon; successful completion includes verified backup generations. Its arguments and boundaries are defined under [Importing a Hermes install](conversation-and-memory.md#importing-a-hermes-install).

The command recognizes `lina core`, `lina tui`, `lina install`, `lina update --prepare`, `lina version` and `lina-node version`. Both hosts also list the internal `proc-supervise --control-fd <fd> --status-fd <fd> --files <count>` helper used by `kit/proc` on Linux. Command descriptor numbers start at 3; control and startup status follow them. The helper is not a user shell or a new shipped binary. Missing or unknown subcommands print usage on stderr and exit 64.

LINA Core and the TUI are one executable, `lina`. Its subcommands cover these uses:

| Use | What it does |
| --- | --- |
| Core service | Runs LINA Core. The service manager starts LINA Core this way (see Services). |
| TUI | Opens the terminal surface, a separate process that connects to LINA Core over the `client` connection ([surfaces.md](surfaces.md)) |
| Install | Places LINA Core's artifacts, including the Claude adapter's files and lockfile, registers the LINA Core service, and `moirai-worker` when it is part of the install, and creates the state root ([filesystem.md](filesystem.md)). It signs in to no provider: the first run of a client offers the ChatGPT sign-in ([surfaces.md](surfaces.md#settings)). |
| Update preparation | `lina update --prepare` verifies a local release archive, stages a version and runs its Core preflight (see Updates). It returns preparation evidence without switching the active composition. |
| Backup and restore | Takes a backup generation and restores one ([filesystem.md](filesystem.md)) |
| Diagnosis | Reports the state and readiness of each component, with the cause of each failure |
| Replay | Re-runs a recorded turn sandboxed and without effects, optionally under candidate harness files, and writes the result as files. It is a development tool and runs only when it is called ([Replay command](#replay-command)). |
| Intake replay | `lina intake-replay <cases.jsonl>` scores binding baselines in fresh Core runtimes using an explicit private or synthetic case file ([Record and measurement](conversation-and-memory.md#what-a-message-is-about)). |
| Episode export | Writes recorded turns of the chosen conversations out of the ledger into the export staging area for use outside LINA ([conversation-and-memory.md](conversation-and-memory.md#episode-export)) |
| Version | Prints the version, the commit and the build date of the build ([versioning.md](versioning.md)) |

`lina install` supports a Linux main with a working systemd user manager. Its
default state root is `$XDG_STATE_HOME/lina`, falling back to
`~/.local/state/lina`; its default user unit directory is
`$XDG_CONFIG_HOME/systemd/user`, falling back to `~/.config/systemd/user`.
`--state-root`, `--unit-dir` and `--bundle` accept absolute paths. The bundle
defaults to the directory of the executing binary. Installation validates its
manifest, requires its target to match the host OS and architecture, and verifies
the size and SHA-256 of the exact artifact bytes copied.
The install includes the eight working personas from public
`persona/<preset>/core-v1.md` and `default-voice-v1.md`, using the keys and names in
[Presets](conversation-and-memory.md#presets). Release validation checks all eight
pairs before building release artifacts, independently enforcing the 64 KiB JSON-escaped pair budget. The files are embedded in the Core binary and covered by its artifact hash; installation needs no separate persona copy. Core loads them without a private checkout or persona-source setup;
the legacy `--persona-source` / `persona_source` path does not override shipped
starter layers. Migration publishes normal preset revisions while preserving
prepared turns and agent-owned layers. Only backstory and world material stay
in the private world repository and enter through material recall; their absence
does not clear a working persona or block installation and first conversation.
See [Presets](conversation-and-memory.md#presets) for validation, per-preset
unavailable-pair fallback and revision behavior.
The executable and its manifest are placed in `system/bin/`. The default client
socket is `system/run/client.sock` under the state root; `--socket` can choose a
shorter absolute path where the Unix socket path limit requires it.

Every state directory is private (0700); identity, inventory, configuration and
adapter files are 0600 and installed executables are 0700. Install creates the
filesystem contract's empty directories without opening canon or writing an
account credential. It supplies the durable random main identity. The unit
`lina-core.service` starts the installed executable as the current OS user with
explicit root and socket arguments. A bundled `moirai-worker` receives its own
`lina-moirai.service`, ordered after and dependent on Core, with
`--canon <state-root>/canon/canon.sqlite` and
`--socket <state-root>/run/jobs.sock`. No provider adapter receives
a service unit.

Core opens canon and serves the job API at that socket. Install enables the
included worker after Core. The worker's service retries initial unavailability
because Core may still be opening canon when systemd starts the worker. A
missing model route makes model-based jobs unavailable without stopping Core
or deterministic growth jobs. A copied executable or a successful systemd start
job is not evidence of readiness; the installed skill maintenance scenario
exercises the socket with the actual worker and verifies committed results.

Preflight refuses an existing state root, socket, named service, service
override or enablement link, and refuses linked destination ancestors. An
in-state client socket must be below `system/run/`, and socket paths overlapping
install destinations or the worker socket are refused before any writes. An
explicit install-layer TCP address check holds a probe listener through file
publication and refuses occupied ports; the current Core service uses Unix IPC
and does not expose a TCP listener. Refusal precedes state creation and service
registration. New files are created exclusively. Failure before startup rolls
back only entries created by this attempt. Once service startup has been
attempted, an error retains the install for diagnosis because a service may
already be running. Install exits 78 on failure and never treats an existing
install as authorization to replace its identity, repair its files or restart
its services.

The inventory in `system/tools.json` records the resolved executable path,
bounded `--version` output, observed status and the bundle's verified version
list. `--tool name=/absolute/path` overrides PATH discovery for a named tool.
Probes are bounded, missing or unstartable tools do not block installation, and
unverified versions are listed together with the verified versions and remain
usable. The list comes from `verifiedWorkerVersions`; for Codex, install compares
versions after removing the optional `codex-cli ` prefix on both sides, while
preserving the original output and list in the inventory. An older or otherwise
unlisted version stays unverified. A missing verified list is empty, never an
implicit verification.
`system/install.json` records the identity, socket, tools and missing optional
components. The bundle's optional `verifiedWorkerVersions` and `installFiles` fields are
described in [packaging](../../packaging/README.md). The legacy `verifiedTools`
field remains a fallback only when `verifiedWorkerVersions` is absent or null;
an empty canonical map does not inherit legacy verification. Adapter files and their
lockfile are copied only after digest verification; no SDK or user tool is
downloaded during install. Until the build includes Claude adapter artifacts,
that provider is reported unavailable. Missing background-worker artifacts
affect only background memory and planning. Installation never signs in to a
provider; the first preparation still has to pass the account check and offer
the ChatGPT sign-in when no account is usable.

The installation acceptance scenario observes the installed Core identity and
TUI connection through the existing client protocol. Account-free preparation
is exercised through the actual turn preparation flow with an empty credential
record: it fails at the account step, reports one line with the sign-in offer,
and sends no provider request. The current conversation/account protocol
operations remain reserved; installation does not introduce a second
preparation or sign-in implementation.

### Provider adapters

The ChatGPT, Gemini and Grok adapters are Go inside LINA Core. The Claude adapter is the only provider adapter that is a separate program.

- The Claude adapter is LINA's own TypeScript, an external component ([Language and build](#language-and-build)) that runs on Node.js. LINA Core starts it on first use of a Claude account, as a stdio child, and stops it after an idle period. It is never started at boot and never kept warm, because the vendor runtime it wraps is large: a binary of about 270 to 290 MB per platform, and a parked process of about 230 to 260 MB. Each concurrent turn on a Claude account holds its own runtime process, so the number of concurrent turns per account is limited (see Deferred).
- LINA does not ship a Node.js runtime. The adapter runs on the Node.js that the person has installed, found on `PATH` or at a path the person configured, as an installed tool ([Installed tools](#installed-tools)). Without a usable Node.js, only Claude accounts are unsupported, and the account says why.
- The release carries the adapter's lockfile, with the exact version and integrity value of the Claude Agent SDK and of its platform packages. On first use of a Claude account, LINA Core installs them into the adapter's own directory from the public npm registry with `npm ci --ignore-scripts`, and verifies the lockfile against the digest in the release manifest. LINA does not redistribute the SDK package in its installers or artifacts, because it is under the vendor's own license terms. Which SDK features are used is decided by the capabilities that the runtime's start-up message reports, not by comparing version numbers.
- An update of the adapter, its lockfile and the SDK version is part of a LINA update ([Updates](#updates)).

### Services

- LINA Core is a systemd service running as the user's own OS user, so it sees the user's tools and settings the way the user's own Codex does.
- `moirai-worker` is an optional systemd user service of the same OS user.
- Node registers with the device's service manager: systemd on Linux, launchd on macOS and a Windows service on Windows.
- An install never restarts an existing service without authorization. New settings, databases, services, ports and paths never overwrite existing ones.

### Install compositions

Supported platforms are defined in [product-families.md](product-families.md). An install is one of these runtime compositions:

| Composition | Contains |
| --- | --- |
| Conversation only (Linux) | LINA Core with the Claude adapter's files, the TUI, optionally `moirai-worker`, and bubblewrap |
| Linux main | Conversation only, plus the worker adapters, which drive a coding tool the user installed when work is delegated; optionally Node and LINA APP on the same machine |
| Additional device Node | Node. Coding work there uses the coding tool installed on that device, and is supported only after the Node's relay of the worker is verified. |
| LINA APP only | LINA APP |
| Development host | Not a product composition |

When a piece is missing:

- Missing conversation pieces make conversation fail its readiness checks. There is no `PATH` fallback.
- A worker that is missing, can't be connected to or fails its handshake makes only delegation unsupported. An unverified worker version is reported and never stops delegation.
- A missing `moirai-worker` makes only background memory and planning unsupported.
- A running service is never evidence that a turn succeeded.
- A provider whose pieces are missing (no usable Node.js or SDK package for Claude, no `grok` command for Grok, no signed-in account) makes only that provider unsupported, and its account shows why. Conversation needs one usable account of any provider.

### Release manifest

Every build from a clean commit, a development build included, writes a manifest next to its executables. Every build's manifest holds the same fields as the release manifest, listed below, and records its commit. The release manifest is the name of a release build's manifest.

`node scripts/build.mjs --out <dir> [--target <os/arch>] [--version <release-version>]` builds `lina` and `lina-node` for the selected target (the host by default, `.exe` on Windows) and writes `<dir>/manifest.json`. The build matrix accepts Linux, macOS (`darwin`) and Windows on amd64 or arm64; platform acceptance remains separate from cross-compilation. An explicit release version lets the release build label the exact candidate before its tag is published. It uses the workspace's exact Go toolchain, `CGO_ENABLED=0`, `-trimpath`, readonly module inputs, and the `internal/buildinfo.Version`, `.Commit` and `.Date` linker flags used by `lina version`. The build date is the current UTC date. On a clean commit with a local release tag, the version is the highest valid release tag on that commit, without `v`; otherwise it is `0.0.1-alpha.dev`. Building does not create or publish a release. The release orchestrator runs binary assembly on Linux under bubblewrap, using a separate read-only source clone, private PID/network namespaces and bounded regular-file outputs; it cross-builds both Linux amd64 and macOS arm64 without exposing the publisher's Git directory or home.

The initial JSON object contains these required fields; later implementation issues add the remaining manifest values listed below:

| Field | Format |
| --- | --- |
| `version` | Product version without `v`, either `0.0.1-alpha.dev` or `0.0.1-alpha.YYYYMMDD.N`. |
| `commit` | Full lowercase Git commit hash. The version commands display its first seven characters. |
| `date` | UTC build date, `YYYY-MM-DD`. |
| `target` | Build OS and architecture, for example `linux/amd64` or `darwin/arm64`. |
| `goToolchain` | Exact toolchain used, such as `go1.27.3`. |
| `kitVersion` | Positive integer read from `kit/VERSION`, initially `1`. |
| `sqlite` | Object with binding `module`, exact `version`, Go `moduleSum` (`h1:`), `sqliteVersion`, `wasmSHA256` (64 lowercase hex characters), `execution: "wazero"`, and `sqliteVec` (exact `version` and `sha256` of the complete custom Wasm engine; older manifests may have null). |
| `providers.chatgpt` | `adapter: "responses"`, `retryCount: 0`, and `requestFields`, the explicit array of allowed top-level request fields. |
| `verifiedWorkerVersions` | Optional object mapping worker names to verified version arrays; `codex` uses bare versions from the adapter's `versions.json`, such as `["0.163.0"]`. |
| `binaries` | Array of objects with `name` (basename), `size` (positive byte count), and `sha256` (64 lowercase hexadecimal characters). Includes both `lina` and `lina-node`, with platform suffixes. |

The writer copies `providers` from `kit/provider/responses/manifest.json` and checks its adapter identity and zero retry count. The Responses adapter reads that configuration from the generated manifest, validates required and forbidden fields, and permits per-request narrowing only. The initial list is exercised by scripted wires; live V1 acceptance remains required before conversation acceptance.

The writer removes an old manifest before building and writes a new one atomically after both binaries succeed. It obtains SQLite metadata by running `internal/core/manifest/sqliteinfo` on the build host with the same toolchain, `CGO_ENABLED=0` and readonly module inputs: Go build information supplies the binding version and checksum, while in-memory SQLite and sqlite-vec queries and a hash of the embedded bytes identify the engine. The Wasm bytes are platform-independent; the metadata probe is never executed as a foreign-target binary. Uncommitted tracked, staged or nonignored untracked changes still permit a development build but suppress the manifest. A source change or a different `HEAD` detected during the build or metadata probe also suppresses it. The three generated output files do not count as untracked source changes; tracked files cannot be used as output artifacts.

`internal/core/manifest.Read(path)` decodes the JSON and validates the required fields, valid calendar dates, full commit hash, exact toolchain, positive safe integers, unique safe binary names and SHA-256 syntax. It requires the selected SQLite binding, exact versions, a valid Go module digest and the engine digest and execution mode; a sqlite-vec descriptor must identify the same complete engine digest. Both binaries must be present with matching platform suffixes consistent with `target`. Additional fields are accepted so later issues can extend the manifest. The reader validates the records; comparison with executable bytes belongs to the consuming component. Manifests from before the target, SQLite or provider fields were introduced must be regenerated by a clean build. The reader validates the Responses adapter identity, zero retry count, and request-field list as well. A clean rebuild replaces the earlier `sdkModule`/`sdkVersion` descriptor with `adapter: "responses"`.

Foundation runs the build script's behavioral tests and the manifest package's Go checks. A clean-build test checks sizes and SHA-256 against both produced binaries, matches their version output to the manifest, resolves the displayed commit abbreviation to the full manifest commit, and reads the generated JSON through the Go reader. Dirty-build tests verify that binaries are still built and that no stale manifest remains.

- A build's version is `0.0.1-alpha.YYYYMMDD.N` for a release build, which is the number of its tag `v0.0.1-alpha.YYYYMMDD.N`, and `0.0.1-alpha.dev` with the commit for a build that is not a release ([versioning.md](versioning.md)). The commit is recorded in the build, never in the tag.
- The supported-combination table of a build lists the peers, platforms, protocol versions and capabilities that the build supports, so the TUI and LINA Core of one build find their row. Compatibility is decided by the protocol version, never by the product version ([host-protocol.md](host-protocol.md), [versioning.md](versioning.md)).
- A running component reads the values this design places in the release manifest, such as the request field list and the verified worker versions, from the manifest of its own build.
- Turn preparation compares the running LINA Core executable with the manifest of its build ([conversation-and-memory.md](conversation-and-memory.md)).

The release manifest records:

- external components, each with version, digest, execution mode, source and license
- versions and digests of LINA Core, Node, LINA APP and `moirai-worker`, and the Go toolchain that built them
- the app shell: the Wails version, the web engine version observed in acceptance, the mobile host version (and the Electron version where it is used), and the digest of the front-end package lockfile
- the plan catalog: its version and digest ([Connected accounts and automatic setup](#connected-accounts-and-automatic-setup))
- the idle memory of the packaged LINA app on Linux and macOS, which the foundation gate compares against on every build
- the SQLite binding and sqlite-vec, with versions and digests
- the LINA kit version
- the model-API adapter identities, provider SDK versions where used (the Gemini client and the Claude Agent SDK package the adapter installs), and retry values
- the request field list of each model-API provider, and the provider capabilities admitted for each route ([Provider capabilities](#provider-capabilities))
- the built-in conversation tool declarations and the sandbox policy
- the verified versions of the installed tools LINA uses (see Installed tools)
- the plugin catalog: its version, digest and signature, and the key that verifies it
- the process environment rules for sandboxed commands and plugin processes
- the service configuration
- the state schema version
- the supported-combination table ([host-protocol.md](host-protocol.md))
- the Claude adapter: its version, its digest and the digest of its lockfile; and the sign-in client settings of the ChatGPT adapter (issuer, scopes, resource and app name hint)

The current `supportedCombinations` section has one client 1.0 row for peer `lina-tui` on the selected build target's `GOOS/GOARCH`, with `lifecycle` version `1.0` and evidence from protocol conformance and `TestPlannedRestart`. The listener and build writer obtain this row from the same compiled manifest definition. Product version is absent from the compatibility key.

The manifest pins only LINA's own artifacts and dependencies: its components, the app shell (with the engine it bundles, where that is Electron), and the external components LINA ships. A change to a pinned value (an SDK, the Claude adapter's lockfile, a request field list, a sign-in scope, environment rules, retry values) lands in the same change as its manifest change.

### Packaging and resident budget

LINA Core, Node and the desktop LINA app each have a packaging and resident budget. The budget covers artifact size, start time, and resident memory and CPU over 24 hours, measured with OS instrumentation. It is declared before the measurement runs and recorded with the result.

## Supply chain

Every external dependency, vendored source and bundled binary follows [dependencies.md](../policy/dependencies.md). Its gates run in the local foundation verification from the first commit ([foundation.md](../policy/foundation.md)).

## Updates

Replacing the host OS and migrating or rolling back state schemas are separate operations.

Core's restart request is durable in `system/core/lifecycle.json`, outside every backup generation. `when_idle` continues admitting work while waiting; explicit `drain` closes admission and waits for existing effects, streams and read-only calls. Both use the admission fence in [C-05–C-07](go-core.md#start-recovery-idle-and-update), and Unknown activity blocks both. No timeout forces a restart. A successful fence durably records commitment before orderly shutdown and planned exit 75. The request remains owed across a crash until a later epoch reaches READY and records discharge; a crash before commitment leaves the request pending for another safe attempt. Restore leaves the control record alone. Changes to settings, persona and bindings do not request restart or advance the epoch.

A client negotiates `lifecycle` version `1.0` and sends `restart_request` with optional `mode`; omission selects `when_idle`. `restart_get` reads the durable receipt, including during recovery. The reply is `restart_status` with `owed` and, when a request exists, its id, mode and requesting epoch; committed and discharged epochs appear only after their durable transitions. Requests require the connection's sender and the current epoch, and mutations are unavailable during recovery. A pending request with another mode is refused as duplicate. A response is written before the request can cause shutdown; failure to write does not undo the durable request. The existing service manager starts the replacement Core after exit 75; Core does not spawn a replacement itself.

An update runs in this order:

1. Drain: settle in-flight effects and record any whose outcome is unknown.
2. Snapshot the ledger, the schemas and the manifest.
3. Prepare the new composition: new binaries and settings, with the previous ones kept for rollback.
4. Smoke: one conversation request with its readiness checks, and the worker readiness check when a worker is installed.
5. Resume.

The preparation command is `lina update --prepare --state-root <path> --archive
<tar.gz> --sha256 <digest> [--required-manifest <path>]`. The expected digest
comes from release metadata supplied by the caller. The command freezes and
verifies the archive before extraction, accepts only bounded regular files and
directories, and refuses traversal, links, duplicate entries and truncated
archives. It verifies the build target and every binary/adapter artifact's size
and digest, including a pinned Claude package and lockfile when present.
The limits are 512 MiB compressed, 1 GiB expanded, 256 MiB per file and 4096
entries. Staging requires space for the compressed and expanded limits before
it starts. No provider credential or inherited configuration reaches the child.

Versions are private directories at `system/updates/<archive-sha256>`. Existing
directories are refused and preserved. A failed attempt removes only its own
new directory and temporary files; interruption may leave an incomplete directory
which is never reused or treated as prepared. Only a successful candidate
preflight publishes and fsyncs `prepared.json`. The command returns its directory,
version, archive digest, schema/peer result and all six gate cells as JSON.
Success exits 0, invalid arguments 64, and preparation refusal 78.

The candidate runs `core --preflight` in a separate owned process with a
one-minute deadline, without the Core lock or installed-state writes. It can run
beside a live Core; a changing database or WAL causes refusal and requires a
fresh attempt. The schema check allows equal and forward-migratable versions,
refuses newer or incompatible development schemas, and never migrates. Peer
checks use exact protocol, platform and required capabilities from the installed
manifest (or compiled local TUI requirements), bounded by the candidate's own
implemented rows. `--required-manifest` explicitly selects another requirements
table. No product-version comparison substitutes for these checks.

Preparation leaves idle and recent verified generation unknown, so its gate is
closed even on successful staging. Activation must supply those observations
through the drain and backup boundaries and revalidate the preparation evidence;
the preparation command does not perform a swap or restart. Both this preflight
and normal Core startup refuse a newer store with exit 78 without changing the
database, sidecars, lock, epoch or sentinel. Headless scenarios are in
`e2e/m2/update-preflight.json` and exercise actual candidate Core processes.

The installed activation command is `lina update --activate --state-root <path>
--candidate <prepared-directory>`. It persists an update attempt outside backup
generations, requests the running Core's explicit drain and waits for its
committed fence and exit before taking a verified snapshot. Candidate preflight
is repeated against the stopped store. The binary, manifest and pinned adapter
composition is kept for rollback; no user-installed worker or tool is changed.

Activation backup and rollback restore run with the updater caller's cancellation
and deadline, without the installer's 30-second command timeout. Their duration
depends on the installed data size. The same rule applies to restore during
`update --resume`; owned child cleanup, environment filtering and output bounds
remain in force. Service-manager commands retain their short timeout.

The candidate runs first on the private `run/update.sock`. Core writes a durable
READY receipt before admitting the smoke conversation. Smoke requires a real
completed conversation result and, when installed, the bundled Moirai worker's
read-only `--preflight` against canon and the live job socket. It does not dispatch
background jobs. The candidate then drains; the updater records completion and
starts and checks the ordinary service. Missing account/model readiness is a
smoke failure. Core refuses normal startup during an unfinished update, before
opening canon or minting an epoch. `lina update --resume --state-root <path>`
recovers an interrupted attempt using its recorded paths, composition and
generation, or retains a hold with a closed cause. Changed installation/data
paths and foreign services are refused before controlling another service.
If the resumed service fails to start or pass readiness, recovery records
`held` with `update_resume_failed`, including when the interrupted attempt had
already recorded `complete` or `rolled_back`. A failed readiness check stops the
service with a bounded cleanup context even if the updater was cancelled.
The READY receipt stays sticky, and normal Core startup remains blocked.

An explicitly configured Claude OAuth token also reaches the trusted smoke Core
and its model adapter, so smoke does not require a cached vendor login. Core
consumes that token from its environment after configuring the adapter and before
starting workers. This Core-only launch exception does not widen tool or worker
environments or forward other filtered provider variables. The token stays out
of logs, receipts and diagnostics.

Rollback restores the previous compatible composition from the kept binaries and the pre-update snapshot. Automatic rollback is allowed only while the candidate has never reached READY. A possibly started forward schema migration without an undo proof holds instead of rolling back. Binaries are never rolled back over an irreversible migration. When an update fails, the install restores the previous compatible composition, or holds: it stops, keeps the current state intact and reports. It never leaves a half-updated component running against data it can't read.

- The main gets release artifacts from the private GitHub Releases of the LINA repository with the owner's `gh` authentication, and distributes Node binaries and their updates to devices over the tailnet ([releases.md](../policy/releases.md)).
- The Claude adapter, its lockfile and the SDK package it installs are updated only as part of a LINA update, by their pin. An update never starts, stops or updates a tool the user installed, such as a worker, the `grok` command or Node.js.
- A hash of a dirty work tree is never a release.
- An OS snapshot or an OS update is never a restore or a ready receipt for LINA Core's composition. Snapshot settings, a CLI version or an active service never prove an install composition, a restore or model readiness.

## Recovery

LINA Core dispatches nothing before its new epoch is durable ([main-authority.md](main-authority.md)). On restart, LINA Core stops pending approvals, tool calls and background effects and fences the previous process generation: a late response from that generation never changes current state. An effect that couldn't be stopped is recorded as unknown and is never re-run automatically. Worker turns cut by the restart follow [work-and-delegation.md](work-and-delegation.md). The pollers and Gateway sessions of external channels are part of that fence: the previous generation's sessions are closed, the new epoch opens its own, and a button on an approval request from an earlier epoch is answered as stale and applies nothing ([main-authority.md](main-authority.md#channels)). A background run that was working follows the durability rule of [work-and-delegation.md](work-and-delegation.md#background-runs): it is resumed once from its last checkpoint under the new epoch, and a late result from the previous generation is fenced. Completion events are rows in LINA Core's database. Once the new epoch is durable, LINA Core runs one reconcile pass over every task marked in progress or watched before it hands off anything new.

Restore, its order and its compatibility rules are defined with backup generations in [filesystem.md](filesystem.md).

## Failure injections

Before a composition is accepted, each of these failures is injected and the outcome must match the rule it tests.

These install and update failures must end in the previous compatible composition, or in a hold that keeps state intact and reports:

- a truncated download
- a start failure after install
- a full disk during install or update
- a path change of the installation or data location
- a backup that doesn't form a compatible pair with the installed version
- a process killed between update steps

The activation failure matrix is exercised by `e2e/m2/update-activation.json`
through the installed executable, client protocol and actual bundled worker.
It includes pre-READY start refusal, post-READY provider failure and updater death
after dispatch. Package injections cover truncated gzip/digest failures, ENOSPC,
service/snapshot/swap failures, incompatible generation refusal, changed paths,
and SIGKILL acknowledged at each binary/adapter rename. Failure evidence uses
closed causes and durable receipts; it does not infer success from a running
service or from elapsed time. Existing runtime-specific injections below remain
with their owning components and are included in full foundation verification.
`e2e/m2/update-resume-failure.json` kills the updater after smoke completes but
before the ordinary service starts, then refuses the recovery start. It checks
the durable hold, blocked Core startup and unchanged data and composition.

These runtime failures must end as their rules require:

| Injection | Required outcome |
| --- | --- |
| Digest mismatch of sqlite-vec, or of the lockfile or a package of the Claude adapter | The component doesn't start and reports the mismatch |
| Sandbox unavailable | Conversation continues; shell and file tools are unsupported, local plugin servers added by command do not start, and the reason is shown |
| Provider unavailable (a failed connection, 503 or 529) | No request is sent after the failure; the surface shows the typed cause; nothing moves to another provider |
| Account token expired, revoked or reused | The account shows needs sign-in, no request is sent with it, no other account is used in its place, and conversation on other accounts is unaffected |
| Usage cap reached | The account shows usage cap reached with no invented reset time; new requests go to another connected account only by the quota-aware routing, never to a pay-per-use path the person did not allow; otherwise the person is offered the switch |
| Claude adapter killed with a turn in flight | The turn's outcome is unknown and is not replayed; late output from the old process is fenced |
| LINA Core killed with a tool call in flight | The effect is unknown and never re-run; a late result from the old generation is fenced |
| LINA Core killed after receiving a channel update and before answering it | The update is handled once, by its stored cursor and idempotency key; a reply whose delivery is unknown is not sent again |
| LINA Core killed with a background run working | The run is resumed once from its checkpoint, the previous generation's late result is fenced, and a second kill lands the run as failed |

## Remote access

Remote access uses [Tailscale](https://tailscale.com/kb) only. LINA APP, the TUI and Node connect to LINA Core directly inside the tailnet. A mobile app host may connect with a WebSocket on that same tailnet address ([host-protocol.md](host-protocol.md#client-connection)). Tailscale owns device identity and access control. LINA runs no account service and no login that a client or a device presents to LINA Core, and registers a device on its first connection. The sign-in that this product calls LINA login is the owner's ChatGPT sign-in ([LINA login](#lina-login)): it is made once, on the main, binds the owner's ChatGPT identity to the owner's person id and gives the conversation its default model account. It is not checked when a client or a device connects, and it opens LINA Core to nobody outside the tailnet. A LINA admits only its owner, so every client connection acts for the owner ([host-protocol.md](host-protocol.md)). LINA Core accepts remote connections only on its tailnet address; there is no relay and no public listener. Channel connections are outbound as well: LINA Core opens its own connection to each platform, and no platform calls in. There is no webhook endpoint and no listener for channels ([integrations.md](integrations.md#channels)). A relay, and the login of LINA's own that a relay would need, are on the backlog ([cognition-and-life.md](cognition-and-life.md)).

Remote TCP is explicitly enabled with `lina core --state-root <path> --socket <path> --tailnet-listen <IP:port>`. Core asks the local Tailscale daemon for the current assigned addresses and owner before binding only that exact address, and authenticates every remote peer with its source IP and port. The daemon must be running; a missing daemon, non-owned address or bind failure fails startup and closes acquired listeners. There is no fallback address. The trusted LocalAPI Unix socket defaults to `/var/run/tailscale/tailscaled.sock`; installations with a different socket may specify `--tailscale-socket <absolute-path>` together with the listener option. The owner uses another untagged device under the same Tailscale user; same-device clients retain the Unix socket and its ancestry boundary.

Both client listeners run during RECOVERING and share the READY transition, installed handlers and graceful shutdown. Signal cancellation, startup failure or a listener failure joins both services and closes accepted clients, including incomplete handshakes. The `running` capability's typed stop cancels an owned background run without approval and preserves its collected work. WebSocket framing is a separate increment (#203). The automated scenario is [e2e/m3/tailnet-listener.json](../../e2e/m3/tailnet-listener.json); its synthetic LocalAPI and network seam verify Core composition without claiming live two-device tailnet connectivity.

## LINA OS composition

LINA OS is a separate, optional project in [thisisjun786/lina-os](https://github.com/thisisjun786/lina-os).
Its composition, profiles and desktop environment rules live in [lina-os `docs/design.md`](https://github.com/thisisjun786/lina-os/blob/main/docs/design.md).

## Runtime acceptance

The first implementation issue demonstrates the runtime with these checks. Each check's baseline comes from outside LINA: the request surface that the issuer documents for Sign in with ChatGPT, the installed Codex, the installed `grok` command, the types the Claude Agent SDK publishes, the Go module checksum and vulnerability databases, and OS instrumentation.

| Check | What runs | Passes when |
| --- | --- | --- |
| V1 ChatGPT sign-in and Responses round trip | The Sign in with ChatGPT flow end to end through the sign-in relay, on a local client and on a remote one (dynamic registration, loopback redirect, scope check, ID token check, refresh with rotation). Then the Go Responses HTTP/SSE adapter for three or more turns with `store=false` and `stream=true`: the encrypted reasoning round trip, a function tool declared the way the request surface requires (including `apply_patch` in its function form), parallel tool calls, and the fields `include`, `parallel_tool_calls`, `service_tier` and `prompt_cache_key`, which the surface does not document | Every turn round-trips without rejection, the fields sent are within the request field list, and the result records which undocumented fields the surface accepts. If the encrypted reasoning round trip is rejected, or the relay's redirect is not accepted, the check fails and the ChatGPT path is reopened before conversation work builds on it |
| V2 packaging, storage and residency | Static binaries per platform; SQLite with sqlite-vec from Go (WAL, FTS5 and vector queries); LINA Core's executable, built from the same commit, for 24 hours as a systemd service on Omarchy x64 and as a launchd job on macOS arm64. The macOS run stands in for Node's budget, because Node uses the same runtime parts. | sqlite-vec loads, the services start, and size, start time and resident memory and CPU stay within the budget declared before the run on both platforms; the chosen SQLite binding is recorded in the release manifest |
| V3 own conversation loop | LINA's Go loop with the ChatGPT adapter: streaming, tool calls with their before and after hooks, steering input, cancellation and parallel tool calls | The loop's tests pass |
| V4 provider adapters | A Claude turn and a Grok turn, each with the runtime's built-in tools off, LINA's tools offered over MCP, an approval routed to LINA Core and answered, session persistence off, and usage and session id recorded; for Claude, the start-up message check, the quota source the token supports, and the cache mark kept on a prompt block; for Grok, the approval and tool-restriction behavior over the Agent Client Protocol; and a Gemini turn with its thought parts returned as received | Each adapter completes its turn with every tool call run by LINA Core under its checks, a runtime that loaded anything LINA did not put there is refused, and the result records what each quota source reported |
| V5 worker adapter round trip | Go types generated from the JSON Schema that the installed Codex writes with `generate-json-schema --experimental`, then `initialize`, `thread/start` with `dynamicTools`, `item/tool/call` and turn completion | The round trip completes with the installed Codex, and the adapter records its version |
| V6 supply-chain baseline | The Go and npm gates of [dependencies.md](../policy/dependencies.md), run by the local foundation verification ([foundation.md](../policy/foundation.md)) on the minimal dependency set, with `go.sum`, the module count, the Claude adapter's lockfile and its transitive package count recorded | The gates pass, `go.sum` verifies against the checksum database, `govulncheck` passes, and no npm package needs a lifecycle script |
| V7 development loop | A worker's edit to Go code, then `go build`, `go vet` and `go test` for the affected packages; and a full local `foundation` run (`node scripts/foundation.mjs --full`) | The time from the edit to the result of those commands, and the full `foundation` time, stay within the budget declared before the run |

Foundation keeps one local command and one required CI result. PRs select docs, automation, dependencies, build/analysis and unit checks as applicable, without e2e by default. The `e2e` label opts a PR into its reached acceptance scenarios; label changes reselect the plan. Its shared candidate plan separates build and analysis, measured unit-package shards, and acceptance scenario-file shards. Scenario imports, helper inputs and the binaries they launch determine which acceptance cases a change reaches; shared or unmapped inputs select all. Deleted or renamed runtime fixtures retain acceptance coverage: an input without a known consumer that maps to an acceptance package selects every scenario in that package. Pushes to `dev` and nightly verification run every case. Post-merge failures open or update one `qa` issue per package and top-level test in the current milestone (`QA_MILESTONE`), carrying the tested head, first failing commit range and failure lines. A package timeout or panic marks an unfinished scenario as failed only when a test or descendant is still running, or a subtest explicitly failed, and retains bounded diagnostics. A parallel test paused without a later continue event, including a parent waiting only on paused children, remains unknown; tests that never started are not failures. Unknown scenarios neither open QA issues nor close existing findings, and do not change the failing package, shard or foundation result. QA reporters are serialized with up to 100 pending jobs instead of replacing a pending reporter. Before opening the first finding, the reporter consults retained results from completed later runs of the same branch. A latest terminal pass on a descendant head prevents an older failure from opening a recovered finding; missing or expired artifacts do not prove recovery. A later head with an explicit passing test result closes an existing finding. Missing, skipped and stale observations cannot establish recovery, and these findings do not block PRs. A failed-job rerun uses the newest attempt for each shard while retaining results for jobs not rerun; older attempts cannot overwrite the selected result. Missing or mismatched shard results fail the final gate. The [foundation policy](../policy/foundation.md#execution) defines the execution and evidence contract. V7 measurements identify the exact workload, environment, repeated command and spread before and after a change; successful and failed runs remain distinguishable.

## Deferred

- The packaging and resident budget for LINA Core, Node and the desktop LINA app: declared in the first implementation issue before the V2 measurement runs (for the app, before its first packaged measurement), and recorded with its result. A result outside the budget reopens the language choice, and for the desktop app it reopens the shell choice.
- The development-loop budget: declared before the V7 measurement and recorded with its result. A result outside the budget reopens the language choice.
- The SQLite binding is selected by #39 and recorded in the build manifest. #94 packages the pinned sqlite-vec engine with mandatory digest verification, Linux amd64 and macOS arm64 build targets, and binary archives in the release build stage. Vector queries, FTS5, WAL and online backup run against the packaged engine. Service startup is exercised through the installer and Core protocol. The full V2 24-hour residency, native macOS execution and performance acceptance remain separate from these packaging checks; producing an archive alone does not qualify a supported-combination row.
- The Windows sandbox mechanism of the kit's sandbox wrapper: chosen and verified by the Windows Node implementation, which cannot be accepted without it.
- Exact pins (the Go toolchain, the Gemini client, the MCP Go SDK, Bubble Tea, the SQLite binding, sqlite-vec, the JSON Schema to Go type generator, the app shells (Wails, the mobile host, and Electron where it is used), the Claude Agent SDK package): set by the first implementation issue and recorded in the release manifest.
- The verified versions of each installed tool: set by the first implementation issue (V5 for Codex) and recorded in the release manifest.
- Whether LINA Core as a service sees the user's login environment (`PATH`, the SSH agent and the keychain), or reads the login shell's environment once at start instead: measured by the first implementation issue.
- Per-tool deadlines other than the 15 second default of the turn budget: set during implementation acceptance of the conversation engine.
- Absolute install locations, the absolute state root, service names and socket paths per OS: set during packaging implementation acceptance.
- The subcommand names and flags of `lina`: set by the first implementation issue.
- Additional build-manifest fields beyond the implemented provenance and binary records in [Release manifest](#release-manifest): added by the implementation issues that introduce the corresponding components and behavior.
- Publishing binaries and their manifests: added to [releases.md](../policy/releases.md) when LINA is first packaged.
- The default embedding model: chosen by the implementation issue that brings memory recall into answers.
- Whether LINA qualifies as a Sign in with ChatGPT client, including a LINA Core on a remote host the person owns: confirmed with the issuer before the release that depends on it.
- Whether the Claude subscription route through the Agent SDK is allowed for LINA: the SDK's overview says third-party products may not offer claude.ai login without approval ([research](../research/provider-optimization.md#result)). Confirmed with the vendor before the release that depends on it. A Claude API-key route over the Messages API is planned as its own implementation.
- The keep-alive interval of an idle ChatGPT refresh token, the warning period before a Claude token's year ends, the idle period after which the Claude adapter stops, and the number of concurrent turns per Claude account: set during implementation acceptance of the provider adapters.
- Which quota source a Claude token supports (the runtime's events, the usage report, or both): decided by a live test in V4.
- Where a model's context window comes from when its adapter reports none: set by the implementation issue that ships the provider adapters.
- Narrowing a plugin server's network to named hosts: needs a filter in the sandbox wrapper; chosen at implementation acceptance of the plugin system, and until then the allowance is network on or off.
- The starting values of the turn budget (acknowledgement, soft and hard budget, tool deadline), which only set the hand-off point: tuned by measurement during implementation acceptance of the conversation engine, on waiting time and completion quality together (rework, corrections and "that is not what I asked"), never on waiting time alone.
- Replay uses immutable history references, pinned persona and recorded request configuration; missing reconstructible assembly excludes the turn. Raw ContextPackets remain unstored.

### Built-in web reads

[Web search and page reading](web-tools.md) implement `web_search(query, limit)` and `web_extract(url)` in ordinary turns. The same tool registry and network approval path govern both. Only configured providers are offered, returned content is untrusted evidence, and the provider key belongs to the existing secret store.
