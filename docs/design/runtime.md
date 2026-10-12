# Runtime

This contract fixes what LINA's components are built from and how they run: the language and the build, the vendored source, the LINA kit, the conversation loop and its model path, the non-chat adapters, the tools and their sandbox, the tools LINA uses without owning them, storage, packaging, updates, recovery, remote access and the composition of LINA OS. It is normative: implementations must follow it, and any change to it is made in this file and joins `dev` under [integration.md](../policy/integration.md). Finishing this document does not mean any component is built or accepted; runtime proof belongs to the implementation issues that consume it.

## Scope

In this document, Node is LINA's device execution component and Node.js is the JavaScript runtime. The two names are never interchangeable.

This contract covers:

- the language, the build and the processes of every component LINA builds
- the vendored source and the LINA kit
- the conversation loop, the model path through opencodex and the non-chat adapters
- the conversation tools, plugin tools and the OS sandbox
- installed tools that LINA uses without owning them, including the coding tools LINA runs as workers
- storage engines, packaging, the `lina` command, the release manifest and the packaging and resident budget
- updates, recovery, failure injections, remote access and LINA OS composition

It does not cover the envelope and wire formats ([host-protocol.md](host-protocol.md)), grants, epochs, receipts and the approval policy ([main-authority.md](main-authority.md)), turn preparation, request assembly, persona, memory and compaction ([conversation-and-memory.md](conversation-and-memory.md)), the work engine, the kind-of-work rule, worker adapters and the LINA work harness rules ([work-and-delegation.md](work-and-delegation.md)), plugins ([integrations.md](integrations.md)), installation modes and LINA OS profiles ([product-families.md](product-families.md)), the state root, backup generations and restore ([filesystem.md](filesystem.md)), screens ([surfaces.md](surfaces.md)) or dependency and vendoring rules ([dependencies.md](../policy/dependencies.md)).

## Language and build

LINA Core, `moirai-worker`, the `lina` TUI, the worker adapters, Node, the LINA work harness (`lina-work`) and the LIFE world engine are Go. They are built as static binaries per platform, and LINA's own code needs no separate language runtime on any device. The LINA work harness runs inside the worker ([work-and-delegation.md](work-and-delegation.md)). The LIFE world engine is part of the LINA Core family; its packaging is a backlog item ([cognition-and-life.md](cognition-and-life.md)).

Go fits these components because:

- LINA Core, the TUI and device Node each ship as a single static binary per platform, cross-compiled from one source tree.
- Code that workers write gets a fast edit, build and test loop, measured as V7 (see Runtime acceptance).
- An always-on main keeps a low resident cost.
- It suits a main that may later serve many people.

The LINA app is one Flutter codebase in Dart, built natively for each platform it supports ([surfaces.md](surfaces.md)).

The conversation engine is LINA's own Go loop, built on the [Codex](https://github.com/openai/codex) tool contracts, with a structure that follows the [pi](https://github.com/earendil-works/pi) agent loop and conversation patterns from [Hermes Agent](https://github.com/NousResearch/hermes-agent) (see Conversation loop). pi and Hermes Agent are design references only: no code from either is vendored or translated line by line.

Each function has exactly one path. A function never switches to a second transport when the first one fails. A missing piece makes its function unsupported, and the function says so.

External components (document parsers, OCR, the local transcription component and similar) may be written in any language, need any runtime and carry any license, Python, a JRE and AGPL code included. They run only as separate processes at a version pinned by the release, and are never linked or loaded into a LINA process.

## Installed tools

LINA uses some tools that the user installs and that LINA neither ships nor pins: coding tools such as Codex, which LINA runs as workers ([work-and-delegation.md](work-and-delegation.md)), the command-line tools that plugins rely on, such as `gh`, and the local server commands of third-party plugins ([integrations.md](integrations.md)). Each is a separate project with its own releases and repository.

- LINA uses the installed tool found on `PATH` or at a path the user configured, and records the version it observed.
- LINA lists the versions of each such tool that it has verified. A version not on the list is reported to the user as unverified, with the version found and the verified versions, and LINA still uses the tool. A function becomes unsupported only when its tool is missing, can't be started or connected to, or fails its protocol handshake, and LINA says why.
- LINA never installs, updates or pins the tool on its own. Inside the user's Codex, the only thing LINA installs and updates is its own work harness plugin ([work-and-delegation.md](work-and-delegation.md)).
- LINA OS may offer to install these tools for the user ([product-families.md](product-families.md)).

## Processes

