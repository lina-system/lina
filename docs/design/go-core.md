# LINA Core in Go

This document fixes how LINA Core is built in Go where the design contracts leave a point open: processes, packages, interfaces and algorithms. It is a design document, subordinate to the thirteen contracts in this directory: [product-families.md](product-families.md), [main-authority.md](main-authority.md), [filesystem.md](filesystem.md), [runtime.md](runtime.md), [host-protocol.md](host-protocol.md), [conversation-and-memory.md](conversation-and-memory.md), [work-and-delegation.md](work-and-delegation.md), [surfaces.md](surfaces.md), [integrations.md](integrations.md), [materials-and-knowledge.md](materials-and-knowledge.md), [cognition-and-life.md](cognition-and-life.md), [non-competition.md](non-competition.md) and [self-diagnosis-log.md](self-diagnosis-log.md). It cites contract sections instead of restating them. Where this document and a contract differ, the contract wins and this document is corrected. A contract changes only in its own file. The five changes this design asked for were accepted on 2026-10-20 and are written into the contracts; "Contract change proposals" says where each now lives. The terminal UI is designed in [tui.md](tui.md), the order of work is in [go-roadmap.md](go-roadmap.md), and the patterns behind the choices are in [hermes-patterns.md](hermes-patterns.md). Finishing this document does not mean any package, table or process is built or accepted; runtime proof belongs to the implementation issues that consume it.

## North star

The person does not look at memory, issues, detailed plans or pull requests. They see the design at a glance with how far each piece of work has come, and the list of what they must do or decide. Replies are short and lead with the conclusion; detail goes to the work panel (a pager in the TUI). See [product-families.md](product-families.md#north-star) and [surfaces.md](surfaces.md#default-view).

Core carries this with five mechanisms, each a decision below:

- The waiting list and the progress lines are computed by Core (C-55).
- Reply budgets are enforced while a reply streams (C-51).
- Results are typed and shown as cards (C-52).
- Work that is heavy never runs inside the turn (C-49, C-50).
- The audit trail is reachable only through the Details capability (C-42).

One structural rule follows. The Go types that make up the default view (progress line, waiting row, card, notice) have no field for an id, a path, a hash or an internal state name. Those live in Details documents. A change that lets an internal term into the default view is a defect.

## Conventions

- `C-nn` names a decision of this document and is never reused. `T-nn` names a decision in [tui.md](tui.md).
- A number marked (P) is a starting value to measure. A number without (P) comes from a contract or a protocol.
- Sketches fix seams, not exact signatures. `ctx` is `context.Context`. Ids are opaque string types.
- Terms are the contracts' own: an agent is a persona, a worker is a coding tool, a sub-agent is a background helper of Core. Korean terms are 목표, 일, 할 일.
- Policy documents: [foundation.md](../policy/foundation.md), [dependencies.md](../policy/dependencies.md), [integration.md](../policy/integration.md).

## Processes, binaries and packages

**C-01 One main (changed).** One `lina core` runs per state root, as a user service under the person's OS user ([runtime.md](runtime.md#processes)). Inside it run these goroutine families: one lane per conversation, the background-run supervisor, the gateway adapters, the scheduler, the completion-event reactor, the client hub and the job API for `moirai-worker`. Children are created through `kit/proc` in a Core-owned process group (a cgroup where the service manager has one): the Claude adapter (started on first use, stopped after idle), the `grok` command, workers, plugin servers and sandboxed commands. There is no process per agent and no model proxy. A foreground `lina core` for development runs only where install created the main id file.

The control-file increment (#36) starts with `lina core --state-root <path> --socket <path>`. The state root must already exist, and installation must supply `system/core/main.json`; Core never invents a replacement identity. This replaces #31's socket-only invocation and placeholder main id and epoch. Installation and the service unit remain G1-23 work.

**C-02 Binaries and modules (changed).** Subcommands of `lina`, proposed here because [runtime.md](runtime.md#the-lina-command) defers the names: `core`, `tui`, `install`, `backup`, `restore` (main stopped), `doctor`, `replay`, `export`, `about`, `version`. `version` prints the version, the commit and the build date that the build sets with `-ldflags` ([versioning.md](versioning.md)). Separate artifacts: `lina-node` and `moirai-worker`. Modules: the root module and `kit` (`github.com/thisisjun786/lina/kit`), joined by `go.work` and a `replace`.

The initial command skeleton registers `core`, `tui` and `version` in `lina`, and `version` in `lina-node`. Both `version` commands print the shared `internal/buildinfo` version line on stdout and exit 0 ([versioning.md](versioning.md#what-a-binary-reports)). Until their implementation lands, `core` and `tui` report that they are not implemented on stderr and exit 70. Missing or unknown subcommands print usage on stderr and exit 64. Future handlers dispatch into internal packages. Node depends on the permitted kit packages and the Go standard library; its command entry point may also import `internal/buildinfo`.

Layout: `cmd/lina`, `cmd/lina-node`, `cmd/moirai-worker`, `protocol/schema`, `protocol/fixtures`, `persona/<preset>/`, `skills/` (built-in, embedded), `catalog/` (plan and plugin catalog sources), `adapters/claude/` (TypeScript, lockfile), and `internal/` with `core`, `node`, `tui`, `clientapi`, `buildinfo` (shared binary metadata), `design` (generated from the token source) and `fakecore`. Builds are static; cgo follows the SQLite binding chosen by [V2](runtime.md#runtime-acceptance). The kit wrapper starts bubblewrap directly ([runtime.md](runtime.md#sandbox)); there is no hidden re-exec mode.

Kit seams, following [runtime.md](runtime.md#lina-kit):

| Package | Holds | Never holds |
| --- | --- | --- |
| `provider` (`responses`, `gemini`, `grokacp`, `claudeproc`) | The adapters | The secret store; retries |
| `loop`, `tools`, `sandbox`, `proc` | Loop core, executor, sandbox wrapper, environment builder, process groups | State paths |
| `protocol`, `rpc`, `result`, `materials` | Generated types, JSON-RPC line peer, typed-body validation and rendering, parsing, citation check, web fetch | A database writer |
| `grant`, `lease`, `secret`, `diag` | The Authorization check shared by Core and Node, leases, a placeholder-printing secret type, closed enums | Persona, skills, knowledge scope |

Callers pass state locations, persona, skills and knowledge scope.

**C-40 Package map and import rules (kept).** Layers under `internal/core/`:

| Layer | Packages |
| --- | --- |
| L1 | `canon` (writer, fence, migrations), `eventstore`, `secrets`, `ctlfiles`, `guard` (masking), `ids` |
| L2 | `authority` (gate, grants, approvals, effect ledger), `lifecycle`, `budget`, `manifest` |
| L3 | One store per table group: `ledger`, `inbox`, `agents`, `memory`, `retract`, `skills`, `records`, `assets`, `work`, `timers`, `plugins`, `channels`, `accounts` |
| L4 | Flows: `lane`, `assemble`, `compaction`, `turn`, `runs`, `reply`, `results`, `modelpath`, `exec`, `exchange`, `gateway`, `worker`, `reactor`, `review`, `ghclient`, `progress`, `waiting`, `importer`, `backup`, `update`, `recover`, `clientsrv`, `jobs`, `mcphost` |
| L5 | `boot`, `supervise` |

Outside the Core layers, `internal/buildinfo` owns the linker-supplied version, commit and UTC build date and the embedded VCS fallback. It imports only the standard library and is shared by the two command entry points.

Rules: imports run downward. L3 stores never import each other; L4 composes them inside one `canon.Writer.Do`. `assemble` is pure. `kit` imports nothing from the root module. `internal/node` and `cmd/lina-node` import only the standard library and the kit packages listed for Node, with one exception: `cmd/lina-node` may import the exact package `internal/buildinfo`. This exception does not extend to Node runtime packages or buildinfo subpackages. Charm libraries appear only in `tui/term` and `tui/ui`. `os/exec` appears only in `kit/proc` and `kit/sandbox`, and `os.Environ` only in `kit/proc`. Secret reveal happens in two functions. The default-view types live in a `view` package with no id fields. All of this is one ordinary `go test` (`internal/archtest`) that the components check of [foundation.md](../policy/foundation.md) already runs. It adds no gate.

**C-56 Moirai worker boundary (new).** [runtime.md](runtime.md#processes) and [cognition-and-life.md](cognition-and-life.md#request-path-and-background) make `moirai-worker` an optional separate Go service. It opens canon read-only, holds no credential, and talks to Core over a 0600 Unix socket in a 0700 directory at the path the install sets ([runtime.md](runtime.md#deferred) leaves socket paths to packaging). It asks Core for two things: a model call on Core's path under the budget and the run kind (`Aux`), and a write through the Moirai write path (`Apply`), which checks the epoch fence and the write rules. Each job has a schedule, a budget, a switch and an adoption record, is idempotent, and keeps a watermark. Killing the worker leaves canon unchanged. Without the socket only background memory and planning are unsupported. A summary on demand runs in Core, so conversation never depends on the worker. The release manifest pins the worker with Core, so this socket has no version negotiation.

```go
// internal/core/jobs: what moirai-worker may ask of Core.
type API interface {
	Aux(ctx context.Context, r AuxRequest) (AuxResult, error) // model call on Core's path
	Apply(ctx context.Context, p Proposal) (Applied, error)   // the only write path; epoch-fenced
	Report(ctx context.Context, j JobID, s JobState) error    // schedule, budget, adoption record
}
```

## Start, recovery, idle and update

**C-03 Lock, control files, epoch (kept).** Core takes a non-blocking `flock` on `system/core/core.lock`. The files under `system/core/` are the lock, `epoch` (a decimal string, CP-02), `run.sentinel`, `halt.json`, `lifecycle.json` (the restart request) and `main.json` (the main id; no key). Data records are written by temp file, fsync, rename and directory fsync, at mode 0600 in a 0700 directory. The lock is the exception: it is created once and fsynced with its directory, then opened and flocked without replacement or unlinking, so all contenders use the same inode. None is in a backup generation, and a LINA restore leaves them alone (CP-03). A state root on a non-local filesystem is refused. The new epoch is `max(epoch file, highest epoch in canon by read-only peek, peer_seen_max) + 1`, fsynced before canon opens (CP-01). `peer_seen_max` is the highest epoch a peer reported when it refused a stale one. The epoch fence is the first statement of every write transaction. A lower epoch fails `stale_epoch` and latches the writer.

The initial identity and sentinel formats are:

```json
{"main_id":"main-example"}
```

```json
{"epoch":"42","socket":"/absolute/path/to/core.sock"}
```

`main.json` holds a nonempty opaque `main_id` and no key; it is required before a foreground start. `run.sentinel` records the minted epoch and the owned client socket path, and is removed with a directory fsync after orderly shutdown. At step 5 it keeps the previous sentinel's socket path, or the empty string when no socket has been owned, and any pending bind intent. At step 8, after validating the socket directory, reclaiming a recorded stale target if necessary and confirming that the target is absent, Core durably adds an optional `pending_socket` absolute path while retaining the previous owned `socket`. It records this intent before calling the listener's bind. If a previous pending path differs from the requested target, Core validates and reclaims that pending path before replacing the intent. A failed bind clears the new pending intent. After a successful bind at a different path, Core validates the previous owned socket directory and retires the previous socket under the same stale-socket checks before publishing the new owned `socket` and removing `pending_socket`. Until retirement succeeds, the durable sentinel retains both the previous owned path and the new pending path. A kill before retirement or final publication can therefore recover either path on the next start, and a completed path change leaves the previous path available for later reuse. A retirement failure closes the new listener, refuses startup and preserves both recovery paths in the sentinel. An occupied target is refused before recording new intent, including on repeated starts, and an unsuccessful path change retains the previous owned socket identity. Existing sentinels without `pending_socket` remain valid; a present field must be a string that is empty or absolute.

Recovery receives the previous sentinel before its replacement. Only a socket named by that previous sentinel's `socket` or `pending_socket`, at mode 0600, whose connection probe returns connection refused may be removed while the root lock is held. Live sockets, other occupied paths and unrecorded stale sockets are preserved. Before reclaiming or binding a socket, its parent must pass the same current-user ownership and private-directory checks as the state directories; a foreign-owned or group/other-writable parent is refused with exit 78. The socket is created at mode 0600 by binding with umask 0177 on a dedicated Linux thread with an unshared filesystem context. The thread is retired after binding; other services retain their ambient umask. No post-bind chmod is needed, so a kill immediately after bind leaves a private, recoverable pending socket. If the filesystem context cannot be isolated, startup refuses before binding.

The root, `system/` and `system/core/` must be owned by the current user and private (0700); existing unsafe paths are refused, not chmodded. Control records must be regular files at 0600, owned by that user, without symbolic or hard links. Records larger than 64 KiB, malformed JSON objects and invalid epochs stop startup with exit 78. The epoch file contains only its canonical decimal digits, with no whitespace; overflow refuses the start and preserves the record. Linux filesystem validation permits ext-family, XFS, Btrfs, tmpfs, overlay, F2FS, ZFS, NILFS, ReiserFS and JFS. Network filesystems, FUSE, pseudo filesystems and unknown drivers are refused before control files are created. Persistence still depends on the chosen filesystem's guarantees; tmpfs is local but does not survive a reboot.

**C-04 Start, RECOVERING, halt, shutdown (kept).** Start steps:

1. Refuse a bad state root (exit 78).
2. Take the lock (exit 69 if held).
3. Open the event writer. Events carry a null epoch until step 5.
4. Read the sentinel, the halt marker and the restart request.
5. Mint the epoch.
6. Open canon, probe, migrate.
7. Mirror the epoch into canon.
8. Bind the sockets. The state is RECOVERING.
9. Reconcile.
10. READY.

The #36 startup coordinator executes these boundaries in order. Event writer, read-only canon peek, canon open/probe/migrate, epoch mirror and reconciliation have explicit service interfaces for their owning implementations. In this increment the CLI has no event writer or canon implementation and serves only the handshake; an absent or empty canon directory is allowed, but an existing canon store without its service is refused before minting an epoch. The coordinator passes a null epoch to startup events by leaving the event writer unbound until step 5; it supplies the new epoch only after the file and directory fsyncs succeed. Canon's writer cannot open before that boundary.

Steps 1–4 have no new epoch and confer no authority. A kill at one of those boundaries is followed by an epoch above the last durable high-water mark; a kill at or after step 5 is followed by an epoch above that killed run's minted epoch. Subprocess tests acknowledge each of the ten completed boundaries over a pipe before SIGKILL, plus the durable pending-intent, immediate socket-creation and bound-before-publication boundaries within step 8; readiness and crash injection never rely on a fixed startup delay.

At step 8 the client service accepts handshakes in RECOVERING. Subsequent requests return `unavailable` until reconciliation completes. READY admits the operations implemented by that client service; the current handshake-only service still grants no capabilities. A halt marker keeps Core in RECOVERING and is never cleared by startup or shutdown. The previous sentinel, halt marker and restart request are passed to reconciliation without silently discarding invalid records.

Missing or invalid command arguments exit 64; lock contention exits 69; invalid state, corrupt control records or socket startup failures exit 78. SIGTERM and SIGINT stop accepting clients, close accepted connections (including incomplete handshakes), remove the owned socket and exit 0. Shutdown closes the socket service, canon and event writer in reverse acquisition order before releasing the lock. Each service receives a five-second shutdown deadline and must honor it. An orderly shutdown removes the sentinel and preserves the epoch, main id, halt marker and restart request. Startup failure unwinds acquired resources, retains any minted epoch and leaves the sentinel for the next recovery.

The reconcile table follows [runtime.md](runtime.md#recovery) and [main-authority.md](main-authority.md#command-outcomes):

| Found | Outcome |
| --- | --- |
| Model request or partial stream | Discarded; the turn is `interrupted` and not replayed; one plain line in the conversation |
| Tool intent with no receipt | `unknown`; never run again; an irreversible one waits for the person |
| Node effect, Node not back | `unknown` until the Node reports |
| Worker turn cut by the restart | `unknown`; settled by reading the thread |
| Pending approval | Stopped; a late button is answered stale |
| Background run that was working | Resumed once from its checkpoint; a second cut lands it failed or partial |
| Outbound channel send, outcome unknown | Not sent again |
| Channel update | Handled once by its stored cursor and key |
| Agent exchange | Mailbox is durable; re-dispatch once by deadline |
| Completion events | One reconcile pass over every task in progress or watched |
| Anything from a restored generation | Non-terminal external-effect rows become `unknown`; leases and claims are void |

Halt: `SQLITE_CORRUPT`, `NOTADB`, or an I/O error whose read-only reopen fails writes the halt marker. The process stays in RECOVERING, serves reads and `doctor`, issues no grants, starts no adapters and fails writes with `store_write_halted` (a client sees `unavailable`). Only a verified whole-generation restore clears it. Disk full is not a halt. Shutdown runs the start stack in reverse with a cap per phase.

**C-05 Idle and leases (kept), C-06 Restart and update (kept), C-07 Hot reload (kept).** Idle is decided by effects, not chat silence: Idle, Busy(reasons) or Unknown, and Unknown counts as busy.

- Hard, blocking: a tool call or send with an effect in flight, an open Node command, a worker turn, a migration or update, a restore.
- Live, waited for and never ended by timeout: a streaming model request, a read-only tool.
- Reported, not blocking: queued input, timers, background runs (they resume once), derivation, backups.

Every kit call that goes outside takes a `lease.Lease`, so a missing lease does not compile. A restart request is a durable record in `lifecycle.json`. `when_idle` is the default and `drain` is explicit. A restart is never forced by timeout; only the person may force it. Exit codes: 0 stop, 75 planned restart, 78 fatal, 69 lock held. Settings, persona, bindings and plugin lists change by control verbs and never restart Core.

The #37 lease API is `Acquire(ctx, lease.Activity{Class, Reason}) (lease.Lease, error)`. Classes are `Hard`, `Live` and `Reported`; an unknown class or empty reason is refused. Kit adapters carry the lease in their typed API and enter external work through `lease.Do(ctx, lease, call)`. Its missing-argument fixture must fail compilation, and its companion with a lease must compile. Zero and released leases are rejected before the callback runs. A lease is an activity record, not an authorization grant. Leaser implementations register activity before constructing its handle with `lease.New` and supply the callback that removes it.

Lease copies share one lifetime. `Release` is idempotent, prevents new calls and removes the activity only after every call already inside `Do` has returned, including error and panic paths. Context cancellation never removes an activity by itself. Callers release leases when their work finishes; a call still running remains blocking even after its owner requests release. The kit neither retries the call nor forces completion on a timeout.

`lifecycle.NewRegistry` combines local leases with an optional reader of caller-owned activity. A nil reader means all activity is represented by local leases, with no implicit state path. A configured reader must return a consistent snapshot and honor cancellation; a failed read, missing record, invalid activity, canceled read or uninitialized registry yields `Unknown`. `IdleReport.Busy()` treats every state other than `Idle` as busy. Hard and Live entries appear in `Blocking`; Reported entries appear in `Reported` and do not prevent idle. Reports are independent copies ordered by class and reason, and duplicate activities remain separate until each lease is released. The reader's error is retained in the report.

`Edges` is a buffered, coalescing wakeup for one lifecycle observer, which reads `Idle` again after a notification. Lease acquisition and completed release notify automatically; the owner of an additional activity source calls `Notify` after that source changes. Notifications never wait for the observer. This increment exposes only the package API: the client protocol still has no idle or restart operation. The restart fence and its protocol behavior remain G1-24 work.

```go
type Registry interface { lease.Leaser; Idle(ctx context.Context) IdleReport; Edges() <-chan struct{} }
type Fence interface { Prepare(ctx context.Context) (Token, IdleReport); Commit(Token) error; Cancel(Token) }
```

**C-38 Update and recovery (kept).** The five steps of [runtime.md](runtime.md#updates) map to code:

- Drain is the fence.
- Snapshot is the manifest and schema record plus a recent verified backup generation, taken now if none is recent.
- Prepare unpacks after a digest check into a version directory and runs `core --preflight` in a separate process, with no lock and no writes. It checks the schema and the supported-combination rows.
- Smoke is one conversation readiness check and worker readiness.
- Resume.

The gate needs every cell: idle, candidate verified, schema relation, peer rows, free space, a recent verified generation. Rollback restores the kept binaries, is automatic only while the new binary never reached READY, and never runs over a migration that cannot be undone; afterwards the default is a forward fix. The Claude adapter, its lockfile and the SDK pin move only with a LINA update; tools the user installed are never touched. A store newer than the binary exits 78 without writing. Migrations are forward-only, numbered, one transaction each.

## Client service

**C-42 Client service (changed).** The wire is [host-protocol.md](host-protocol.md#client-connection). This decision fixes the server side.

- Listeners: a Unix socket in a 0700 directory at mode 0600 with a peer user check (`SO_PEERCRED`, `LOCAL_PEERCRED`); and the tailnet address only, never another interface, with a WebSocket framing of the same JSON-RPC messages for a mobile host. Types are generated from the schema. The roadmap builds the Unix socket first and the tailnet listener and the WebSocket framing with remote clients.
- Handshake: `hello`, then `welcome` with the highest version both sides speak that the build manifest's supported-combination table lists for the peer; capabilities are the intersection with that row. Otherwise `unsupported_combination` and close.
- Delivery: a table of recent `message_id` values with their first answer (retention (P): 24 hours). A repeat gets the first answer. An uncertain delivery is settled by asking for the outcome, never by resending.
- Streams: the envelope has no sequence field, so the payloads carry the stream mechanics. Topics are `conversation:<id>`, `running`, `progress`, `waiting`, `results:<conversation>`, `exchanges`, `accounts`, `channels` and `core`. Each topic has a gapless cursor `(epoch, seq)`, a ring of recent frames (P: 1,024), and on any gap a `resync` followed by a snapshot window (P: 200 items). Associative frames coalesce (appended text, replace-style rows). `item_done`, approvals and questions are never dropped or coalesced. A slow client overflows only its own topic with `resync_required`. Text deltas batch per 16 ms or 4 KiB (P). Heartbeat 15 s, client timeout 45 s (P).
- Approval decider: an answer to an approval or a question is accepted only from a client connection whose peer is not a descendant of Core by process ancestry. The socket path is not mapped into any sandbox. Remote clients are identified by Tailscale. The residual risk is another process of the same OS user that Core did not start. Acceptance test: a connect from inside the sandbox fails.
- RECOVERING: reads and subscriptions are served; mutations return `unavailable`.
- Details: the capability returns a typed document of the audit trail behind a card, result or answer. The activity log is a view over records Core already keeps.

The current Core increment implements only the local handshake, using the generated `kit/protocol` types. It creates missing socket directories at 0700, requires an existing parent to be a directory at 0700, and sets the socket to 0600 before accepting clients. The listener refuses an occupied socket path. Boot may first reclaim its own recorded stale socket under the C-03 rules. The first frame is a JSON-RPC 2.0 `hello` request with the client envelope in `params`; the response preserves the request id and carries the answer envelope in `result`. Frames are newline-delimited and capped at 1 MiB. The only supported protocol row is client 1.0 with no capabilities. An offer containing 1.0 receives `welcome` with the installed main id, the current durable epoch, `person-placeholder`, and empty agent and capability arrays; any other version offer receives `unsupported_combination` and the connection closes. A welcomed connection stays open until disconnect or shutdown; further valid requests receive `unavailable` in RECOVERING or `capability_not_negotiated` after READY. Malformed framing closes the connection. Peer credential checks, the full supported-combination manifest, event recording, deduplication and the remaining client operations belong to the later client-service implementation; this increment accepts no effects.

```go
type Hub interface {
	Subscribe(ctx context.Context, c Conn, t Topic, after *Cursor) (Snapshot, <-chan Frame, error)
}
```

## Conversation

**C-17 Ledger (changed).** Tables: `conv_event` (append-only), `ev_rel` (`view_of` with ordinal, `supersedes`, `pairs_with`) and `ev_span` (sensitivity per span). Fields are exactly those of [conversation-and-memory.md](conversation-and-memory.md#event-fields). `speaker_kind` is an argument of the admit call, set by the entry path (an `Entry` enum) and never parsed from text. `guard.Mask` runs before append: a secret-shaped value or key block keeps the flag, not the value. A body above the inline size (P: 16 KiB) goes to `materials/lina/` and the event refers to it; missing bytes mark `bytes_unavailable`. Only the Moirai write path and the ledger package write these tables.

**C-24 Agent as data (changed).** An agent is rows and files, not a process. Canon holds its row, its emotional state and the values of its blank settings. `versions/agents/<id>/` holds its persona layers as revisions: core, default voice, user voice, growth snapshot. Presets are persona material in `persona/<preset>/`. The first run creates LINA from the LINA preset and asks only for the ChatGPT sign-in. A preset update gives the next revision of core and default voice only. A preset methodology is short guidance in the core layer ([conversation-and-memory.md](conversation-and-memory.md#preset-methodologies)). Deletion follows [conversation-and-memory.md](conversation-and-memory.md#agents) and ends channel bindings at once. A persona write cannot change any grant or standing approval.

The `agents` store uses the fenced canon writer for agent rows, preset records, state, revision pointers, persona preparation acknowledgements and channel bindings. It saves each set of four layers as a Git commit in a private bare repository at `versions/agents/<id>/` before publishing its pointer. Immutable `refs/revisions/<commit>` retain saved versions; `HEAD` anchors the first version and canon selects the current one. Each immutable file is written and synced in a private temporary file beside its destination, then installed without replacing an existing file and followed by a directory sync. Opening the store removes abandoned temporary files; concurrent writers compare only fully published bytes. Unpublished objects after a failed write are never applied. `Prepare(turn, agent)` verifies the saved bytes and durably pins one revision to that turn; repeating it after a restart returns that revision, while a later turn selects the latest published revision. This is the persona part of preparation, not evidence that account, context or model-policy preparation succeeded.

Preset publication updates every extant agent of that preset in one transaction, replacing only core and default voice; per-agent state and the other layers stay intact. Direct revisions use an expected revision to reject stale edits and cannot replace preset-owned layers. First-run initialization is durable and idempotent: it creates LINA once from the embedded placeholder preset and returns only the ChatGPT sign-in requirement when no model account is usable. Deleting that default does not cause initialization to recreate it.

Agent trash hides its records from normal reads and new preparations, and removes its bindings in the same transaction. Restore is available strictly before 30 days and never revives bindings. Expiry records a content-free tombstone, removes persona/state/preparation records and the agent’s action history in the same transaction, and retries removal of the agent's repository. The deletion coordinator supplies time and propagates deletion to conversation and personal-knowledge stores when those stores exist. These store operations have no TUI or Core-protocol endpoint yet; package scenarios exercise real canon, Git revisions, restart and failure behavior. Account sign-in, the complete turn-preparation ledger and channel transports are composed by their owning flows.

**C-11 Preparation and assembly (changed).** Preparation runs the four steps of [conversation-and-memory.md](conversation-and-memory.md#preparation) in order and stops at the first failure with one plain line; the failing step is in Details. `Assemble(Input, Counter) (Request, Report)` is pure and lays out the seven items of the [request layout](conversation-and-memory.md#request-layout). `atropos.Packet` builds the ContextPacket once per turn under a time cap (P: 250 ms) and returns a minimal packet on timeout. Items drop as the request layout orders. The report holds ids, reason codes and digests, never text. The ledger records the request hash, the assembly list and the fidelity class of the provider that served it. The ContextPacket is never written into the history. For agent-runtime providers the same layers go in as one non-persisted session.

**C-08 Loop and run durability (changed).** `kit/loop` is the loop core of [runtime.md](runtime.md#conversation-loop): a stream function, a context transform before each request, hooks before and after each tool call, steering and follow-up input, cancellation through the turn, and parallel tool calls.

Commit points: the turn row and inbox claim in one transaction; the preparation acknowledgement; done output items verbatim; the assistant tool-call message before any call starts; a non-read intent before dispatch; each result when it ends. A visible partial is kept only as an aborted entry outside model context.

Rules, each testable with a scripted provider:

- A call outside the offered set returns `tool_unavailable`. A cut-off response runs no tool.
- Every call id gets exactly one result in call order (synthetic for aborted, invalid and denied).
- Calls of one response run in parallel as the contract says, at most 8 at once (P); two calls that write the same path run in call order.
- The idempotency key is built as [host-protocol.md](host-protocol.md#envelope) says, with the task id as scope, or the turn id for an effect asked in a conversation (CP-04).
- A tool deadline excludes the approval wait. A call that needs approval returns `awaiting_person` at once and runs its stored arguments, bound by hash, after the answer.

After a restart an accepted turn is never replayed ([runtime.md](runtime.md#failures-and-retries)).

The #55 increment implements `kit/loop.Run` over `provider.Stream`. It waits for a terminal event and a clean stream end before releasing any call, and reports `incomplete_response` for a cut-off call so a caller can request complete arguments without an automatic retry. Completed items are committed verbatim as they arrive; aborted visible deltas are stored separately and excluded from model context. Validation and before hooks finish in call order before execution. Independent calls use at most eight slots; normalized write targets form ordered dependency chains. Results commit as each call ends and enter model context in call order. Steering is drained at request boundaries, before the context transform; the admission lane owns follow-ups after the final boundary.

`turn.Store` installs its schema through a caller-supplied numbered canon migration after the inbox schema. Claiming an inbox row and inserting the turn are one fenced transaction, with one turn per inbox row; preparation commits before a model request. Journal failures stop dispatch or the next request. Startup calls `Recover` before admission: completed receipts survive, every open call receives an aborted result (including calls whose item committed before the response ended), and accepted turns cannot be claimed again. `turn.Tool` uses the existing registry and authority ledger for intent-before-dispatch and receipt commits. Approval-required dispatch returns `awaiting_person`; `ResumeApproval` uses only the approval's stored arguments with a fresh execution deadline. The earlier model result remains immutable; the later effect receipt belongs to authority.

These are package boundaries, with scripted Responses-adapter and SQLite reopen coverage. They add no conversation capability to Core's client protocol or the TUI. Protocol admission, application startup wiring, and result notifications attach in their consuming flows.


**C-09 Admission, steering, limits (changed).** All input enters one durable inbox, unique on (conversation, source, native id) and acknowledged when its row commits. One lane goroutine per conversation runs one turn at a time. Input during a turn is steering, applied at the next round boundary; input after the turn is a follow-up. Stop is a cancellation with a typed cause (`Interrupt{Issuer}`: person, watchdog, epoch_lost, grant_revoked, budget, shutdown); the text the model sees derives from the issuer. Limits apply only to chains that start without a person: continuations per input, exchange rounds (P: 4) and a scheduled budget. Exhaustion parks the input visibly and drops nothing.

The inbox table is installed through a numbered canon migration using `canon.InboxSchema`; admission uses the existing fenced writer. `inbox.Store.Admit` returns a receipt only after commit, with the stored row id and whether it was inserted. Repeated delivery returns the original id and preserves the original bytes, including across restart. Empty key fields are refused. Rollback, cancellation and commit failures return no receipt. `inbox.Insert` permits admission and a source cursor to share a transaction; its result is provisional until the enclosing write commits. A receipt acknowledges storage, not turn completion.

One `lane.Manager` per Core instance serializes submitted turns in each conversation's submission order while allowing other conversations to proceed. Each turn returns before the next starts, even after cancellation. Idle lane goroutines retire; shutdown rejects new submissions, cancels active turns, skips queued callbacks and waits for active work to return. An expired shutdown wait never means the turn stopped. The manager does not claim, replay or retry durable inputs; turn claims and steering stay at their respective flow boundaries. These packages currently have no client admission endpoint; the handshake-only client service remains as described in C-42.

**C-49 Turn budget and hand-off (new).** Values and meaning are in [runtime.md](runtime.md#turn-budget): acknowledgement 3 s, soft 6 round trips and 30 s, hard 10 and 60 s, tool deadline 15 s, all starting values. The design:

- `Meter.Start(turn, receivedAt)` returns a `Gauge` that counts round trips and tool wall time (model time excluded) and reports Under, Soft or Hard.
- At Soft, any call other than `start_run` and `dispatch_work` gets a typed `budget_reached` result and does not run. Tool declarations and the cached prefix stay unchanged.
- At Hard, Core ends the loop. With no effect made it calls `runs.StartFromTurn` with the input and the turn's tool outputs. With an effect made, the remaining reads and analysis go to a run and further effects wait on a question card. An effect with unknown outcome is never run again.
- A tool that can run long returns a handle. The budget decides only where work runs.
- A `turn_outcome` row per turn holds waiting time and quality counters (corrections, repeats, rework, "not what I asked"), filled by the reactions job of Lachesis. There is no automatic tuning.

**C-50 Background runs (new).** The sub-agent layer of [work-and-delegation.md](work-and-delegation.md#background-runs), built on the loop core.

- `runs.Start` returns in milliseconds with `working` or `waiting` (or `refused: budget`). A run is a goroutine in Core.
- Its tool set is built from the brief's allowlist and the run scope of [main-authority.md](main-authority.md#sub-agent-runs). Absent tools are not declared. Every call passes the Authorization checks.
- Concurrency is 4 per agent and 8 per person, then `waiting`. There is no time or call cap.
- A run ends only on its done condition, a real failure, or the person's stop. A real failure is no model or tool event for the stale interval, a dead process, or the same error repeating: one retry from the checkpoint, then an honest report. A checkpoint is written after each round trip.
- Done is checked mechanically where possible. The brief carries `Check{Name, Kind}` with kinds `citations_resolve` (a kit check), `tool_called` and `claimed`. Core, not the model, assembles the notice's account of what was verified and what was only claimed.
- Landing stores the artifact as a LINA material, resolves citations (an unresolved one lands the run partial), records the landing, and hands a notice to the conversation at a turn boundary ([conversation-and-memory.md](conversation-and-memory.md#background-results)).
- Long output is condensed like a conversation. A run is never ended for size.

```go
type Brief struct {
	Goal, Scope, NotTouch, Done string
	Checks []Check
	Result result.Type
	Allow  []ToolName
	Refs   []Ref // materials and memory projections, by id and revision
}
type Runs interface {
	Start(ctx context.Context, o Origin, b Brief) (RunID, State, error)
	Follow(ctx context.Context, id RunID, note string) error // steering at the run's next round trip
	Stop(ctx context.Context, id RunID) error                // idempotent; keeps what was gathered
}
```

**C-51 Reply shape (new).** [conversation-and-memory.md](conversation-and-memory.md#reply-shape) sets the budgets. This decision fixes the engine.

- `reply.Budget{Lines, Chars}` exists per reply type and surface, stored per person in canon as a setting.
- Counting: lines as rendered lines; characters as Unicode scalar values after NFC, spaces excluded for Korean; other languages use a ratio table (data).
- A streaming `Filter` counts the deltas. At the hard ceiling it stops showing the reply, ends the visible text at the last complete sentence, and creates a `document_draft` preview as the card. The ledger keeps the reply as written.
- Counters: ceiling breaches, and internal vocabulary, ids, hashes and paths in the default view (a word list per language as data; patterns for ids, hashes and paths). The check never judges wording. A rewrite before showing is not built.
- Tuning: the reactions job of Lachesis recognizes the person's reactions, and Core applies the contract's step as a setting change that records its evidence. The person edits budgets in settings.

**C-52 Typed results (new).** Contract: [host-protocol.md](host-protocol.md#typed-results), [conversation-and-memory.md](conversation-and-memory.md#results), [materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result).

- `kit/result` validates a body against the schema and size limits of its type and renders `Text` deterministically from the body, so no model writes `text`.
- Tools: `present_result`, `read_result` (returns the revision read) and `save_result`. What the model sees and the stale-revision rule are in the results section above.
- A revision is one ledger event with the whole result.
- Save is a Core effect with the key of `result_save` (CP-05); destinations are those of the saving section.
- Summary-first writing is a background run given the latest outline revision.

```go
type Result struct {
	ID ResultID; Rev int; Type Type
	Title, Summary, Text string
	Body Body // sealed: Summary, Table, Diagram, Chart, Draft, Comparison, Plan, File
	Status Status; SavedAs *Ref; RevisedBy ActorID; Prov Provenance
}
func Validate(t Type, raw json.RawMessage, lim Limits) (Body, error)
func PlainText(b Body, m Measurer) string
```

## Model path and accounts

Contract: [runtime.md](runtime.md#model-path). There is no model proxy. Four providers fall in two fidelity classes.

**C-43 Provider interface and fidelity (new).**

```go
// kit/provider: the only model boundary. Adapters never retry and never read the secret store.
type ProviderID string // chatgpt, claude, grok, gemini
type Class uint8       // ModelAPI, AgentRuntime

type Adapter interface {
	Provider() ProviderID
	Class() Class
	Models(ctx context.Context, l lease.Lease, c Credential) (ModelList, error) // display data, not entitlement
	Quota(ctx context.Context, c Credential) ([]QuotaRow, error)  // unknown is a value
	Stream(ctx context.Context, l lease.Lease, r Request) (Stream, error)
}
type Credential struct { // handed in for one call
	Account AccountID
	Kind    AuthKind // LoopbackOAuth, LongLivedToken, APIKey, ObservedCLILogin
	// The private bearer prints as a placeholder and only Authorize can apply it.
}
type Request struct {
	Credential Credential // supplied for this call, never read from a store
	Model   ModelRef
	Effort  Effort
	Items   []Item     // LINA's own item types
	Tools   []ToolDecl // declared order is kept
	Fields  FieldList  // request fields allowed for this provider, from the build manifest
	Purpose RunKind    // Turn, Background, BackgroundJob, Worker, WorkerSubthread, Auxiliary, Replay
	Bridge  ToolBridge // set for AgentRuntime providers only
}
type ToolBridge interface { // the runtime calls back into Core for every tool
	Call(ctx context.Context, c ToolCall) (ToolResult, error)
}
type Stream interface {
	Next(ctx context.Context) (Event, error) // Delta, ItemDone, ToolCall (from final items), Terminal
	Meta() Meta                              // request id, fidelity class, usage, runtime session id
	Close() error                            // cancel and join the request
}
type Usage struct{ Input, Cached, CacheWrite, Output, Reasoning *int64; CostEstimate *Money } // nil is unknown
type QuotaRow struct { // the fields of runtime.md#usage-and-cost; a nil value is unknown, a reset time is never invented
	Provider ProviderID; Account AccountID; Window Window
	UsedPct *float64; ResetsAt *time.Time; State QuotaState; Source QuotaSource; Observed time.Time
}
```

For AgentRuntime providers, Core carries out the list of [runtime.md](runtime.md#providers), with LINA's tools offered through `Bridge` as MCP tools. The ledger names the class per request.

#53 implements this boundary in `kit/provider`. Credentials are per-call, omit their bearer from JSON, and print only a placeholder; `Authorize` applies the bearer to the HTTP request without returning it. Model-list network calls also require a lease. The future secret-store adapter supplies these credentials. Items and tool declarations retain provider JSON so opaque state can round-trip without reconstruction.

**C-44 Responses and Gemini adapters (new).** Responses adapter: Go standard-library HTTP and a bounded SSE reader, `store` false, `stream` true, the request field list from the build manifest, `reasoning.encrypted_content` requested, and tools declared in the form the sign-in request surface requires (`apply_patch` as a custom tool or a one-string function). Done items come from `response.output_item.done` only, and tool calls dispatch from done items only. Current-turn items are never sent altered or dropped. [V1](runtime.md#runtime-acceptance) decides the open fields. Gemini adapter: the Go genai client, stateless requests, thought parts returned as received, and the same stream rules.

#54 implements `kit/provider/responses` with `net/http` and small JSON/SSE wire types. `responses.ReadConfig` reads `providers.chatgpt` from the build manifest through the transport-independent `responses/config` package, which Core's manifest reader also uses; the adapter rejects an absent required field, an unsupported field, a different adapter identity, or a nonzero retry count. `Request.Fields` may narrow that list but never widen it. It sends only the manifest-approved fields, keeps input items and tool declarations byte-for-byte and in order, and rejects system-role items. `apply_patch` is a custom tool or a function accepting one required string. Auxiliary requests reject tools and omit separate instructions. Every stateless request includes `reasoning.encrypted_content`, including when effort is empty and the model uses its default. A field list that excludes `include` refuses the request before sending; the adapter never silently loses reasoning state.

The HTTP request uses the supplied per-call credential and the fixed API origin, with zero retries and no redirects. The SSE reader handles comments, named events, multiline data and CRLF, and bounds each event at 32 MiB. A truncated event or `[DONE]` without `response.completed` is not successful completion. A lease covers the whole streaming request; `Close` cancels it and joins its goroutine. Deltas are display-only, `ItemDone` contains only the exact `response.output_item.done` item, and `ToolCallReady` events are held until a successful `response.completed`. EOF, malformed data, incomplete/failed responses and cancellation release no pending tool. A completed response's output array never manufactures a done item. Failures use recognized code, then type, then HTTP status; neither classification nor returned error text uses a provider message. Missing usage fields remain nil, including reported zero versus missing counts. Model lists are display data, and plan quota has no invented number or reset time.

Scripted wires establish these adapter invariants, and clean-build tests establish manifest production and validation. The adapter is not yet reachable through Core's conversation protocol or the TUI, so this increment has no observable scenario under `e2e/m1/`. It does not establish live OAuth, entitlement, or V1 acceptance of the provisional request-field list.

**C-45 Claude adapter process (new).** An external TypeScript program on the person's Node.js. Core starts it on first use of a Claude account and stops it after idle (P: 10 minutes), with concurrent turns per account limited (P: 2). The protocol is one JSON message per line on stdio.

- Core to adapter: `start` (system text, items, tool declarations, model, effort), `tool_result`, `cancel`, `ping`.
- Adapter to Core: `init` (tools, MCP servers and plugins the runtime loaded), `delta`, `item_done`, `tool_call`, `usage`, `limit`, `terminal` (reason, session id, cost estimate), `error`.
- Core refuses the turn if `init` lists anything LINA did not put there.
- First use installs the SDK from the release lockfile ([runtime.md](runtime.md#provider-adapters)); it is never shipped in LINA artifacts.

**C-46 Grok adapter (new).** A Go Agent Client Protocol client over the `grok` command on stdio, best effort. The steps are `initialize`, session create with LINA tools handed as MCP servers, prompt, updates, permission requests routed to Core's approval path, and cancel. Telemetry and trace upload are switched off explicitly. Account state comes from a cheap authenticated call, not from the command's files.

**C-47 Accounts, credentials and sign-in (new).**

```go
// internal/core/accounts
type Account struct {
	ID AccountID; Provider provider.ProviderID; Label string; Person PersonID
	Auth AuthKind; ClientID, HostID string; Scopes []string
	AccessExpires, RefreshExpires *time.Time
	Status Status; LastSuccess time.Time // OK, NeedsSignIn, NotEligible, UsageCapped
	Plan *PlanRef                       // read from the provider or command, or stated by the person
}
type Store interface {
	Get(ctx context.Context, id AccountID) (Account, error)
	Credential(ctx context.Context, id AccountID) (provider.Credential, error) // reveal site 1 of 2
	Replace(ctx context.Context, id AccountID, r Rotated) error                 // persist before use
}
type SignIn interface {
	Begin(ctx context.Context, p provider.ProviderID, r RelayCaps) (Pending, error) // PKCE verifier and state stay in Core
	Complete(ctx context.Context, id PendingID, in Relayed) (Account, error)         // code, state, iss; or error; or pasted URL
}
```

One refresh goroutine runs per account. Rotation, timing, keep-alive and failure follow [runtime.md](runtime.md#credentials), and the flows per provider are in [runtime.md](runtime.md#accounts-and-sign-in). The sign-in relay runs on the `client` connection ([integrations.md](integrations.md#consent-through-the-client)).

**C-48 Connected accounts, setup and routing (new).**

```go
// internal/core/routing
type Role string // "conversation", "background"
type Choice struct{ Account AccountID; Model ModelRef; Effort Effort }
type Policy struct {
	Rev   uint64
	Roles map[Role]Choice
	Work  WorkerPrefs // editable, provisional rule table
	Spend PayPerUse   // the budget the person set; zero by default
}
func Setup(accounts []Account, plans PlanCatalog, prev Policy, stated Stated) (Policy, Summary) // pure
type Router interface {
	Route(ctx context.Context, r Role) (Choice, Reason, error) // reads quota rows; moves new requests only
}
```

`Setup` is a pure function of the connected accounts, the plan catalog (data with a version and digest in the build manifest) and what the person stated. It runs on connect and on a plan change and returns the one-line summary. `Route` follows the quota-aware routing of [runtime.md](runtime.md#connected-accounts-and-automatic-setup) and moves new requests only.

**C-10 Failures, usage and budget (changed).** Failures are typed from the provider's own answer, in the order code, type, status. Message text is never parsed. Kinds: NeedsSignIn, NotEligible, UsageCapped, Unavailable, NotAccepted. Core and the model-API adapters retry nothing: the retry count is 0 and sits in the manifest ([runtime.md](runtime.md#failures-and-retries)). An agent runtime retries on its own, and Core records the retries it reports. Usage rows name agent, account, provider, run kind and run; unreported is nil. `budget.Reserve` and `Settle` cover every run kind the contract lists. At the limit the conversation stops sending and runs wait with their checkpoint.


#53 implements `budget.Reserve` and `Settle` inside a caller-owned fenced canon transaction. The caller installs `budget.Schema` in its migration. Reservations count every agent and all seven run kinds against one token allowance; settlement is idempotent for identical usage and rejects a rewrite. Each row retains agent, account, provider, run kind and run, with unreported usage encoded as null. Known input plus output replaces the reservation charge without double-counting cached or reasoning subsets; if either total is missing, the charge retains at least the reservation until usage is known. This charge is admission accounting, not reported usage. No Core protocol or TUI operation exposes the budget yet.

## Tools, approval and secrets

**C-12 Approvals, Dispatch and the effect ledger (changed).** Whether a call needs approval is decided as [main-authority.md](main-authority.md#plugin-tools) and [integrations.md](integrations.md#tool-classification) say: a call to a tool that a service profile marks, or an outward or irreversible effect of a built-in tool, asks every time, and every other call runs. A tool is `Spec{Name, Schema, Outward, Conc, Deadline, Placement}`, and `Registry.Dispatch` is the only way to run one and always returns. The effect ledger row holds the fields of the [plugin rules](integrations.md#plugin-rules), with the states of [host-protocol.md](host-protocol.md#effect-states). `kit/grant.Check` is the one implementation of the Authorization section for Core and Node, and it fails closed with a closed denial code per field. Workers on the main get a grant record with node id none, for audit and for stop and revoke; it does not confine the worker.

The dispatch implementation validates arguments with the Core-registered `Spec.Schema` before admission. Only the service profile supplies a plugin's `Outward` bit. `grant.Check` compares the grant identities, epoch, expiry, revocation, OS permission, live session, exact tool/target permission and revision with trusted executor observations. Empty optional identities mean none, not a wildcard. The effect claim and target revision reservation commit in canon before the handler runs; the result commits in a separate transaction. A key binds the validated argument hash, account, tool, target, task, person and agent. A repeated key returns the durable result even if the target revision has since advanced; changed intent under that key is refused. Concurrent or interrupted claims never execute twice. Stale writes record the current revision and the previous writing agent. A timed-out or interrupted effect is unknown, retains its target reservation, and requires reconciliation. The registry bounds the dispatch wait; an adapter still running retains its lease and slot until it exits, and a late success cannot overwrite unknown. Adapters must enforce the revision at the actual external write as well, because a canon reservation cannot exclude edits made outside Core.

These packages are callable Core services; the current connection-only client protocol has no tool or approval operation. Protocol/TUI scenarios are added when that surface is connected.

**C-13 Approvals (changed).** The policy is [main-authority.md](main-authority.md#approval). A request record holds what, where and undoable, computed by Core from validated arguments and never from model prose, with the choices, a scope key for grouping, the epoch, and the requesting agent and person. A request pending at restart is stopped (C-04). Answers are accepted as C-42 and C-27 say, and text never answers. An approval the person must answer is a row in the waiting list (C-55).

Approval admission now writes the Core-computed presentation and validated arguments to canon with an opaque request id. The fingerprint also binds the presentation, scope and undoability, so a changed plan cannot consume an earlier answer. `authority.Answer` consumes only a pending current-epoch request from its authenticated owner; the connection adapter supplies the decider facts, and text and Core descendants are refused. Duplicate or late answers are stale. On service startup pending and accepted-but-unused requests become stopped, while standing network grants remain durable. Once approval consumption and the effect claim share one transaction.

Only a built-in sandbox network request offers always allow. It creates a standing grant for the person, normalized target and scope key, covering every agent by default or the owner's explicit agent set. An empty named set grants nobody. One revocation persists the person and time and prevents the next admission, including an accepted request whose standing grant was revoked before dispatch. Plugin tools and outward effects never receive standing grants; channels offer only once and deny. When no connected surface can present the whole request, admission records a refusal instead of leaving an inaccessible pending request. The client and channel adapters must establish the decider facts and surface availability before connecting these services to protocol operations.

**C-14 Sandbox (changed).** The sandbox is that of [runtime.md](runtime.md#sandbox), invoked through `kit/sandbox`, with the seccomp program compiled per architecture at build. The probe runs at start and before the first call; if the sandbox is unusable, shell and file tools turn off with a cause and conversation continues.

- Network: a command a plugin profile names gets it by default, and asks first only when the profile marks it as outward or irreversible ([integrations.md](integrations.md#command-profile)). Any other asks once, with once, always allow and deny.
- First release: connected sources are mounted read-only for the shell. Edits to a person's files go through file tools with revision checks and the recoverable form of [materials-and-knowledge.md](materials-and-knowledge.md#editing-a-persons-files-in-place). Deletion by file tools goes to a trash.
- A local plugin server added by command gets its own sandbox instance, its data directory and only the allowance on its card.
- A shell write is recorded with changed paths and hashes before and after, found by a size-and-time manifest diff of the working directory.

**C-15 Untrusted text, masking, secrets (kept).**

- Masking follows [conversation-and-memory.md](conversation-and-memory.md#event-fields). Redaction runs at three seams: before ledger append, on final model-bound text, and on UI and channel egress.
- Tool results from plugins, the web and files are external evidence: user role, marked untrusted, with a fixed preamble and defanged delimiters.
- The secret store is that of [integrations.md](integrations.md#credentials-and-sign-in). Plaintext is revealed in two functions only (the environment builder and the header injector). Rotation is refused inside a turn.
- A pasted secret is masked and dropped. Keys enter only through account, plugin and channel forms.

Native content enters `guard.Admit` with an `Entry` chosen by the trusted adapter and a source locator. The adapter establishes the owner before choosing the owner entry; it never decodes entry or identity facts from the body. Admission fixes the speaker kind in an immutable message and refuses an unknown entry. Plugin results, web fetches and file reads have speaker kind `tool`. Forwarded content, external participants, agent exchanges and background notices also carry external evidence status, while keeping their distinct speaker kinds. External evidence without an origin is refused.

The message's `Evidence` view carries role `user`, an untrusted flag and the origin, with a fixed preamble and an `external_evidence` envelope. Both the body and the origin escape ampersands, angle brackets, quotes and square brackets, so source text cannot close the envelope or insert model-role delimiters. The original body remains data; model assembly uses the escaped view without decoding it. These labels and delimiters do not grant authority. No message, including owner text, is an approval answer: only the separate authenticated interaction path supplies `authority.Answer` with decider facts. A known approval id or a claimed owner in fetched content cannot consume a pending request.

Guard admission and evidence rendering are callable Core services. The current connection-only protocol exposes neither conversation content nor tool and approval operations; protocol/TUI scenarios accompany the future adapters that expose them. Imported history retains the separate provenance rules in [Event fields](conversation-and-memory.md#event-fields).

The masking/store increment (#48) supplies `internal/core/secrets.Mask` as the native-text admission boundary, within the issue's package scope. It replaces common provider-token shapes, credential assignments, bearer/basic credentials, private-key blocks (including an unfinished block), and caller-flagged secret spans. Its immutable `Masked` value contains only replaced text and `secret` spans using half-open UTF-8 byte offsets in that text. `Text` is the display/storage projection; `DerivedText` excludes the spans for indexes, summary inputs, ContextPackets and exports. Serialized records retain the flags and reject unmasked recognized credentials on read. Detection is heuristic: callers must pass explicit sensitivity spans for credentials without a recognized shape. The future ledger, assembly and egress paths must use this boundary; the current protocol still has no conversation capability.

`kit/secret.Secret` prints `[REDACTED]` through Go formatting, JSON/text serialization and structured logging, including nested values; it has no plaintext field that an unexported containing field can expose through formatting. Its only plaintext exits are `Environment` and `InjectHeader`, which validate the target field and reject invalid values. The caller still owns the destination grant and redirect policy. `secrets.Store` keys credentials by person, service and field, accepts an injected OS-keychain backend, and returns only the opaque type. `OpenFiles` is the explicit fallback when no keychain is available: a private state root, a 0700 `secrets/` directory, opaque filenames and atomically replaced, fsynced 0600 files owned by the current user, refusing links and unsafe modes. Core must hold its state-root lock for the store's lifetime. Backend errors expose no credential or path; a failing keychain does not trigger a fallback. Every active turn holds a store pin; mutation is refused until all pins are released. OS-keychain platform adapters, credential forms and turn/boot wiring are not exposed by this increment.

## Records and memory

**C-16 Canon file and writer (kept).** `canon/` holds the SQLite database. It has three classes of data: canonical rows; FTS tables written in the same transaction as their source; and model-written derived rows stamped with the revocation epoch. One writer goroutine owns one write connection. Readers open `query_only`. WAL and `synchronous=FULL`. No transaction spans a model, tool, Node or network call. Migrations are forward-only. The SQLite binding is chosen by [V2](runtime.md#runtime-acceptance) against four needs: FTS5, the online backup API, an optional sqlite-vec load verified against a manifest digest, and static builds. sqlite-vec is the optional embedding feature; with no embedding endpoint, recall uses FTS5.

The ordered application migrations are returned by `canon.Migrations`; table groups keep their schema in separate files. The asset tables hold stable ids and observed paths, immutable revision metadata, export delivery records and trash transitions. `assets` uses the existing fenced writer for all metadata changes and a read-only transaction for each query. File preparation and external delivery happen outside canon transactions; revision bytes are synced before their metadata is committed. A stale writer cannot replace existing revision bytes.

The registry retains assets at version 1 and appends `InboxSchema` at version 2. Identical-byte edits also enter the fenced write transaction and confirm the current revision and non-trashed state before succeeding. Asset reads can stream with size and SHA-256 checked at EOF, without holding the store mutex while the consumer reads; the byte-slice convenience API refuses content over its explicit 8 MiB limit. Closing a stream before EOF releases it without establishing integrity.

**C-53 Records repository and index (new).** Contract: [filesystem.md](filesystem.md#records-repository), [work-and-delegation.md](work-and-delegation.md#storage-of-records). One Git repository at `versions/records/`, behind an interface so the backend can change (a pure-Go Git library by default; the Git command as an alternative; thousands of files decide it).

```go
// internal/core/records
type Repo interface {
	Read(ctx context.Context, id ID) (Record, Rev, error)
	Commit(ctx context.Context, c Change, a Actor) (Rev, error) // Actor: person and asking agent; stale base is stale_revision
	Head(ctx context.Context) (CommitID, error)
	Rebuild(ctx context.Context) error // the index from the files
}
type Index interface{ Query(ctx context.Context, v View, p Params) ([]Row, error) } // today, upcoming, anytime, later, memos, board
```

- A write is: an intent row in canon; files written and committed with person and agent in the message; index rows updated with the new head; the intent closed.
- At start and before each write, Core compares the index head with the repository head. A mismatch rebuilds from the files, because files are canon.
- Core refuses a write to a record whose file changed under it, and shows an outside change as the contract says.
- The index has FTS over title and note, and edge rows for belongs-to and blocked-by. A status change runs a stale-mention query across records and fixes structured fields or flags the rest.

**C-54 To-do engine (new).** Fields, views and rules are in [work-and-delegation.md](work-and-delegation.md#the-to-do-record). Design:

- Views are SQL queries over the index, run by Core.
- Recurrence is Core's own parser for the subset (daily, weekly, monthly, yearly; interval; by-day; by-month-day; count; until) plus the from-completion flag. `Next(rule, from, zone)` advances only to a future occurrence. A table-driven corpus covers month ends and zone gaps.
- Reminders and pre-warnings are timer rows (C-26).
- Candidates are rows with a source, confidence and numbered handle. An automatic addition writes an undo handle to the undo log.

**C-18 Compaction and the summary tree (changed).** `compaction.Policy` is data with the values of [conversation-and-memory.md](conversation-and-memory.md#compaction-and-resume); the in-turn pass and the continuation turn follow the same section. The summary comes from an auxiliary request to the conversation model.

Tables: `sum_node`, `sum_edge` (child unique) and `sum_cover` (event ranges and skipped events with reason), with the node fields of the [summary tree](conversation-and-memory.md#summary-tree). Publication is one transaction that checks contiguous coverage from the start of the conversation, the expected frontier and the epoch. On summarizer failure nothing is published. `expand(node|event)` pages children or exact bodies across all of the agent's conversations. The summarizer gets its segment in a nonce-enveloped data block.

**C-19 Retraction (kept).** One transaction suppresses the targets, deletes index rows, invalidates derived rows by revocation epoch and advances it ([conversation-and-memory.md](conversation-and-memory.md#correction-retraction-and-deletion)). The removal list `backups/removals` is fsynced before the tombstone commits.

**C-20 Memory items (changed).** The table has the fields of [conversation-and-memory.md](conversation-and-memory.md#memory-items). There are no storage caps; limits apply only to the rendered standing profile (lines and tokens). The write checks of that section run at commit, and a failed check refuses with the reason.

```go
type RecallScope struct{ Agent AgentID } // sole constructor of recall candidates
func (s RecallScope) Holders() []Holder  // the agent and "all"; filtered before ranking
```

**C-21 Person model (changed).** The views are those of [conversation-and-memory.md](conversation-and-memory.md#person-model). `recall_person` at depth 1 makes one auxiliary request over at most N items (P: 12), with citations checked against the retrieved set and capped at 10 s. Deeper work is a background run. The person brief and the maintenance pass are jobs for `moirai-worker` ([cognition-and-life.md](cognition-and-life.md#memory-jobs)). Duplicate clusters inside one subject use normalized equality, token overlap and, when available, embedding similarity; never across subjects.

**C-22 Korean search (changed).** FTS5 with the trigram tokenizer for ledger and memory, plus a Hangul bigram posting table for words of two syllables (fallback: bounded scan). The acceptance set of the contract, by id, is a particle-attached three-syllable word and a two-syllable word, on the ledger (imported conversations included) and on memory. Each index records its tokenizer and fallback. Vectors are derived data in sqlite-vec when an embedding endpoint is configured.

**C-23 Skills and growth (changed).** Skills are `SKILL.md` folders in `versions/skills/`; built-in skills are embedded. The agent writes through a Core tool. A reversible-write check classifies a diff: only instruction text, no new executable file, and no tool, connection or permission in the front matter. A reversible write is adopted at once as a new version with a one-line notice and a one-action undo, and an undone content hash is not written again. Anything else goes to the explicit-acceptance path as a `summary` result and is built by the work engine ([cognition-and-life.md](cognition-and-life.md#skill-suggestions)). The catalog index holds each skill's name and description (its one-line trigger), and Atropos preloads at most three bodies by FTS over the descriptions (P). The prune and merge job and the persona growth step ([growth](cognition-and-life.md#growth)) use the same path.

**C-28 Importing a Hermes install (changed).** Contract: [conversation-and-memory.md](conversation-and-memory.md#importing-a-hermes-install), [filesystem.md](filesystem.md#importing-another-memory-system). It is a job for `moirai-worker`, started by the person, with no model call and deterministic classification. Package `importer` holds:

- snapshot readers (SQLite via the online backup API on a read-only connection; Honcho through an HTTP client whose transport rejects any method but GET);
- the schema gate and the maps the person confirms;
- the contract's ordered stages, one transaction per conversation;
- `legacy_ref(system, instance, kind, key)`, unique and written in the same transaction as its record;
- a reverse mapper whose canonical hash must equal the stored source hash, and a resolver for old ids;
- a secret scan with flags, an `import_sealed` event, and a backup generation at the end.

Fixtures are synthetic only.

## Plugins, channels and exchanges

**C-34 MCP host and plugin catalog (changed).** Contract: [integrations.md](integrations.md#plugins). `mcphost` uses the pinned official Go SDK and implements the [host](integrations.md#host), the [registry](integrations.md#registry) and the [OAuth flow](integrations.md#oauth-for-mcp-servers). Design points:

- The registry row stores a hash of each tool definition. A changed hash marks the tool on the plugin card, which shows the difference.
- Names are `<plugin>__<tool>` within 64 characters, with a hash suffix and a stored map. Declaration order is deterministic.
- A call waiting for input is parked as a background item with a question card and never holds the turn.
- Metadata fetches go through one dialer that refuses private, loopback, link-local and metadata ranges and redirects to them, with the contract's one exception.
- The catalog is a signed static file (JSON plus a detached signature, the key carried by the release), replaced only by a newer version with a valid signature.
- First-party GitHub: Core's own reads use `gh` as a child with fixed arguments (`ghclient`).

**C-27 Channel gateway (changed).** Contract: [integrations.md](integrations.md#channels), [main-authority.md](main-authority.md#channels). One adapter per platform sits behind the contract's boundary.

```go
// internal/core/gateway
type Adapter interface {
	Run(ctx context.Context, in Admitter) error // long poll or Gateway session; one per bot token, main only
	Send(ctx context.Context, l lease.Lease, o Outbound) (SendResult, error) // result id or error class
	Edit(ctx context.Context, ref MsgRef, o Outbound) error
	Answer(ctx context.Context, i InteractionRef, text string) error
	Fetch(ctx context.Context, a AttachmentRef) (io.ReadCloser, error)
	Caps() Capabilities // text length, edit, buttons, files, threads, streaming, result types
}
type Admitter interface{ Admit(ctx context.Context, e Inbound) error } // inbox row and cursor in one transaction
```

- The binding table, scope and pairing are those of [integrations.md](integrations.md#connection-binding-and-scope) and [integrations.md](integrations.md#identity-and-pairing).
- The cursor (the Telegram offset) is stored with the inbox row in one transaction, so the poll loop is LINA's code over a client library. Telegram comes first; Discord sits behind the interface.
- The outbound queue is per bot and chat and follows the [outbound rules](integrations.md#inbound-and-outbound-rules). A send of unknown outcome is not repeated.
- Approval buttons carry an opaque id and one letter. Core applies the checks of [main-authority.md](main-authority.md#channels) before it edits the message to the decision.
- A non-owner message is an `external_participant` event ([conversation-and-memory.md](conversation-and-memory.md#channel-conversations)).
- The progress digest is built by Core, and the adapter only sends it.

**C-25 Agent exchanges (changed).** Contract: [conversation-and-memory.md](conversation-and-memory.md#agent-exchanges).

- Table `exch_msg(delivery_id, exchange_id, from, to, state)` with the mailbox states of the contract. Delivery is at least once, deduplicated by delivery id.
- The asker runs the check it named on the typed hand-off.
- An exchange turn runs as a background run of Core for the answering agent, with persona, common knowledge and named materials, never personal knowledge. The round limit is 4 (P).
- The `needs_person` flag creates a waiting row. The log is a view over the ledger with one line per exchange.

## Work engine

Contract: [work-and-delegation.md](work-and-delegation.md). LINA ships no coding harness and re-runs no test or command. The development role is a method: define, choose, operate ([work-and-delegation.md](work-and-delegation.md#development-method)).

**C-29 Workers (changed).** `worker.Adapter` follows the contract's adapter boundary and is the only worker seam.

```go
// internal/core/worker
type Adapter interface {
	Name() string
	Version(ctx context.Context) (string, error)
	Capabilities() CapabilitySet                                           // declared at thread start and checked
	Start(ctx context.Context, l lease.Lease, s StartSpec) (Thread, error) // workdir, task packet, LINA work tools
	Input(ctx context.Context, t Thread, in Input) error                   // continue or steer
	Interrupt(ctx context.Context, t Thread, why Reason) error
	Events() <-chan Event // progress, usage, Finish (typed), Question, Approval, ToolCall
	Answer(ctx context.Context, t Thread, a Answer) error
	Quota(ctx context.Context) ([]provider.QuotaRow, error)
	Snapshot(ctx context.Context, t Thread) (ThreadSnapshot, error) // for reconcile
}
```

- Adapters: Codex over `codex app-server` on stdio (Go types generated for each verified version; exhaustive switches). Claude Code and Antigravity as headless children with streaming output. Devin through its session API, read about every 10 seconds only while a task is in flight.
- Workers run with the user's own setup and get no LINA sandbox, credential, hook or skill ([work-and-delegation.md](work-and-delegation.md#workers)). Approvals go to the originating conversation. A worker on another device is started and relayed by that device's Node.
- A process group kill is the stop. A cut turn's effects are unknown until `Snapshot` and a read of GitHub settle them.
- Work tools: for Codex, `lina` dynamic tools (submit receipt, ask, coordinate, read projection). For the others, the receipt is built from the final result and the pull request and marked adapter-derived.

**C-57 Work definition, choice and splitting (new).**

```go
type Definition struct {
	What, Why string
	Scope     []string
	NotTouch  []string // with the owner of each
	Done      string   // one condition a command or CI job can establish
	DesignRef []Ref    // contract and section
	WriteArea []Glob
	Branch    string
}
```

- The agent drafts a definition with the `dispatch_work` tool. Core validates required fields, glob syntax and that the design reference resolves in the repository at the base commit, and renders the task packet.
- Core does not split work itself. The dev preset carries the splitting criteria as guidance ([work-and-delegation.md](work-and-delegation.md#splitting-into-tasks)). Core checks mechanically that each task has one done condition and a write area, that overlapping areas are ordered by a dependency, that dependencies have no cycle, and that hotspot files are taken whole or moved to one task.
- `Chooser` ranks workers from situation tags, quota rows and host capacity, using an editable rule table marked provisional. A quota or login stop hands the same packet to the next worker.
- `Scheduler.Release` runs on events (landed, new task, quota change, a parked worker's reset), not on a poll. It starts every task whose dependencies landed, whose area lease is free and whose worker has quota. Area leases live from hand-off to landed, closed or abandoned. Concurrency starts at 4 per repository (P). A task that cannot start shows exactly one reason.
- One task, one worker, one worktree, one branch, created by Core with the Git command from a parent clone in `work/clones/`.
- The CRW pin is the start gate for the work ledger records: a pin record and a LINA fixture runner whose expected values come from the fixture ([work-and-delegation.md](work-and-delegation.md#start-gate)).

**C-58 Completion events, GitHub layer, review, merge (new).** Contract: [work-and-delegation.md](work-and-delegation.md#event-driven-flow), [work-and-delegation.md](work-and-delegation.md#judgment-and-delivery), [integrations.md](integrations.md#merges).

- A completion event is a row keyed by task, attempt and head SHA, and its claim is idempotent. Chat input and events share one reaction path.
- `Reactor.On` does, in order: one read of the pull request through `ghclient` (a `gh` child with fixed arguments); one design-conformance review; return of P0 and P1 findings once; merge or close; then `Scheduler.Release`. The CI watcher follows [completion signals](integrations.md#completion-signals).
- The review is Core's own call on the `background` role. The diff, the design reference text at the base commit and the work definition go in as an enveloped data block. Findings come back as schema-validated records `{id, severity P0..P3, file, claim}`. A failing required check and a change outside the write area are P1 findings computed by Core. A prose-only reply stores none. A large diff is reviewed by file group and merged into one finding set.
- The return, the close on a residual finding and the merge steps are those of the contract. Core takes the merge slot, merges pinned to the head last read, and only one read after the merge lands the task.
- Stall handling and the reconcile sweep (every 5 minutes (P), no model call) follow the contract.

**C-30 Task states and stage sources (changed).** Card states, `done` and verdicts are those of [work-and-delegation.md](work-and-delegation.md#progress-display) and [work-and-delegation.md](work-and-delegation.md#judgment-and-delivery). `Project(ledger) Card` rebuilds from the ledger at any time. Each stage summary carries a source label: worker report, GitHub, Core's own review, or unknown. Only Core writes verdicts (`verified`, `needs_changes`).

## Timers, progress and waiting

**C-26 Scheduler (kept).** The canon `timer` table keeps authored fields apart from scheduler-owned ones. One sleeper goroutine runs Core's own five-field and RRULE next-instant function in the timer's zone. A nonexistent local time fires at the first valid instant, and a repeated hour fires once. One transaction selects due timers, inserts a fire row unique on (timer, instant), advances next and commits; then it dispatches. Uses: reminders and pre-warnings, the briefing, expiry of exchanges and approvals, deterministic sensors (a model runs only when the signature changes) and jobs. At restart a claimed fire of an older epoch is replaced once, and only if its action is read-only.

**C-55 Waiting list and progress lines (new).**

- `waiting.Build` collects rows from approvals, questions, `needs_input` runs, blocked work, 할 일 the person promised or that are overdue with a deadline, requests awaiting a reply, exchanges flagged for the person, and outside-change problems. Each producer returns a row with kind, effort hint and an answer mapping. Core re-reads the source before showing.
- Order follows [surfaces.md](surfaces.md#waiting-for-me); lightest first is the alternative order. Home gets five rows and a count.
- `progress.Compute` builds one line per running 일 and 목표 per [work-and-delegation.md](work-and-delegation.md#progress-line): name, stage word and bar, just finished (the outcome line of the latest landing, built from structured fields and the title), next, and a waiting mark. Unobserved values are unknown, never zero. State words are codes; clients own the Korean and English catalogs.
- Both are computed on change events, coalesced (P: 250 ms), and pushed as row diffs on the `waiting` and `progress` topics. `Digest` makes the channel form.
- The design at a glance is a deterministic `diagram` result generated from plan data and the ledger, ordered by when each stage becomes usable.
- The roadmap builds the waiting list and the running list first. The progress lines, the digest and the design at a glance follow with the progress view.

## Replay and episode export

**C-59 Replay and export (new).** Contract: [runtime.md](runtime.md#replay-command), [conversation-and-memory.md](conversation-and-memory.md#episode-export).

- `lina replay <episode|turn-id> --candidates <dir> --out <dir>` rebuilds the request from the assembly list and runs the kit loop in the sandbox, as the contract says.
- Dependencies are swapped by interface. `Journal` and the effect ledger are discarding implementations, so a replay cannot write. Read tools run live; network reads and effect calls are answered by the recorded result; a turn with an unrecorded effect is excluded. Usage is a run kind of its own.
- `lina export` writes JSON Lines episodes and a manifest (counts, left-out reasons, revocation epoch) to `staging/export/`. Nothing is selected by default, only turns that start from a `person` event qualify, and secrets stay masked.
- The improvement loop that calls these commands is a development-time tool and is not in the product.

## Node

**C-35 Node (changed).** A separate static binary importing only the kit packages for grant, lease, sandbox, proc, protocol, rpc, diag and secret. It connects over the `node` connection, locally or on the tailnet.

- Device identity comes from Tailscale. Core asks the local daemon's API who owns the remote address (`DeviceIdentity.WhoIs`; a Linux implementation first, others at Node acceptance) and registers the device on first connection. Registration grants nothing. There is no key pinning.
- The Node state machine of [main-authority.md](main-authority.md#node-states) is table-driven.
- A write-ahead receipt journal is acknowledged by boot id and sequence. Every effect passes `grant.Check`, the OS permission and the live session. Commands dedupe by command id.
- Expiry is a duration the Node counts on a clock that includes suspend (candidates: `CLOCK_BOOTTIME`, `mach_continuous_time`); the numbers are Node acceptance values.
- The seat lease ([non-competition.md](non-competition.md#seat-lease)) is a grant naming a GUI session plus Node-side exclusivity.
- A receipt for an artifact carries provenance. Earlier-epoch receipts are read only while a Node is RECONCILING, matched on ids, and settle an effect as `reconciled`, not `receipted`.
- Noted for the Node phase, not yet in the contracts: LINA Core runs on the person's own computer by default, and other devices join as Nodes in one action. Nodes bid for work by free capacity, load, readiness and capabilities (a logged-in browser, a messenger app, a platform-only app, a GPU); work of a failed or vanished Node is offered to another. A Node keeps repositories only as a cache (clone on demand, worktree removed after merge), and for other work it gets read-only copies of the files it needs, fetched by content hash, and returns its outputs as result artifacts that Core files into the one canonical store. Credentials are granted per job and never copied into a Node's files. These rules are folded into the Node, work and storage contracts with the Node phase.

## Storage, backup and diagnosis

**C-39 Data placement (changed).** The areas are those of [filesystem.md](filesystem.md#state-root-and-layout). The Go owners: `canon` owns `canon/`; `ctlfiles` and `eventstore` own `system/`; `records`, `skills` and `agents` own `versions/`; `assets` owns `materials/` and `derived/`; `work` owns `work/`; `backup` owns `backups/`; `secrets` owns `secrets/`. Credentials are not backed up. Node execution state is outside the state root.

`assets` also owns import and export staging. Its materials implementation uses private directories and files, paths confined to an open state-root handle, random ids, increasing revision directories and SHA-256 hashes. Rename updates the observed path; edit preserves the id and older bytes. Its backup operation copies and verifies all recorded material revisions, including trash, into an unpublished generation supplied by the backup coordinator. The store has no TUI or protocol operation yet; package tests exercise real files and canon for identity, staging, export, trash and backup corruption. Version-space text, derived-data production, whole-generation backup, and coordinated permanent deletion retain their respective owners.

**C-32 Backup and restore (kept).** The procedure and the restore rules are those of [filesystem.md](filesystem.md#backup-generations). The manifest adds the epoch record at capture. Restore runs with the main stopped and never lowers the epoch. It also rebuilds the records index if its head differs from the repository, marks non-terminal external-effect rows unknown, voids leases and claims, and shows each account as needing sign-in. Zero-exposure test: forget an item after generation G, wipe the state root, restore G with the removal list, search by id.

**C-33 Diagnosis (kept).** The `kit/diag` emitter takes closed enums and opaque ids only, so a free-text, path or payload field cannot be written ([self-diagnosis-log.md](self-diagnosis-log.md#event-schema)). Gateway events are written under `conversation`. Events go to `system/events/` through their own writer, never the canon writer. Failure, unknown, denied and cancelled results commit synchronously. A chain root exists per boot, turn and task. A preallocated reserve lets a failure commit on a full disk. `diag.VerifyCitations` is the test oracle for the model, interruption and storage injections. Tokens, cost and latency live in the canon `usage` table, not in events.

The #41 package increment implements the closed emitter, strict twelve-field codec, boot/turn/task root constructors, query filters and citation oracle. `internal/core/eventstore` takes a caller-supplied `database/sql.DB` for a dedicated SQLite event database and a typed full-disk classifier. It requests WAL/FULL on one reserved connection, commits every append and releases a separately allocated reserve for one full-disk retry. [The diagnosis contract](self-diagnosis-log.md#storage) defines its ownership, query and reserve behavior. The SQLite binding remains #39 work; package and scripted SQL tests do not establish reopen/crash durability or an actual full-disk commit. Core startup, flow roots and the conversation query tool attach in their consuming issues; this increment changes no protocol or TUI operation.

## Contract change proposals

The owner direction is already in the contracts and is not a proposal. The Go design found five gaps. The owner accepted all five on 2026-10-20, and the text of each now lives in the contract named for it. The ids stay.

**CP-01. Accepted 2026-10-20.** [main-authority.md](main-authority.md#terms), Epoch: the main stores a new epoch durably before it dispatches, issues or accepts anything. C-04 takes the lock and opens the event writer first and dispatches nothing before the epoch is durable, as [runtime.md](runtime.md#recovery) says. The coordinator binds the event epoch only after the atomic epoch write and directory fsync and opens canon afterward.

**CP-02. Accepted 2026-10-20.** [host-protocol.md](host-protocol.md#envelope), `epoch` row: an unsigned 64-bit integer written as a decimal string without leading zeros and compared numerically. The epoch record, the sentinel, the cursors of C-42 and the fixtures use this form. The epoch file has no trailing newline; malformed or exhausted records fail closed instead of resetting the counter.

**CP-03. Accepted 2026-10-20.** [filesystem.md](filesystem.md#state-root-and-layout), `system/` row: LINA Core's control files live under `system/core/` (lock, epoch record, run sentinel, halt marker, restart request, main id), are written by LINA Core, are never in a backup generation and are left alone by a restore. The epoch must not fall after a restore, so it does not live only in canon. Orderly shutdown removes only the run sentinel and socket; it retains the epoch and the stable lock inode.

**CP-04. Accepted 2026-10-20.** [host-protocol.md](host-protocol.md#envelope), `idempotency_key` row: for an effect asked inside a conversation turn the task component is the turn id. The effect ledger scopes the key by task id or by turn id. `turn.EffectKey` hashes an unambiguous tuple of that scope, action/tool name, schema-normalized target and stable call id (the intent). The key excludes the boot epoch; authority binds the arguments and actor identity separately, returns a stored receipt for an identical key, and never repeats an unknown effect.

**CP-05. Accepted 2026-10-20.** [materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result): the idempotency key of a result save is built from the result id, the revision, the action and the destination, as [host-protocol.md](host-protocol.md#typed-results) says. C-52 uses the key of `result_save`.

**Open points at the Go level**, with the default in brackets: the SQLite binding (V2); the Git backend for the records repository (a pure-Go library, measured with thousands of files); the Telegram and Discord client libraries; whether `moirai-worker` reads canon directly or through the job API at scale; concurrent turns per Claude account (2); the Korean two-syllable method (a bigram table).

**Not designed here:** the LINA app (a private repository), the library viewers and meeting notes, the Google plugin, Jev, Klotho supervision and the QA map, the news feed, LIFE, the launcher, OpenViking, and the mobile host.

## Decision index

Titles are the headings above. Earlier ids C-01 to C-42 keep their number where the subject survives.

- Kept: C-03 to C-07, C-15, C-16, C-19, C-26, C-32, C-33, C-38, C-40.
- Changed: C-01, C-02, C-08 to C-14, C-17, C-18, C-20 to C-25, C-27 to C-30, C-34, C-35, C-39, C-42. Contract decisions replace the earlier ones. The main cases: the Moirai worker is a separate service, there is no hidden sandbox mode, no automatic replay, no automatic retries by Core, six tool classes and per-agent ceilings withdrawn, workers unconfined, survival tier withdrawn, key pinning withdrawn.
- Withdrawn: C-31 (confirmation list; the approval rule decides), C-36 (desk and work Mac; placement is not a contract), C-37 (model proxy instance; the proxy is removed), C-41 (placement across machines; parallel hand-off decides).
- New: C-43 to C-59.