| Process | Built from | Runs as | Needed for |
| --- | --- | --- | --- |
| LINA Core | Go, a static binary | A service under the user's own OS user | Everything; it is the only canon writer. Agents are data that this one LINA Core runs; there is no process per agent ([product-families.md](product-families.md)). |
| `moirai-worker` | Go, a static binary | An optional background service of the LINA Core install | Background memory and planning |
| `lina` TUI | Go on Bubble Tea, a static binary | A user process connected to LINA Core over its local socket or the tailnet | The terminal surface ([surfaces.md](surfaces.md)) |
| Node | Go, a static binary | A service on each device | Device execution under grants |
| Worker | The coding tool the user installed, with the user's own configuration; Codex runs as `codex app-server` | A stdio child of LINA Core, or of Node on a remote device | Workers |
| LINA work harness (`lina-work`) | LINA's own Go, built as static binaries per platform and shipped in LINA releases as a Codex plugin | Inside the user's Codex, where LINA installs it at the first delegation | Delegated work |
| Sandboxed commands | Whatever a tool call runs | Children of LINA Core inside the OS sandbox | Shell and file tools |
| Plugin processes | Local MCP server commands of installed plugins, and the command-line tools that plugins rely on when LINA Core runs them for its own reads | Children of LINA Core under the user's OS user, outside the command sandbox, with the environment of sandboxed commands | Plugins ([integrations.md](integrations.md)) |
| External components | Any language, pinned by the release | Separate processes | Parsing, OCR, local transcription |
| opencodex | The upstream npm package [`@bitkyc08/opencodex`](https://github.com/lidge-jun/opencodex) (MIT), pinned by the release, on the pinned Node.js runtime the release ships with it | A local service installed with LINA, which LINA connects to | Every chat model call |

LINA Core never starts, stops or restarts opencodex on its own. The operating system's service manager runs it as its own service.

## Vendored source

Upstream code enters the LINA repository only as vendored source or generated upstream code, such as the Go types of the Codex app-server protocol, generated for each verified Codex version ([work-and-delegation.md](work-and-delegation.md)). How vendored code is kept unchanged, wrapped, patched, pinned and synced is defined in [dependencies.md](../policy/dependencies.md); the source, files and license notices are listed in [THIRD-PARTY-NOTICES.md](../../THIRD-PARTY-NOTICES.md).

## LINA kit

The LINA kit is the stateless Go module that LINA Core, the TUI and Node share. It lives in `kit/` of the LINA repository with its own `go.mod`, under the module path `github.com/thisisjun786/lina/kit`. LINA's own module uses it through a `replace` to that directory. It holds:

- the Responses adapter: openai-go with `store=false`, the encrypted reasoning round trip and the stream assembly rules of this document
- the loop core of the conversation loop (see Conversation loop)
- the tool executor and the sandbox wrapper
- the protocol types of the `client` and `node` connections, generated from the JSON Schema in [host-protocol.md](host-protocol.md)
- document parsing, passage anchors, citation checks and the web fetch tool

The web fetch tool is stateless: it takes one URL and hands the fetched document to the kit's document parsing. Its network access follows the caller's grant.

The Responses adapter, the loop core, the tool executor with the sandbox wrapper and the protocol types are built as kit packages from their first implementation. Document parsing, passage anchors, citation checks and the web fetch tool are a separate stateless part of the kit: LINA's materials features use it, and it is not tied to any one materials feature ([materials-and-knowledge.md](materials-and-knowledge.md)).

The kit never holds a default state path, a persona, skills, an agent's knowledge scope or a database writer. Callers explicitly pass state locations, the speaking agent's persona and skills, and the knowledge that agent may read, and each caller stores its own records. What differs per host (writer fencing, queues, the layer that owns retries) stays in the component that uses the kit. The kit has its own version, separate from product and protocol versions, and every build records it in its release manifest.

## Conversation loop

The conversation engine is LINA's own Go loop inside LINA Core, built on the kit's loop core. One loop serves every agent. Each conversation runs with the persona and personal knowledge of its agent and the common knowledge ([product-families.md](product-families.md), [conversation-and-memory.md](conversation-and-memory.md)). The Codex app-server never runs a conversation, and a Codex home never holds conversation state. Apart from installing its work harness plugin, LINA only reads the user's Codex home: for discovery ([work-and-delegation.md](work-and-delegation.md)) and as external evidence ([conversation-and-memory.md](conversation-and-memory.md)).

The loop's structure follows the pi agent loop (`packages/agent`):

- a stream function at the model boundary
- a transform of the context before each request
- a hook before each tool call, which can approve or block it, and a hook after it, which can rewrite its result
- steering input during a turn and follow-up input after it
- cancellation through the whole turn
- parallel tool calls

The loop behaves as follows:

- The inner loop runs tool calls and input that arrives while a turn runs (steering). The outer loop runs follow-up input after a turn ends.
- Tool calls of one response run in parallel, and their results return in the original call order. Argument validation and pre-tool checks run in order, before any call starts.
- A tool call in a response cut off by the output limit is never executed. The loop returns a result that asks the model to call again with complete arguments.
- On abort, every open tool call receives an `aborted` result, so the recorded history stays valid for the next request.

Retries and failover have one owner: LINA Core. The loop core and the SDK never retry on their own.

Conversation quality follows patterns from Hermes Agent (Nous Research):

- the person's standing preferences in every turn
- a brief about the person, synthesized from memory after each answer and used in the next turn
- memory written as statements, never as instructions to the agent
- memory review in the background, outside the request path
- a tool that searches past conversations
- a compaction summary written as a handoff for reference
- older tool outputs condensed into one-line notes
- full-text search that finds Korean words
- a tool that reads a skill body the turn did not receive

Where each applies is defined in [conversation-and-memory.md](conversation-and-memory.md). Recall is prepared outside the request path, apart from a time-capped query on the person's current input, and a compaction summary, written with the conversation model, is prepared before the request that needs it; only an in-turn compaction that must also cover the earlier verbatim tail, and a continuation turn, build their summary when they need it.

The conversation ledger, turn preparation (including the executable hash and opencodex readiness checks), request assembly, persona injection, compaction and resume are defined in [conversation-and-memory.md](conversation-and-memory.md).

## Model path

Every model call of LINA's own engines goes through opencodex, the local Responses API proxy. opencodex is a LINA dependency: LINA's installation installs the upstream opencodex package at the version the release pins, and the pin follows upstream releases under [dependencies.md](../policy/dependencies.md). LINA connects to its local Responses endpoint. The only exceptions are the non-chat adapters. There is no fallback to direct provider authentication.

### Requests

LINA Core calls the Responses API of opencodex at `http://127.0.0.1:10100/v1` directly with the official [openai-go](https://github.com/openai/openai-go) library at a pinned version, through a thin adapter between LINA's types and the SDK's types.

- `store` is `false`. Every request carries the full input it needs.
- With reasoning on, the request includes `reasoning.encrypted_content`. The [reasoning guide](https://developers.openai.com/api/docs/guides/reasoning) requires at least every reasoning item, tool call and tool output since the last user message to go back in the next request unchanged. LINA never sends an item of the current turn altered or dropped. When compacting earlier turns does not bring a follow-up request below the compaction threshold, LINA ends that turn and continues in a continuation turn whose request carries a compaction summary and the person's input once, so the round trip starts fresh ([Compaction and resume](conversation-and-memory.md#compaction-and-resume)). When no compaction runs, LINA sends every earlier item back unchanged as well, because the conversation history carries them all ([Compaction and resume](conversation-and-memory.md#compaction-and-resume)).
- `previous_response_id`, `store: true`, server-side compaction and WebSocket mode are never used.
- The request fields LINA sends form a list in the release manifest. A new field enters the list before it is sent.
- Auxiliary reasoning (internal inference without tools) is a separate request with no tool list and no persona. It runs as its own run kind, can cause no external effect, and its output is never recorded as part of the conversation.

### Stream assembly

LINA assembles the server-sent event stream itself:

- Text and argument deltas accumulate per output item. Deltas drive live display only.
- An output item is final only at `response.output_item.done`. The recorded item, and the item sent back in the next request, is the done item. Encrypted reasoning is taken from `response.output_item.done`, never from `response.output_item.added`, whose value may be incomplete.
- Tool calls are dispatched from final items only.
- `response.completed` closes a response. It is never evidence that a product effect happened.

### Failures and retries

- SDK automatic retries are 0, and the value is recorded in the release manifest.
- Once output has started, a request is never sent again. An accepted turn is never replayed automatically; a turn whose outcome is unknown stays unknown.
- Failures are typed from the response itself: proxy unavailable (`/readyz` 503 or a failed connection), authentication expired, and quota or combination unavailable (429, 503).
- LINA Core reads the request ID header, the error type and `usage` from every response and records them in the ledger with the agent id of the request.

### Models and the management plane

- The conversation model list comes from the opencodex catalog, and LINA Core records the catalog revision. The catalog is for display and is never capability evidence.
- Model and effort are fixed at request boundaries: per conversation request, at worker thread creation and at the next turn boundary. A running request or an active turn never switches model or effort.
- LINA never changes the model on its own. A model switch is an explicit decision and takes effect from the next request.
- Ownership of provider accounts, sign-in and model enablement is defined in [product-families.md](product-families.md). LINA Core reads the opencodex management plane read-only and never exposes it to LINA APP or to any remote client.
- A worker reaches models through its own configuration ([work-and-delegation.md](work-and-delegation.md)).

### Usage and cost

- Usage comes from `usage` in each conversation response and from the usage events a worker reports, such as Codex's `thread/tokenUsage/updated`. An unreported value is unknown, never zero.
- The total budget covers conversation requests, worker threads, the sub-threads a worker starts and auxiliary reasoning. Every agent draws on the one budget, and each usage record names the agent it served.
- The actual model, routing reason and cost per request come from the opencodex management plane (`/api/usage`, `/api/logs`), read by LINA Core. The management token lives in LINA's secret store ([filesystem.md](filesystem.md)) and is read only by LINA Core; it never reaches LINA APP, a remote client or a worker. When the plane can't be read, route and cost are unknown.
- Budget totals count tokens. Catalog prices and zero values are not actual cost.
- LINA Core reserves and settles the budget. At the limit the conversation stops sending requests, and workers are interrupted under the budget policy of [work-and-delegation.md](work-and-delegation.md). The Codex goal token budget is not used.
- A conversation request is matched to its opencodex record by the request ID header. A worker's model, route and cost are known only as far as the worker reports them.

### Request regression

A regression test compares LINA's requests with a baseline: the request that Codex sends through opencodex for the same conversation, with the Codex version recorded alongside the baseline. It compares fields, `include`, the reasoning resend and the built-in tool declarations. The parts that differ by design are listed with the baseline and left out of the match: Codex's base instructions for coding work, which a LINA request never carries, LINA's developer-role persona and context items, and LINA's own tools ([conversation-and-memory.md](conversation-and-memory.md)). It runs again whenever the opencodex or SDK pin, or the baseline, moves.

## Non-chat adapters

The Jev judge, embedding and transcription each have their own adapter outside opencodex. Their keys, billing and failure diagnosis are separate from opencodex, and none of them appears in the chat model list, the catalog or automatic routing. Jev is called only through its own typed adapter, the judge adapter, which speaks to a hosted judge API (Jev by default) or to a local judge process ([cognition-and-life.md](cognition-and-life.md)).

The embedding adapter calls the embedding endpoint the person configures: an OpenAI-compatible embeddings API and its key. Each vector index records the model id and the dimension it was built with. When the model changes, the index is rebuilt in the background, and until the rebuild finishes recall uses full-text search (FTS5) without the vector index. With no endpoint configured, recall does the same.

None of them blocks basic conversation:

- When embedding is off or failing, recall keeps working at lower quality, as defined in [conversation-and-memory.md](conversation-and-memory.md).
- Transcription defaults to a local transcription component pinned in the release, which needs no key and no setting. An external transcription API is optional, and the transcription adapter is that optional path. With no transcription at all, only recording-to-meeting-notes is unsupported.
- When no judge is available (Jev is off or has no key, and no local judge is running), rules and the main model produce the same function ([cognition-and-life.md](cognition-and-life.md)).

Each adapter reports its connection state, key presence and usage to settings. An adapter that is off reports its state and is never required at first run.

## Tools and sandbox

### Tool set

The conversation engine's tools are the same kind as a Codex worker's, declared with Codex's names and formats: shell execution (`exec_command`, `write_stdin`), file editing (`apply_patch`), `view_image`, `update_plan`, a question tool and a permission request. LINA adds its own tools: reading materials and memory, searching the conversation ledger, reading a skill body, the memory tools (write, correct and forget, defined in [conversation-and-memory.md](conversation-and-memory.md)), dispatching work, reading plans and querying its own events ([self-diagnosis-log.md](self-diagnosis-log.md)). Once LINA's materials take in web pages, LINA also declares the kit's web fetch tool as its own tool (see LINA kit). Under the default approval policy a fetch is a read and never asks ([main-authority.md](main-authority.md)).

Plugin tools are the tools of the plugins that are on ([integrations.md](integrations.md)). Each is named `<plugin>__<tool>`, at most 64 characters, and plugin tools are declared in a deterministic order. When there are many, the conversation declares a subset and loads the rest on demand. The release manifest lists the built-in tools. Plugin tools come from the user's installed plugins, so the conversation ledger records the hash of the plugin tool declarations sent with each request.

Tools are declared in `tools` on every request, and a change to the tool list applies from the next request. `apply_patch` is a custom tool; opencodex converts its format for the backend. LINA's `apply_patch` accepts the Codex patch grammar, and the grammar's test fixtures from Codex (Apache-2.0) run as regression tests with their notice kept.

Conversation and work are split by the kind of work, not by tools. The routing rule is in [work-and-delegation.md](work-and-delegation.md).

### Execution rules

- Before a tool runs, LINA Core checks the person the call acts for, the agent that makes it, scope, grant, target, revision and current epoch ([main-authority.md](main-authority.md)).
- Every tool has a deadline and always returns a result. A failure before the effect starts returns the failure and its reason. When the deadline passes while the effect runs, or its outcome can't be known, the tool returns "outcome unknown, do not retry" and the effect ledger records `unknown`. Effect states are defined in [host-protocol.md](host-protocol.md).
- The effect ledger deduplicates tool effects by the idempotency key defined in [host-protocol.md](host-protocol.md), never by call ID. The same effect called again with a new call ID never runs twice; the call returns the first result, or `unknown`. The record and key of a plugin write are defined in [integrations.md](integrations.md).
- An effect outside the grant, and every plugin tool call, follows the approval policy of [main-authority.md](main-authority.md), including its tiers for plugin tools and the rule for when no surface can present an approval request.

### Default scope

- The conversation working directory is `workspace/` under the state root, one directory that every agent uses ([filesystem.md](filesystem.md)).
- The user files LINA reads by default are the library scope: the set of connected sources ([filesystem.md](filesystem.md)). Nothing enters it unless the person connects it.
- The default grant, before any approval, is write access in the working directory and read access in the library scope.

### Shared writes

Every agent works in the same working directory, so two agents can change the same file. No agent locks another out; the checks below decide, and the record shows which agent changed what:

- `apply_patch` follows the Codex patch grammar and fails when its context lines no longer match the file. The agent reads the file again and retries on its current content.
- A shell write, such as one through `exec_command`, can't be checked before it runs. The effect ledger's record of the tool call holds the changed paths, their hashes before and after the call, and the agent id.
- Writes to materials, connected sources and plan objects keep their own revision checks: a write against a stale revision is refused with `stale_revision` ([main-authority.md](main-authority.md), [materials-and-knowledge.md](materials-and-knowledge.md)).

### Sandbox

Shell and file tools run inside an OS sandbox that LINA Core invokes directly through the kit's sandbox wrapper. Shell and file tools never run unsandboxed. Plugin processes run outside it (see Processes) and are controlled at the tool call ([main-authority.md](main-authority.md)).

- The write roots are the conversation working directory and the grant's scope. Everything else is read-only, and `.git` directories inside a write root stay read-only.
- Network is blocked by default. A sandboxed command gets network access in one of two ways:
  - A command that an installed plugin's service profile classifies, such as `gh` or `git` under the GitHub plugin, gets it when LINA Core approves its elevation at that command's tier ([integrations.md](integrations.md), [main-authority.md](main-authority.md)). This classification is the default for those commands.
  - Any other command gets it when the model asks for network for that command and the person confirms, as in Codex. The first request asks, with the choices once, always allow and deny. "Always allow" records a standing grant for commands with the same prefix, which the person revokes in settings at any time ([main-authority.md](main-authority.md), [surfaces.md](surfaces.md)).
- Sandboxed commands get LINA Core's environment without model-provider keys, so the user's own tools and settings, including the GitHub setup of [integrations.md](integrations.md), work there as they do for the user. LINA's secret store, the opencodex management token and data keys are never visible inside the sandbox.
- The policies follow the Codex sandbox source (Apache-2.0), with provenance recorded. The active sandbox policy is recorded in the release manifest.

On Linux the sandbox is [bubblewrap](https://github.com/containers/bubblewrap) with seccomp. bubblewrap runs as a separate executable and is never linked. Commands get their own user and PID namespaces, their own network namespace while network is blocked, and `no_new_privs`. The seccomp filter is a compiled BPF program shipped with the release for each architecture and handed to bubblewrap by file descriptor.

LINA Core runs on the Linux main ([product-families.md](product-families.md)), so the conversation engine's shell and file tools always run in the Linux sandbox. The kit's sandbox wrapper also serves hosts other than Linux: the commands that Node runs on macOS and Windows.

On macOS the wrapper uses Seatbelt: `/usr/bin/sandbox-exec` with an SBPL profile derived from the Codex profiles. Apple marks `sandbox-exec` deprecated and offers no command-line replacement, so the wrapper checks that it works on every start.

On Windows the wrapper runs commands only inside a verified Windows sandbox. A verified Windows sandbox is part of Windows Node acceptance; until it passes, sandboxed commands report unsupported on Windows instead of running unsandboxed.

LINA Core checks on start, and before the first tool call, that the sandbox works on this host. Unprivileged user namespaces, AppArmor restrictions and hardened kernels can each disable bubblewrap. When the sandbox is missing or fails, conversation continues, only shell and file tools are unsupported, and the user sees why.

## Workers

Workers are coding tools the user installed, driven through one worker adapter per tool; Codex is the first, through its app-server protocol over stdio. LINA uses the installed tool as it is (see Installed tools): its version, login, model provider, sandbox, approval mode, plugins and integrations are the user's own configuration. The LINA work harness is LINA's own rebuild of the CXC coding-work harness; it ships in LINA releases as a Codex plugin that LINA installs and updates in the user's Codex ([work-and-delegation.md](work-and-delegation.md)).

The adapter boundary, the Codex adapter, compatibility, readiness, assignment, approvals, the harness rules and merges are defined in [work-and-delegation.md](work-and-delegation.md).

## Storage

- Structured state lives in SQLite, used from Go through one SQLite binding. Which binding, with cgo or without, is chosen by the packaging and storage check (V2) and recorded in the release manifest. No other database engine or SQLite binding is used.
- Vector search uses [sqlite-vec](https://github.com/asg017/sqlite-vec) at a pinned version through that binding, linked into the binary or loaded as an extension as the binding requires. A sqlite-vec platform binary loaded at run time is verified against the digest in the release manifest before it loads.
- Canon databases run in WAL mode. Full-text search uses SQLite's FTS5. How recall uses FTS5 and sqlite-vec is defined in [conversation-and-memory.md](conversation-and-memory.md).
- Vector indexes are derived data, rebuilt from canonical text. Moving the sqlite-vec pin rebuilds the indexes and never touches canon.
- Backups use SQLite's online backup, never a copy of database or WAL files. What a backup generation holds is defined in [filesystem.md](filesystem.md).

## Packaging

### Binaries

LINA Core and the `lina` TUI ship as one static binary per platform, and Node ships as its own static binary per platform, each built with the pinned Go toolchain. A device that runs only Node or only the TUI installs just that binary and needs no separate language runtime. The release manifest records each binary's version and digest and the Go toolchain that built it.

Supported platforms are defined in [product-families.md](product-families.md). A platform and architecture enter release builds, and with them the supported-combination table of a release ([host-protocol.md](host-protocol.md)), only after the packaging and resident measurement passes on them. Until then they are built only as development builds.

### The `lina` command

LINA Core and the TUI are one executable, `lina`. Its subcommands cover these uses:

| Use | What it does |
| --- | --- |
| Core service | Runs LINA Core. The service manager starts LINA Core this way (see Services). |
| TUI | Opens the terminal surface, a separate process that connects to LINA Core over the `client` connection ([surfaces.md](surfaces.md)) |
| Install | Places the pinned opencodex package and its Node.js runtime, registers the LINA Core and opencodex services, and `moirai-worker` when it is part of the install, and creates the state root ([filesystem.md](filesystem.md)). When opencodex has no signed-in model provider, it opens the opencodex sign-in ([product-families.md](product-families.md)). |
| Backup and restore | Takes a backup generation and restores one ([filesystem.md](filesystem.md)) |
| Diagnosis | Reports the state and readiness of each component, with the cause of each failure |

### opencodex and its Node.js runtime

opencodex is an npm package and is the only part of a LINA install that runs on Node.js. An install that contains LINA Core ships the pinned opencodex package together with a verified Node.js runtime at the version opencodex needs, and runs opencodex as its own service. Verified means the official Node.js release archive, checked against the project's signed checksum list ([verifying binaries](https://github.com/nodejs/node#verifying-binaries)), with its version and digest in the release manifest. opencodex starts on that runtime by absolute path. A system Node.js, a version manager or `node` on `PATH` is never used. A runtime update is a LINA release and is applied as described in Updates.

### Services

- LINA Core is a systemd service running as the user's own OS user, so it sees the user's tools and settings the way the user's own Codex does.
- `moirai-worker` is an optional systemd user service of the same OS user.
- Node registers with the device's service manager: systemd on Linux, launchd on macOS and a Windows service on Windows.
- An install never restarts an existing service without authorization. New settings, databases, services, ports and paths never overwrite existing ones.

### Install compositions

Installation modes are defined in [product-families.md](product-families.md). Each mode is one of these runtime compositions:

| Composition | Contains |
| --- | --- |
| Conversation only (general Linux) | LINA Core, the TUI, opencodex, optionally `moirai-worker`, and bubblewrap |
| Minimal general Linux | Conversation only, plus the worker adapters, which drive a coding tool the user installed when work is delegated |
| LINA OS main | Minimal general Linux, plus Node, LINA APP and exactly one update owner |
| Additional device Node | Node. Coding work there uses the coding tool installed on that device, and is supported only after transfer between that device and the main is verified. |
| LINA APP only | LINA APP |
| Development host | Not a product composition |

When a piece is missing:

- Missing conversation pieces make conversation fail its readiness checks. There is no `PATH` fallback.
- A worker that is missing, can't be connected to or fails its handshake, or a LINA work harness that LINA can't install, makes only delegation unsupported. An unverified worker version is reported and never stops delegation.
- A missing `moirai-worker` makes only background memory and planning unsupported.
- A running service is never evidence that a turn succeeded.

### Release manifest

Every build from a clean commit, a development build included, writes a manifest next to its executables. Every build's manifest holds the same fields as the release manifest, listed below, and records its commit. The release manifest is the name of a release build's manifest.

- A build's version is its release tag, or `dev-<commit>` for a build that is not a release.
- The supported-combination table of a build lists the components built together from the same commit, so the TUI and LINA Core of one build find their row ([host-protocol.md](host-protocol.md)).
- A running component reads the values this design places in the release manifest, such as the request field list, the harness revision and the verified worker versions, from the manifest of its own build.
- Turn preparation compares the running LINA Core executable with the manifest of its build ([conversation-and-memory.md](conversation-and-memory.md)).

The release manifest records:

- external components, each with version, digest, execution mode, source and license
- versions and digests of LINA Core, Node, LINA APP and `moirai-worker`, and the Go toolchain that built them
- opencodex's Node.js runtime, the SQLite binding and sqlite-vec, with versions and digests
- the LINA kit version
- the conversation SDK version and its retry values
- the request field list
- the built-in conversation tool declarations and the sandbox policy
- the LINA work harness: its revision, content hash, hook declaration and contract version
- the verified versions of the installed tools LINA uses (see Installed tools)
- the process environment rules for sandboxed commands and plugin processes
- the service configuration
- the state schema version
- the supported-combination table ([host-protocol.md](host-protocol.md))
- opencodex: its version, digest and address format

The manifest pins only LINA's own artifacts and dependencies: its components, opencodex with its Node.js runtime, and the external components LINA ships. A change to a pinned value (opencodex, harness, SDK, request field list, environment rules, retry values) lands in the same change as its manifest change.

### Packaging and resident budget

LINA Core and Node each have a packaging and resident budget. The budget covers artifact size, start time, and resident memory and CPU over 24 hours, measured with OS instrumentation. It is declared before the measurement runs and recorded with the result.

## Supply chain

Every external dependency, vendored source and bundled binary follows [dependencies.md](../policy/dependencies.md). Its gates run in the local foundation verification from the first commit ([foundation.md](../policy/foundation.md)).

## Updates

LINA OS composes pinned artifacts. Replacing the OS and migrating or rolling back state schemas are separate operations.

An update runs in this order:

1. Drain: settle in-flight effects and record any whose outcome is unknown.
2. Snapshot the ledger, the schemas and the manifest.
3. Prepare the new composition: new binaries and settings, with the previous ones kept for rollback.
4. Smoke: one conversation request with its readiness checks, and the worker readiness check when a worker is installed.
5. Resume.

Rollback restores the previous compatible composition from the kept binaries and the pre-update snapshot. Binaries are never rolled back over an irreversible migration. When an update fails, the install restores the previous compatible composition, or holds: it stops, keeps the current state intact and reports. It never leaves a half-updated component running against data it can't read. LINA OS adds steps of its own around this order (see Deferred).

- The main gets release artifacts from the private GitHub Releases of the LINA repository with the owner's `gh` authentication, and distributes Node binaries and their updates to devices over the tailnet ([releases.md](../policy/releases.md)).
- opencodex is updated only as part of a LINA update, by its pin. An update never starts, stops or updates a tool the user installed, such as a worker.
- A hash of a dirty work tree is never a release.
- An Omarchy update is never a ready receipt for LINA's composition. Snapshot settings, a CLI version or an active service never prove an install composition, a restore or model readiness.

## Recovery

LINA Core dispatches nothing before its new epoch is durable ([main-authority.md](main-authority.md)). On restart, LINA Core stops pending approvals, tool calls and background effects and fences the previous process generation: a late response from that generation never changes current state. An effect that couldn't be stopped is recorded as unknown and is never re-run automatically. Worker turns cut by the restart follow [work-and-delegation.md](work-and-delegation.md).

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

These runtime failures must end as their rules require:

| Injection | Required outcome |
| --- | --- |
| Digest mismatch of opencodex's Node.js runtime or of sqlite-vec | The component doesn't start and reports the mismatch |
| Sandbox unavailable | Conversation continues; shell and file tools are unsupported and the reason is shown |
| opencodex unavailable | No request is sent; the TUI shows the typed cause |
| LINA Core killed with a tool call in flight | The effect is unknown and never re-run; a late result from the old generation is fenced |

## Remote access

Remote access uses [Tailscale](https://tailscale.com/kb) only. LINA APP, the TUI and Node connect to LINA Core directly inside the tailnet. Tailscale owns device identity and access control. LINA has no login of its own and registers a device on its first connection. A LINA admits only its owner, so every client connection acts for the owner ([host-protocol.md](host-protocol.md)). LINA Core accepts remote connections only on its tailnet address; there is no relay and no public listener. A relay and a login of LINA's own are on the backlog ([cognition-and-life.md](cognition-and-life.md)).

## LINA OS composition

LINA OS is Linux based on [Omarchy](https://github.com/basecamp/omarchy) 4. It composes verified LINA Core, Node and LINA APP artifacts, pinned by manifest with version and digest, together with opencodex and its pinned Node.js runtime and an operating, update and recovery environment. opencodex on LINA OS never runs on a distribution-provided Node.js. Its profiles are defined in [product-families.md](product-families.md).

- opencodex is part of LINA's installation on LINA OS, as on every install.
- LINA OS may offer to install a coding tool such as Codex for the user, which LINA then runs as a worker. It is the user's installed tool (see Installed tools); LINA OS neither pins nor owns it.
- An Omarchy root snapshot or an Omarchy update is never a restore or a readiness proof for LINA Core. The recovery units of LINA OS are defined in [filesystem.md](filesystem.md).
- Omarchy 4's desktop shell is one Quickshell-based shell that provides the bar, the launcher and notifications. Its active theme is `~/.local/state/omarchy/current/theme/colors.toml`, which third-party apps follow through templates in `~/.config/omarchy/themed/` and hooks in `~/.config/omarchy/hooks/theme-set.d/`.
- Omarchy's default packages lack `fcitx5-hangul`. LINA OS adds it with its Hangul input setup, so Korean input works in the LINA app and the terminal.

How LINA APP and the TUI adapt to Omarchy is defined in [surfaces.md](surfaces.md).

## Runtime acceptance

The first implementation issue demonstrates the runtime with these checks. Each check's baseline comes from outside LINA: requests sent by Codex, the opencodex endpoint, the installed Codex, the Go module checksum and vulnerability databases, and OS instrumentation.

| Check | What runs | Passes when |
| --- | --- | --- |
| V1 opencodex pass-through | openai-go through opencodex for three or more turns with `store=false`, the encrypted reasoning round trip, a custom tool (`apply_patch`) and parallel tool calls | Every turn round-trips without rejection, and the fields match the Codex baseline request apart from the parts that differ by design (see Request regression) |
| V2 packaging, storage and residency | Static binaries per platform; SQLite with sqlite-vec from Go (WAL, FTS5 and vector queries); LINA Core's executable, built from the same commit, for 24 hours as a systemd service on Omarchy x64 and as a launchd job on macOS arm64. The macOS run stands in for Node's budget, because Node uses the same runtime parts. | sqlite-vec loads, the services start, and size, start time and resident memory and CPU stay within the budget declared before the run on both platforms; the chosen SQLite binding is recorded in the release manifest |
| V3 own conversation loop | LINA's Go loop with openai-go through opencodex: streaming, tool calls with their before and after hooks, steering input, cancellation and parallel tool calls | The loop's tests pass |
| V4 LINA work harness | The LINA work harness, installed as a plugin in the user's Codex by an install command, on a test assignment from LINA Core | A stage receipt and a completion receipt reach LINA Core through LINA's work tools; the Stop hook round-trips: a managed turn that was about to end without a result continues through the Stop hook, and its receipt reaches LINA Core; and LINA Core records a test verdict on the receipts ([work-and-delegation.md](work-and-delegation.md)) |
| V5 worker adapter round trip | Go types generated from the JSON Schema that the installed Codex writes with `generate-json-schema --experimental`, then `initialize`, `thread/start` with `dynamicTools`, `item/tool/call` and turn completion | The round trip completes with the installed Codex, and the adapter records its version |
| V6 supply-chain baseline | The Go and npm gates of [dependencies.md](../policy/dependencies.md), run by the local foundation verification ([foundation.md](../policy/foundation.md)) on the minimal dependency set, with `go.sum`, the module count, the opencodex lockfile and its transitive package count recorded | The gates pass, `go.sum` verifies against the checksum database, `govulncheck` passes, and no npm package needs a lifecycle script |
| V7 development loop | A worker's edit to Go code, then `go build`, `go vet` and `go test` for the affected packages; and a full local `foundation` run (`node scripts/foundation.mjs --full`) | The time from the edit to the result of those commands, and the full `foundation` time, stay within the budget declared before the run |

## Deferred

- The packaging and resident budget for LINA Core and Node: declared in the first implementation issue before the V2 measurement runs, and recorded with its result. A result outside the budget reopens the language choice.
- The development-loop budget: declared before the V7 measurement and recorded with its result. A result outside the budget reopens the language choice.
- The SQLite binding: chosen by V2 and recorded in the release manifest.
- The Windows sandbox mechanism of the kit's sandbox wrapper: chosen and verified by the Windows Node implementation, which cannot be accepted without it.
- Exact pins (the Go toolchain, openai-go, the MCP Go SDK, Bubble Tea, the SQLite binding, sqlite-vec, the JSON Schema to Go type generator, opencodex and its Node.js runtime): set by the first implementation issue and recorded in the release manifest.
- The verified versions of each installed tool: set by the first implementation issue (V5 for Codex) and recorded in the release manifest.
- Whether LINA Core as a service sees the user's login environment (`PATH`, the SSH agent and the keychain), or reads the login shell's environment once at start instead: measured by the first implementation issue.
- Per-tool deadlines: set during implementation acceptance of the conversation engine.
- Absolute install locations, the absolute state root, service names and socket paths per OS: set during packaging implementation acceptance.
- The subcommand names and flags of `lina`: set by the first implementation issue.
- The path and format of a build's manifest, how a build writes it, and its foundation check: set by the first implementation issue.
- Publishing binaries and their manifests: added to [releases.md](../policy/releases.md) when LINA is first packaged.
- The default embedding model: chosen by the implementation issue that brings memory recall into answers.
- LINA OS's own update steps (a pre-update snapshot check and an extra backup before drain): set during LINA OS implementation acceptance.
