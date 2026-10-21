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

File tools use the explicit internal `lina file-tool --root <directory> --versions <private-directory>` subcommand, started with argv through `kit/sandbox` in a Core-owned process group. It handles one bounded JSON request on stdin and returns one typed reply on stdout. `cmd/lina` only registers `kit/tools.Main`; the implementation can later be registered by `lina-node`. This adds no shipped executable and is not a hidden re-exec mode.

The control-file increment (#36) starts with `lina core --state-root <path> --socket <path>`. The state root must already exist, and installation supplies `system/core/main.json`; Core never invents a replacement identity. `lina install` verifies the build bundle, creates a random main id in a new private state root, and registers the `lina-core.service` user unit with explicit state-root and socket arguments. Existing state and services are refused, including an install's own previous identity; installation is not an update or implicit restart. The concrete layout and failure behavior are specified in [runtime.md](runtime.md#the-lina-command).

**C-02 Binaries and modules (changed).** Subcommands of `lina`, proposed here because [runtime.md](runtime.md#the-lina-command) defers the names: `core`, `tui`, `install`, `backup`, `restore` (main stopped), `doctor`, `replay`, `export`, `about`, `version`. `version` prints the version, the commit and the build date that the build sets with `-ldflags` ([versioning.md](versioning.md)). Separate artifacts: `lina-node` and `moirai-worker`. Modules: the root module and `kit` (`github.com/thisisjun786/lina/kit`), joined by `go.work` and a `replace`.

The command registers `core`, `tui`, `install`, `doctor` and `version` in `lina`, and `version` in `lina-node`. Both `version` commands print the shared `internal/buildinfo` version line on stdout and exit 0 ([versioning.md](versioning.md#what-a-binary-reports)). Missing or unknown subcommands print usage on stderr and exit 64. The install handler dispatches into `internal/core/install`; failed installation exits 78 and preserves existing files and services. Node depends on the permitted kit packages and the Go standard library; its command entry point may also import `internal/buildinfo`.

`lina doctor [--state-root <path>]` diagnoses the installed identity, control records and canon without starting Core or writing installed state. The default root matches `install` (`XDG_STATE_HOME/lina`, or `~/.local/state/lina`). A healthy installation prints `OK` and exits 0; a corrupt or refused installation names the failed component and plain cause on stderr and exits 78. Invalid arguments exit 64. Doctor checks a private temporary copy of the database and committed WAL, including SQLite integrity and schema compatibility, then verifies that the source files are unchanged; concurrent changes require a retry. The temporary copy is removed. An empty canon directory is valid before first use. Doctor never mints an epoch, creates a lock or socket, migrates canon, repairs corruption, or clears a halt marker.

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
| L4 | Flows: `lane`, `assemble`, `compaction`, `turn`, `runs`, `reply`, `results`, `modelpath`, `exec`, `exchange`, `gateway`, `worker`, `reactor`, `review`, `ghclient`, `progress`, `waiting`, `backup`, `update`, `recover`, `clientsrv`, `jobs`, `mcphost`, `install` |
| L5 | `boot`, `supervise` |

The separate worker has its own downward import layers. Core may import only its `jobapi` contract, never its reader or runtime; the worker imports no Core, provider, secret, process-launch or arbitrary third-party package. The SQLite binding is allowed only in the exact read-only reader package. The ordinary architecture test enforces these boundaries.

| Worker layer | Packages |
| --- | --- |
| W1 | `internal/moirai/jobapi` (credential-free types and Unix-socket client) |
| W2 | `internal/moirai/canon` (read-only job snapshots) |
| W3 | `internal/moirai` (job execution and scheduling) |
| W4 | `cmd/moirai-worker` (command and signals) |

Outside the Core layers, `internal/buildinfo` owns the linker-supplied version, commit and UTC build date and the embedded VCS fallback. It imports only the standard library and is shared by the two command entry points.

Rules: imports run downward. L3 stores never import each other; L4 composes them inside one `canon.Writer.Do`. `assemble` is pure. `kit` imports nothing from the root module. `internal/node` and `cmd/lina-node` import only the standard library and the kit packages listed for Node, with one exception: `cmd/lina-node` may import the exact package `internal/buildinfo`. This exception does not extend to Node runtime packages or buildinfo subpackages. Charm libraries appear only in `tui/term` and `tui/ui`. The Markdown adapter in `tui/ui` may also import the pinned Goldmark root, `ast`, `text` and `util` packages to supply semantic spans; other TUI layers and other Goldmark subpackages remain excluded ([tui.md T-01](tui.md#boundaries-and-trust-t-01)). `os/exec` appears only in `kit/proc` and `kit/sandbox`, and `os.Environ` only in `kit/proc`. Secret reveal happens in two functions. The default-view types live in a `view` package with no id fields. All of this is one ordinary `go test` (`internal/archtest`) that the components check of [foundation.md](../policy/foundation.md) already runs. It adds no gate.

**C-56 Moirai worker boundary (new).** [runtime.md](runtime.md#processes) and [cognition-and-life.md](cognition-and-life.md#request-path-and-background) make `moirai-worker` an optional separate Go service. It opens canon read-only, holds no credential, and talks to Core over a 0600 Unix socket in a 0700 directory at the path the install sets ([runtime.md](runtime.md#deferred) leaves socket paths to packaging). It asks Core for two things: a model call on Core's path under the budget and the run kind (`Aux`), and a write through the Moirai write path (`Apply`), which checks the epoch fence and the write rules. Each job has a schedule, a budget, a switch and an adoption record, is idempotent, and keeps a watermark. Killing the worker leaves canon unchanged. Without the socket only background memory and planning are unsupported. A summary on demand runs in Core, so conversation never depends on the worker. The release manifest pins the worker with Core, so this socket has no version negotiation.

The job API increment exposes `Aux`, `Apply` and `Report` over bounded HTTP/JSON requests on that Unix socket. Listening requires the held Core state-root lock, kept for the listener lifetime. Under that lock a restart may reclaim an existing 0600 socket only when a connection probe returns `ECONNREFUSED`; a live listener, uncertain probe, non-socket path, or non-private socket/directory is refused without changing that path. The types and client live in `internal/moirai/jobapi`; `internal/core/jobs` owns the service. Core registers each job's model callback, account binding and transactional write-rule callback. Neither SQL, credentials, provider selection, tools nor persona cross this API. A Core schema owner includes `jobs.Schema` and the shared budget schema in its numbered migrations; neither serving the API nor starting a worker creates or migrates canon. Concrete memory/planning algorithms and installation remain their own roadmap increments.

Core owns a job's kind (memory or planning), interval, deadline (at most two minutes), token allowance, maximum tokens per call, on/off switch and adoption record. It enqueues immutable, consecutive source tasks. The worker reads the epoch, policy, committed watermark and next task in a single `mode=ro` / `query_only` snapshot. Its reader exposes no writer. Jobs have separate deadlines and run independently; a slow or failed job does not hold up another job or the conversation path.

`Aux` binds the source task to an auxiliary model call, reserves both the job allowance and shared main budget, and durably caches the result. Missing usage retains the reservation charge. A disconnected worker does not cancel Core's admitted, bounded call, so a restarted worker can recover its result without another model request. If Core stops before recording a result, the durable reservation reports an unknown outcome and is not automatically retried. This accounting and the cached auxiliary result are Core-owned bookkeeping, not an adopted memory or plan.

`Apply` validates the epoch, job switch, schedule and expected watermark, then invokes the registered write rules. The accepted changes, next schedule, proposal receipt and watermark commit in one fenced canon transaction. Failure rolls all of them back. An identical proposal replay returns the receipt; a changed proposal at the same sequence conflicts. Core persists an explicit `Deterministic` job-policy flag in the read-only work snapshot. When true, the worker submits the Core-supplied task input as the proposal directly to Apply; no model callback, account binding or model allowance is required. Core refuses Aux for that job. When false (including older records without the flag), the worker obtains model output through Aux before applying; it never infers the route from a zero budget. Killing a worker cannot write canon or advance its watermark; a Core transaction already committed remains committed. Core's fenced apply helper is also the only mutation boundary for job administration, Aux accounting and Report. Reports carry only a closed status and the current watermark; they cannot advance progress or change policy. The worker has no credential-loading path, clears inherited environment variables at entry, and never contacts a provider.

```go
// internal/core/jobs: what moirai-worker may ask of Core.
type API interface {
	Aux(ctx context.Context, r AuxRequest) (AuxResult, error) // model call on Core's path
	Apply(ctx context.Context, p Proposal) (Applied, error)   // the only write path; epoch-fenced
	Report(ctx context.Context, j JobID, s JobState) error    // status at the committed watermark
}
```

## Start, recovery, idle and update

**C-03 Lock, control files, epoch (kept).** Core takes a non-blocking `flock` on `system/core/core.lock`. The files under `system/core/` are the lock, `epoch` (a decimal string, CP-02), `run.sentinel`, `halt.json`, `lifecycle.json` (the restart request) and `main.json` (the main id; no key). Data records are written by temp file, fsync, rename and directory fsync, at mode 0600 in a 0700 directory. The lock is the exception: it is created once and fsynced with its directory, then opened and flocked without replacement or unlinking, so all contenders use the same inode. None is in a backup generation, and a LINA restore preserves them except for clearing the halt latch after verified recovery (C-04, CP-03). A state root on a non-local filesystem is refused. The new epoch is `max(epoch file, highest epoch in canon by read-only peek, peer_seen_max) + 1`, fsynced before canon opens (CP-01). `peer_seen_max` is the highest epoch a peer reported when it refused a stale one. The epoch fence is the first statement of every write transaction. A lower epoch fails `stale_epoch` and latches the writer.

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

At step 8 the client service accepts handshakes in RECOVERING. Mutations return `unavailable` until reconciliation completes. Negotiated read and subscription handlers remain available during RECOVERING. READY admits the operations implemented by that client service; the default Core offers lifecycle when it is negotiated. A halt marker keeps Core in RECOVERING and is never cleared by startup or shutdown. The previous sentinel, halt marker and restart request are passed to reconciliation without silently discarding invalid records.

Missing or invalid command arguments exit 64; lock contention exits 69; invalid state, corrupt control records or socket startup failures exit 78. SIGTERM and SIGINT stop accepting clients, close accepted connections (including incomplete handshakes), remove the owned socket and exit 0. Shutdown closes the socket service, canon and event writer in reverse acquisition order before releasing the lock. Each service receives a five-second shutdown deadline and must honor it. An orderly shutdown removes the sentinel and preserves the epoch, main id, halt marker and restart request. Startup failure unwinds acquired resources, retains any minted epoch and leaves the sentinel for the next recovery.

A socket can accept a connection before its ownership record is published or its first handshake is served. Cancellation in this bind-to-publication interval finishes retiring the previous socket and publishing the new ownership record, then performs orderly shutdown, including sentinel removal. Cancellation before bind remains an interrupted startup and preserves recovery records with a nonzero exit. A publication or cleanup failure also remains nonzero; the CLI never turns a startup error containing cancellation into a successful exit.

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

Halt: `SQLITE_CORRUPT`, `NOTADB`, or an I/O error whose read-only reopen fails writes the halt marker. The process stays in RECOVERING, serves reads and `doctor`, issues no grants, starts no adapters and fails writes with `store_write_halted` (a client sees `unavailable`). The standalone `doctor` command remains available during RECOVERING and when startup refuses before binding a socket. Only a verified whole-generation restore clears it. Disk full is not a halt. Shutdown runs the start stack in reverse with a cap per phase.

**C-05 Idle and leases (kept), C-06 Restart and update (kept), C-07 Hot reload (kept).** Idle is decided by effects, not chat silence: Idle, Busy(reasons) or Unknown, and Unknown counts as busy.

- Hard, blocking: a tool call or send with an effect in flight, an open Node command, a worker turn, a migration or update, a restore.
- Live, waited for and never ended by timeout: a streaming model request, a read-only tool.
- Reported, not blocking: queued input, timers, background runs (they resume once), derivation, backups.

Every kit call that goes outside takes a `lease.Lease`, so a missing lease does not compile. A restart request is a durable record in `lifecycle.json`. `when_idle` is the default and `drain` is explicit. A restart is never forced by timeout; only the person may force it. Exit codes: 0 stop, 75 planned restart, 78 fatal, 69 lock held. Settings, persona, bindings and plugin lists change by control verbs and never restart Core.

The #37 lease API is `Acquire(ctx, lease.Activity{Class, Reason}) (lease.Lease, error)`. Classes are `Hard`, `Live` and `Reported`; an unknown class or empty reason is refused. Kit adapters carry the lease in their typed API and enter external work through `lease.Do(ctx, lease, call)`. Its missing-argument fixture must fail compilation, and its companion with a lease must compile. Zero and released leases are rejected before the callback runs. A lease is an activity record, not an authorization grant. Leaser implementations register activity before constructing its handle with `lease.New` and supply the callback that removes it.

Lease copies share one lifetime. `Release` is idempotent, prevents new calls and removes the activity only after every call already inside `Do` has returned, including error and panic paths. Context cancellation never removes an activity by itself. Callers release leases when their work finishes; a call still running remains blocking even after its owner requests release. The kit neither retries the call nor forces completion on a timeout.

`lifecycle.NewRegistry` combines local leases with an optional reader of caller-owned activity. A nil reader means all activity is represented by local leases, with no implicit state path. A configured reader must return a consistent snapshot and honor cancellation; a failed read, missing record, invalid activity, canceled read or uninitialized registry yields `Unknown`. `IdleReport.Busy()` treats every state other than `Idle` as busy. Hard and Live entries appear in `Blocking`; Reported entries appear in `Reported` and do not prevent idle. Reports are independent copies ordered by class and reason, and duplicate activities remain separate until each lease is released. The reader's error is retained in the report.

`Edges` is a buffered, coalescing wakeup for one lifecycle observer, which reads `Idle` again after a notification. Lease acquisition and completed release notify automatically; the owner of an additional activity source calls `Notify` after that source changes. Notifications never wait for the observer.

```go
type Registry interface { lease.Leaser; Idle(ctx context.Context) IdleReport; Edges() <-chan struct{} }
type Fence interface { Prepare(ctx context.Context) (Token, IdleReport); Commit(Token) error; Cancel(Token) }
```

`ActivityRegistry` implements the fence with the same mutex as lease admission. `Prepare` closes admission before inspecting activity and returns a token owned by that attempt and registry, including while Busy or Unknown. Existing leases remain usable until their owners finish; releasing one during an external call still waits for that call. `Commit` rereads activity, requires known Idle and a live preparation context, and permanently closes admission for shutdown. `Cancel` reopens only its matching uncommitted attempt; a stale or foreign token cannot cancel another fence or a committed shutdown. Caller-owned activity sources must also enter new external work through this registry, so their activity snapshots cannot bypass admission.

`lifecycle.OpenRestarter` binds the locked control-file store, current durable epoch and shared registry. Its request defaults to `when_idle`, which cancels an unsuccessful preparation and continues admission until the next activity edge. Explicit `drain` retains the preparation and waits with admission closed. Neither mode interrupts Hard or Live work, or treats Unknown, a deadline or cancellation as permission to exit. A duplicate pending request with the same mode returns its original receipt; a different mode is refused instead of silently escalating it. A failed control-file write latches the controller and cannot acknowledge a request or authorize a planned exit.

The version-1 `lifecycle.json` object has `schema: 1`, a random 32-digit lowercase hexadecimal `id`, `mode` (`when_idle` or `drain`), and the requesting `epoch`. Optional `committed_epoch` records a successful idle fence; optional `discharged_epoch` records a later Core epoch reaching READY. Epochs use CP-02 canonical positive decimal strings. Commitment cannot predate the request and discharge must follow commitment. Unknown fields, duplicate keys, null values and malformed or inconsistent records are refused. Writes use the private atomic control-file path and never mint an epoch.

An unknown lifecycle schema version is corrupt control state: Core exits 78 before minting an epoch and preserves the record. A kill after durable commitment and before the replacement's READY leaves the request owed; merely opening the next Core epoch never discharges it. The SIGKILL boundary test covers this interval. Neither mode forces progress on a timeout.

A request is **owed** until discharge, including after a crash before commitment or before the replacement Core reaches READY. Startup reconciliation calls `Ready` only when it actually reaches READY: a request committed by an earlier epoch is then durably discharged, while an uncommitted request remains owed. After a successful fence and durable commitment, the controller requires orderly Core shutdown and exit 75. The discharged receipt stays in `system/core/lifecycle.json`; backup generations never contain it, and restoring canon neither recreates a request nor replaces that receipt. Settings, persona and binding mutations apply through their existing stores without requesting restart or minting an epoch. Command and client composition must share the controller's registry with every external call; package-level idle alone does not establish that a running Core is safe to restart.

Boot constructs this controller under its existing lock, validates the restart record before minting, and supplies it to the client service. `Config.Activities` is the shared registry for composed effects; an omitted registry represents the current Core with no external calls. Startup never discharges a request while halted or before reconciliation and sandbox readiness finish. The observer is joined before stores close. Planned restart follows the same reverse shutdown as stop, removes the sentinel, and returns exit 75 only if every cleanup succeeds; cleanup failure retains recovery state and exits 78. The client service negotiates `lifecycle` 1.0 for `restart_request`, `restart_get` and `restart_status` ([host protocol](host-protocol.md#client-connection)). A Live lease holds the request acknowledgment open until its write completes or the client disconnects, preventing an idle restart from closing its own reply.

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

The topic hub assigns sequence numbers only after coalescing, independently for each topic. Adjacent text deltas for the same item append; adjacent `running_update`, `progress_update` and `waiting_update` rows for the same key replace. A different item or kind, or any non-associative event, ends the pending batch. `item_done`, approvals and questions always keep their own frames. The text batch ends at 16 ms or 4 KiB of text, whichever comes first. Capturing a subscription also ends a pending batch under the publication lock, so its snapshot cursor and the first live frame have no gap.

A subscription without a cursor gets the latest snapshot window. A cursor from another epoch, ahead of the topic, or older than the retained ring gets `resync` followed by that window. Otherwise, replay starts strictly after the cursor and precedes the live queue. The ring retains 1,024 committed frames and the snapshot retains 200 materialized items by stable item key, including accumulated text rather than only its last delta. Each subscription has its own bounded live queue (P: 256 frames). Overflow closes that subscription with a retained `resync_required` cause; other topics and other subscribers of the same topic continue. The cause is separate from the full queue. Snapshot and replay values are independent copies. The hub is in memory for one durable Core epoch; it is not conversation persistence or a substitute for reading older history.


The additive client 1.0 stream payloads implement this boundary. `subscribe` optionally names `topic`, an `after` cursor, and `active` (omitted means true; false unsubscribes and cannot carry `after`). Omitting `topic` preserves the previous reserved request shape. A fresh subscription answers with `snapshot`; an unavailable cursor answers with `resync` and then sends a `snapshot` notification. A retained cursor answers with `subscribe` carrying `active: true` and the replay endpoint, followed by the replay and then live notifications. An unsubscribe answers with `active: false`. Re-subscribing replaces only that connection's subscription to the named topic.

A cursor payload is `{epoch, seq}` with the CP-02 epoch string and an exact unsigned 64-bit JSON integer sequence. Stream events retain their existing kind and payload, adding optional `stream: {topic, cursor, key, revision}` metadata; previously reserved display events also accept optional `text`. Revisions identify the item's last publication. A snapshot carries `topic`, `cursor`, `revocation_epoch` and `items`; each item carries its kind, stable key, revision and that kind's validated payload. `resync` and `resync_required` carry only the affected topic and current cursor. Mechanics never add fields to the envelope. Replay keeps its logical message id, derived from main, topic and cursor, and uses a new attempt id. Snapshots replace a window, and their revocation epoch comes from the owner of the projection.

`Identity.Topics` connects the hub to the local listener. `TopicCapabilities` registers an exact topic or the `conversation:` / `results:` family with an existing capability. A subscription requires that capability in the peer's negotiated manifest row, and all the listener's sender and epoch checks apply first. Reads remain available during RECOVERING. The default source-build table grants only lifecycle; installing a projection and a matching evidenced row is the owning product service's responsibility. No test-only publication operation is exposed to clients. The M1 stream scenario runs the production listener and generated codec with synthetic projections through the shared isolated-socket harness. A single publication and every row in a committed group are validated through the generated frame and snapshot codecs, including Core-assigned stream metadata, before changing the ring, snapshot or sequence. A committed publication group is atomic with respect to snapshot and queue readers; its tests can therefore force overflow without scheduling luck. Physical writes share a serialized writer and the 1 MiB framing limit, and are bounded by cancellation or the 45-second timeout. Shutdown closes the connection and joins its subscription workers.

The local listener uses the generated `kit/protocol` types. It creates missing socket directories at 0700, requires an existing parent to be private and owned by Core's OS user, and binds the socket at 0600 before accepting clients. The listener refuses an occupied socket path. Boot may first reclaim its own recorded stale socket under the C-03 rules. Kernel peer credentials must match Core's user before any frame is processed. Linux uses `SO_PEERCRED`; the Darwin credential adapter uses `LOCAL_PEERCRED` and `LOCAL_PEERPID`, while private socket binding remains Linux-only in this increment.

Frames are newline-delimited JSON-RPC 2.0, capped at 1 MiB including the newline; EOF without that newline is not a message. The first request is `hello`; its response preserves the JSON-RPC id and carries the answer envelope in `result`. Lookup matches the connection, hello peer and platform, and exact protocol version against an evidenced supported-combination row, chooses the highest common numeric version, and intersects client offers, the row and installed server handlers. Product version never decides compatibility. The current codec speaks only client 1.0. Boot accepts a `supportedCombinations` build-manifest section; a missing or malformed explicit table fails startup and an empty table refuses all combinations. The compiled default and build-manifest writer share one table: connection `client`, peer `lina-tui`, the build target as `GOOS/GOARCH`, protocol `1.0`, capability `lifecycle` version `1.0`, and evidence from protocol conformance and the planned-restart scenario. The optional string fields `peer` and `platform` are a minor-compatible addition to the client 1.0 hello schema. Missing, empty or unlisted values receive `unsupported_combination` and close; Core never guesses them from product version or its own platform. The TUI supplies its compiled target. A future app or remote platform requires its own evidenced row.

Boot supplies the durable main id and epoch and can supply the installed person, agents and operation handlers. Until installation connects those services, the source-build welcome retains `person-placeholder` and an empty agent array, and grants lifecycle only when offered and installed. After welcome, sender identity is bound to the connection and a different epoch is refused `stale_epoch`. RECOVERING rejects every mutation with `unavailable` before decider or capability dispatch, while negotiated read and subscription kinds remain available. Sender and epoch validation still run first. An unnegotiated operation is refused `capability_not_negotiated`; a valid welcome stays open until disconnect or shutdown. Approval, question and waiting-list answers also require an eligible decider: Core itself, its children and grandchildren, unreadable ancestry and a changed process identity are refused `not_authorized`. The initial ineligible verdict survives reparenting and eligibility is rechecked before each decision. Deduplication, event recording and the remaining client operations are separate client-service work.

```go
type Hub interface {
	Subscribe(ctx context.Context, c Conn, t Topic, after *Cursor) (Snapshot, <-chan Frame, error)
}
```

## Conversation

**C-17 Ledger (changed).** Tables: `conv_event` (append-only), `ev_rel` (`view_of` with ordinal, `supersedes`, `pairs_with`) and `ev_span` (sensitivity per span). Fields are exactly those of [conversation-and-memory.md](conversation-and-memory.md#event-fields). The admit call takes the trusted entry path (a `guard.Entry` enum), from which `guard.Admit` fixes `speaker_kind`; it is never parsed from text. The shared `secrets.Mask` runs before append: a secret-shaped value or key block keeps the flag, not the value. A body above the inline size (P: 16 KiB) goes to `materials/lina/` and the event refers to it; missing bytes mark `bytes_unavailable`. Only the Moirai write path and the ledger package write these tables. Provider-item JSON is masked at decoded string boundaries and serialized safely; both the persisted body and its derived history projection retain valid JSON, including nested JSON strings with quoted credentials.

The #57 increment installs all three tables through `canon.Migrations`. SQLite triggers refuse updates, ordinary deletes and replacement inserts, including on relations and sensitivity spans. The explicit person-authorized erasure path remains a separate operation to implement; this store exposes no deletion bypass. `ledger.Store.Admit` commits the event, its relations and its spans in one fenced transaction and returns an event only after commit. A repeated event id fails without changing the first event. Parent references must exist in the same conversation; replay, scaffold and relation targets must already exist. Scaffold references name an event that stores the scaffold once, with the same scaffold kind.

Native origin records retain the entry and source locator so readback preserves the distinction between ordinary agent speech and an external agent exchange, or between an ordinary system line and a background notice. Only person entries carry `person_id`; every native event names the conversation's agent. UTC timestamps preserve the supplied occurrence, source-recording time and time-quality class. Secret spans address the original input before masking. Nonsecret tags address the replaced text; all stored offsets therefore describe only the masked body. Secret-shaped metadata is refused instead of silently changing ids or source locators.

Core's composition layer supplies the ledger's material interface from the assets store, without an import between L3 stores. It opens one `assets.MaterialWriter` before admission, which reconciles markers from interrupted preparations. Preparation syncs a marker and the masked body without retaining asset metadata; `PreparedMaterial.Insert` inserts that metadata in the same canon transaction as the event. Closing the preparation after the transaction checks committed canon, removing uncommitted bytes and preserving committed revisions. An interrupted cleanup leaves its durable marker for the next opening. Cleanup cannot revoke an event that committed. The threshold applies to masked UTF-8 bytes. Reads verify the immutable material revision; missing or corrupt bytes return event metadata marked `bytes_unavailable`, with no complete message. Real SQLite and asset-store scenarios, including abrupt process exits at publication boundaries, cover these behaviors; the increment adds no conversation capability to Core's protocol or the TUI.

**C-24 Agent as data (changed).** An agent is rows and files, not a process. Canon holds its row, its emotional state and the values of its blank settings. `versions/agents/<id>/` holds its persona layers as revisions: core, default voice, user voice, growth snapshot. Presets are persona material in `persona/<preset>/`. The first run creates LINA from the LINA preset and asks only for the ChatGPT sign-in. A preset update gives the next revision of core and default voice only. A preset methodology is short guidance in the core layer ([conversation-and-memory.md](conversation-and-memory.md#preset-methodologies)). Deletion follows [conversation-and-memory.md](conversation-and-memory.md#agents) and ends channel bindings at once. A persona write cannot change any grant or standing approval.

The `agents` store uses the fenced canon writer for agent rows, preset records, state, revision pointers, persona preparation acknowledgements and channel bindings. It saves each set of four layers as a Git commit in a private bare repository at `versions/agents/<id>/` before publishing its pointer. Immutable `refs/revisions/<commit>` retain saved versions; `HEAD` anchors the first version and canon selects the current one. Each immutable file is written and synced in a private temporary file beside its destination, then installed without replacing an existing file and followed by a directory sync. Opening the store removes abandoned temporary files; concurrent writers compare only fully published bytes. Unpublished objects after a failed write are never applied. `Prepare(turn, agent)` verifies the saved bytes and durably pins one revision to that turn; repeating it after a restart returns that revision, while a later turn selects the latest published revision. This is the persona part of preparation, not evidence that account, context or model-policy preparation succeeded.

Preset publication updates every extant agent of that preset in one transaction, replacing only core and default voice; per-agent state and the other layers stay intact. Direct revisions use an expected revision to reject stale edits and cannot replace preset-owned layers. First-run initialization is durable and idempotent: it creates LINA once from the embedded placeholder preset and returns only the ChatGPT sign-in requirement when no model account is usable. Deleting that default does not cause initialization to recreate it.

Agent trash hides its records from normal reads and new preparations, and removes its bindings in the same transaction. Restore is available strictly before 30 days and never revives bindings. Expiry records a content-free tombstone, removes persona/state/preparation records and the agent’s action history in the same transaction, and retries removal of the agent's repository. The deletion coordinator supplies time and propagates deletion to conversation and personal-knowledge stores when those stores exist. These store operations have no TUI or Core-protocol endpoint yet; package scenarios exercise real canon, Git revisions, restart and failure behavior. Account sign-in, the complete turn-preparation ledger and channel transports are composed by their owning flows.

**C-11 Preparation and assembly (changed).** Preparation runs the four steps of [conversation-and-memory.md](conversation-and-memory.md#preparation) in order and stops at the first failure with one plain line; the failing step is in Details. `Assemble(Input, Counter) (Request, Report)` is pure and lays out the seven items of the [request layout](conversation-and-memory.md#request-layout). `atropos.Packet` builds the ContextPacket once per turn under a time cap (P: 250 ms) and returns a minimal packet on timeout. Items drop as the request layout orders. The report holds ids, reason codes and digests, never text. The ledger records the request hash, the assembly list and the fidelity class of the provider that served it. The ContextPacket is never written into the history. For agent-runtime providers the same layers go in as one non-persisted session.

The implementation keeps `assemble.Assemble` free of I/O and time reads. `assemble.History` projects native ledger events in order, excluding preparation/compaction audit events, imported records and replay copies, preserving validated `provider_item` bytes and wrapping external messages as untrusted user input. Ordinary system and automation lines carry an explicit speaker/origin envelope as data, so they cannot be confused with the person’s words. Tool provider items must be tool-output envelopes; agent provider items must be assistant messages, reasoning or tool calls, never person/developer/system messages. An exchange reader may explicitly authorize its one partner; only that partner’s exchange events enter as external evidence. Its caller supplies that native ledger projection, a pinned persona revision, stable tool declarations, an authorized ContextPacket and a pure model counter. The request digest covers model, effort, purpose, allowed fields, instructions, ordered tools and exact input-item bytes, excluding credentials and live bridges. Opaque history and current-input bytes are copied unchanged. Assembly removes out-of-scope, retracted, secret, duplicate and superseded context before budgeting; a supersession cannot remove mandatory context and duplicate ids spanning different context kinds are refused. A superseded item stays excluded even if its replacement is later dropped for budget. When the mandatory request still does not fit, assembly returns no request and a typed reason. Reports contain only ids, revisions, digests, counts and reason codes.

Context kind controls ordering and budget priority, not authority. Profile, memory, person-brief and material items always pass through the guard's untrusted user-role evidence envelope, preserving their ids, revisions and citations. Any nonempty source locator also forces that envelope for other kinds. Retrieved data without a separate locator uses its stable context id; an External item without a locator fails assembly. Core-owned mandatory state and adopted instructions without an external origin retain their developer role. This prevents a collected passage or recalled source instruction from gaining authority merely by being grouped as memory or material.

`assemble/atropos.Packet` owns the time cap outside the pure assembler. The caller supplies the already prepared mandatory groups (time/emotion, commitments/reply budget, waiting state and skill bodies). Optional collection has a cancellable 250 ms default cap; timeout returns only those mandatory groups, discards late results and never waits for background work. Collectors must honor cancellation and clean up their reads. Parent cancellation aborts preparation rather than becoming a successful timeout fallback.

`turn.Options.Preparation` runs build verification, account/readiness, model/policy resolution and context preparation in that order. The composition root supplies these store boundaries and the pure assembly callback, respecting the downward import rule. Context preparation completes authorization and retraction checks before assembly. Its typed acknowledgement records the turn, agent, persona revision, policy revision and selected references; each request records its digest, round, provider fidelity class and the same assembly list before dispatch. `turn.EvidenceWriter` binds both recording callbacks to `ledger.Store.Admit`: system-originated `prepare_ack` and `request_assembly` events commit before the callbacks return. Their bodies contain typed metadata only and their parent references tie each request to its acknowledgement. A failed acknowledgement, request-record commit or turn-prepared commit prevents dispatch. The surface receives one plain line and a sign-in offer for a missing usable account; private error text and the failed step stay in Details. The older `Prepare` hook remains the low-level turn-durability seam and cannot be combined with `Preparation`.

The turn builds its packet once and lets the loop append outputs and steering for follow-ups. Each request is recounted after the loop’s context transform and before its evidence commit or provider call. A request still over its limit stops with `request_too_large`, preserving current-turn items and the packet; the consuming compaction flow can fit earlier history through that transform. Only model output reaches the turn journal; neither the packet nor the initial assembled request is persisted. Package scenarios exercise the pure layout, cancellation and timeout, each preparation failure, committed evidence before dispatch and SQLite reopen. These packages expose no new client-protocol or TUI capability; application admission and provider/account wiring belong to their consuming flow.
The #57 store supplies the immutable history records for assembly; preparation, request hashes and assembly lists belong to the consuming preparation/assembly implementation. Ledger admission neither constructs nor stores a ContextPacket.

**C-08 Loop and run durability (changed).** `kit/loop` is the loop core of [runtime.md](runtime.md#conversation-loop): a stream function, a context transform before each request, hooks before and after each tool call, steering and follow-up input, cancellation through the turn, and parallel tool calls.

Commit points: the turn row and inbox claim in one transaction; the preparation acknowledgement; done output items verbatim; the assistant tool-call message before any call starts; a non-read intent before dispatch; each result when it ends. A visible partial is kept only as an aborted entry outside model context.

Rules, each testable with a scripted provider:

- A call outside the offered set returns `tool_unavailable`. A cut-off response runs no tool.
- Every call id gets exactly one result in call order (synthetic for aborted, invalid and denied).
- Calls of one response run in parallel as the contract says, at most 8 at once (P); two calls that write the same path run in call order.
- The idempotency key is built as [host-protocol.md](host-protocol.md#envelope) says, with the task id as scope, or the turn id for an effect asked in a conversation (CP-04).
- A tool deadline excludes the approval wait. A call that needs approval returns `awaiting_person` at once and runs its stored arguments, bound by hash, after the answer.

After a restart an accepted turn is never replayed ([runtime.md](runtime.md#failures-and-retries)).

The #55 increment implements `kit/loop.Run` over `provider.Stream`. It waits for a terminal event and a clean stream end before releasing any call, and reports `incomplete_response` for a cut-off call so a caller can request complete arguments without an automatic retry. Completed items are committed verbatim as they arrive; aborted visible deltas are stored separately and excluded from model context. Validation and before hooks finish in call order before execution. Independent calls use at most eight slots; normalized write targets form ordered dependency chains. Results commit as each call ends and enter model context in call order. Cancellation does not replace a started call's returned result: a committed success or unknown receipt remains success or unknown in the turn journal. Only calls that never started receive a synthetic aborted result. Steering is drained at request boundaries, before the context transform. Every return, including errors, atomically closes steering admission and returns pending input in `Outcome.FollowUps`. An input accepted after the final drain is included in that handoff; `Inputs.Push` returns false after closure so the lane retains ownership and routes it to the next turn. Each run uses its own input queue.

`turn.Store` installs its schema through a caller-supplied numbered canon migration after the inbox schema. Claiming an inbox row and inserting the turn are one fenced transaction, with one turn per inbox row; preparation commits before a model request. Journal failures stop dispatch or the next request. Startup calls `Recover` before admission: completed receipts survive, every open call receives an aborted result (including calls whose item committed before the response ended), and accepted turns cannot be claimed again. `turn.Tool` uses the existing registry and authority ledger for intent-before-dispatch and receipt commits. Function-call input must be JSON; custom-call input reaches the registered schema as raw text, and the schema's normalized JSON arguments go to dispatch. The registry revalidates those normalized arguments before authorization. Approval-required dispatch returns `awaiting_person`; `ResumeApproval` uses only the approval's stored arguments with a fresh execution deadline. The earlier model result remains immutable; the later effect receipt belongs to authority.

Successful turn completion and its conversation replies commit in one fenced canon transaction. Once completion begins, late cancellation does not abort the completed turn or discard its replies: reply preparation is detached from turn cancellation, and the five-second write deadline starts only inside `completionCanon.Write`. A crash after commit leaves each reply in history exactly once, without replaying the turn.

These are package boundaries, with scripted Responses-adapter and SQLite reopen coverage. They add no conversation capability to Core's client protocol or the TUI. Protocol admission, application startup wiring, and result notifications attach in their consuming flows.


**C-09 Admission, steering, limits (changed).** All input enters one durable inbox, unique on (conversation, source, native id) and acknowledged when its row commits. One lane goroutine per conversation runs one turn at a time. Input during a turn is steering, applied at the next round boundary; input after the turn is a follow-up. Stop is a cancellation with a typed cause (`Interrupt{Issuer}`: person, watchdog, epoch_lost, grant_revoked, budget, shutdown); the text the model sees derives from the issuer. Limits apply only to chains that start without a person: continuations per input, exchange rounds (P: 4) and a scheduled budget. Exhaustion parks the input visibly and drops nothing.

The inbox table is installed through a numbered canon migration using `canon.InboxSchema`; admission uses the existing fenced writer. `inbox.Store.Admit` returns a receipt only after commit, with the stored row id and whether it was inserted. Repeated delivery returns the original id and preserves the original bytes, including across restart. Empty key fields are refused. Rollback, cancellation and commit failures return no receipt. `inbox.Insert` permits admission and a source cursor to share a transaction; its result is provisional until the enclosing write commits. A receipt acknowledges storage, not turn completion.

One `lane.Manager` per Core instance serializes submitted turns in each conversation's submission order while allowing other conversations to proceed. Each turn returns before the next starts, even after cancellation. Idle lane goroutines retire; shutdown rejects new submissions, cancels active turns, skips queued callbacks and waits for active work to return. An expired shutdown wait never means the turn stopped. The manager does not claim, replay or retry durable inputs; turn claims and steering stay at their respective flow boundaries. These packages currently have no client admission endpoint; the handshake-only client service remains as described in C-42.

The #61 increment adds `lane.Manager.SubmitLoop`, `Steer` and `Stop`. Each submitted loop receives a fresh steering queue. Admission commits a message before offering it to the active turn; an accepted message enters the next request boundary. A rejected push remains owned by admission and becomes a follow-up. Stop carries the supplied `turn.Interrupt` cause through the active turn and its tools, refuses further steering to that turn, and leaves queued turns intact. The lane stays occupied until the active work returns. Preparation and claim failures close the input queue too and return pending input to the caller.

`turn.Options.InputEvent` binds a turn to its accepted ledger event in the claim transaction. The owner installs `turn.ControlSchema` as a new numbered migration after the turn and ledger schemas. A cancellation appends one system interruption line through the ledger and records its issuer in the same transaction as the terminal state. Startup recovery appends the restart line while closing open calls and terminalizing the interrupted turn; repeated recovery produces no extra line and no model or tool call. The line uses the original conversation, agent and surface and derives only from the typed issuer, never an external error message. Existing successful or unknown tool receipts remain unchanged. Package tests kill a child process after a committed output item and verify the recovered conversation ledger, including failure and retry of the notice transaction. The handshake-only client service still exposes no conversation capability, so this increment has no protocol or TUI scenario.

**C-49 Turn budget and hand-off (new).** Values and meaning are in [runtime.md](runtime.md#turn-budget): acknowledgement 3 s, soft 6 round trips and 30 s, hard 10 and 60 s, tool deadline 15 s, all starting values. The design:

- `budget.Meter.Start(turn, receivedAt)` returns a `Gauge` that counts completed tool batches and the union of time spent in their executors (model time excluded). Either threshold reaches Soft (6 batches or 30 s) or Hard (10 batches or 60 s). Parallel calls count once; rejected batches count so repeated refusals still reach hand-off.
- At Soft, any call other than `start_run` and `dispatch_work` gets a typed `budget_reached` result and does not run. Tool declarations and the cached prefix stay unchanged.
- At Hard, Core ends the loop. With no effect made it calls `runs.StartFromTurn` with the input and the turn's tool outputs. With an effect made, the remaining reads and analysis go to a run and further effects wait on a question card. An effect with unknown outcome is never run again.
- A tool that can run long returns a handle. The budget decides only where work runs.
- A `turn_outcome` row per turn holds waiting time and quality counters (corrections, repeats, rework, "not what I asked"), filled by the reactions job of Lachesis. There is no automatic tuning.

`kit/loop` has a pure policy check before validation and again immediately before a queued executor starts. Budget rejection returns `budget_reached`, meaning nothing ran; it is not an abort or an unknown effect outcome, and the after-execution hook cannot rewrite it. This check never cancels an executor that has already started.

`turn.Store.Run` owns the placement budget, while background runs use the uncapped kit loop. Core supplies the receipt time, and a continuation restores the prior segment's meter and effect receipts; steering never resets it. At the hard boundary the turn returns all gathered input, applied steering, completed output items and ordered tool results through a `StartFromTurn` callback, preserving the C-40 boundary between the turn and run flows. No new model request starts at Hard, and an already running effect finishes with its original receipt. A hand-off failure remains a failure with its gathered state retained.

Report requests declare no tools. A provider that attempts a tool call during a report gets no execution and no second model request for its results; Core records the rejected calls and returns `report_tools_forbidden`. The fenced `turn_outcome` row is created with the claim and finalized with the turn state. It keeps waiting time, placement counters and the returned run handle. The reactions job writes absolute, nonnegative quality counters idempotently through `SetQuality`; it cannot tune the meter. Both new and existing installations add `turn.OutcomeSchema` as a separate numbered migration after the published turn and control schemas; `turn.Schema` remains unchanged. Structured outcome data is masked at decoded JSON string boundaries before storage.

`runs.Service.StartFromTurn` admits one durable run per originating turn, preserving the original inbox payload, active-turn output items, steering, ordered results and effect keys. It excludes unrelated prepared history, persona and memory context. A repeated admission with the same state returns the same handle; changed state is refused. A digest of the original admission preserves that identity when persistence masks credential-shaped strings, including across service restart. The supplied brief keeps the whole request's goal, done condition and checks. Its tool scope is narrowed to reads, and gathered state enters as evidence under background instructions. The composition root supplies `turn.PublishPromotion` through `runs.Config.PublishPromotion`: admission and publication of the source turn's handle commit in one fenced transaction. The turn checkpoints its gathered state and meter before admission. The caller consumes the committed handle before calling `Release`; startup recovery releases a promotion that committed but had not started, without replaying the turn or spending an execution retry. The conversation start tool is named `start_run`, so it remains available at Soft.

When promotion carries effects, the run record also holds an `awaiting_person` question for further effects. Reads and analysis continue; no effect is declared in the run. The question preserves known and unknown receipts, grants no authority for unspecified future actions, and remains pending when the analysis lands. The run cannot claim the whole request complete while that question remains. Concrete later effects belong to the work engine's normal authorization path; an unknown earlier effect is never replayed.

These APIs have no conversation capability in the Core client protocol or TUI yet. Scripted providers, real canon and the real background service exercise promotion and landing; client admission, question-card rendering, and composition-root release attach in their owning flows.

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

The package implementation keeps the run record and pending landing in canon through the existing fenced writer, with `runs.Schema` installed by the composition root. `StartTool` binds the requesting identity outside model arguments. `Start` commits the record and returns the receipt before any execution; the conversation publishes that receipt before calling `Release`. This explicit scheduling barrier makes the ordering independent of goroutine scheduling. The caller's turn context does not own the run's lifetime.

The run's request starts from its brief only. Trusted read/scratch adapters intersect the brief allowlist; every dispatch rechecks its originating authority and named targets through the adapter. A completed tool round is checkpointed with its final tool receipt before the next request; a persisted call intent prevents repeating a scratch write after interruption. Ordinary tool receipts are scoped to a committed model round, so a retry may reuse a call id for new work. After the round commits, its results remain in the checkpoint and the duplicate ordinary receipts are removed; only started scratch replay records need longer retention. Started scratch actions retain a separate full-call replay identity; an unstarted or denied call never suppresses a later authorized call. Steering remains durable until included in its checkpoint, and retry count survives process death. Secret-shaped text and sensitive JSON values of every type (including numeric and structured credentials) are masked before run records, transcripts, materials or the next model request retain them. Masking preserves valid JSON structure and exact numeric evidence, including JSON embedded in text. A pending landing is committed before material publication and can be released again after a transient storage error without another model call. The work directory holds the transcript and a separate scratch directory. `Prepare` is the composition seam for named projections and condensation; it cannot replace declarations or the background purpose. In-flight model/tool calls count as activity until their own adapter deadline; the progress check records the 15-minute check-in without ending a run for age.

Landing uses a stable material identity, keeps verified checks separate from model claims, and publishes pending notice metadata without the artifact body. Citation resolution consumes the pinned revision through EOF and checks its full hash and half-open byte span; page/section and source URL/capture time remain display provenance. Pending notices recheck evidence before delivery. Question transfer metadata explicitly carries the `needs_input` outcome without making the run terminal. Turn-boundary delivery/coalescing belongs to G1-35. The package currently has no TUI or client-protocol entry point; package scenarios exercise real canon/material stores, scripted provider streams and process death. They do not establish a live model account or a surface conversation.

**C-51 Reply shape (new).** [conversation-and-memory.md](conversation-and-memory.md#reply-shape) sets the budgets. This decision fixes the engine.

- `reply.Budget{Lines, Chars}` exists per reply type and surface, stored per person in canon as a setting.
- Counting: lines as rendered lines, using the responding surface’s deterministic layout function (including wrapping); characters as Unicode scalar values after NFC, Unicode whitespace excluded for Korean. Counting never normalizes the displayed or recorded bytes. Other languages use an explicit ratio table (data), keyed by base language: multiply the Korean character ceiling by the ratio and round down. An unconfigured language is refused rather than assigned an invented equivalence. Korean remains 1:1.
- A streaming `Filter` counts the deltas. A reply exactly at the hard ceiling remains inline; on exceeding either limit the filter replaces the visible text with the last complete sentence within both limits and creates a `document_draft` preview. Views are replacements so the unfinished sentence can be retracted without delaying first words. Sentence boundaries follow Unicode sentence segmentation and require terminal punctuation; if none fits, only the card remains. The draft holds the complete reply as a literal paragraph, including text received after the cut. The ledger keeps the reply as written. `Filter.Record` commits the full source as an agent ledger event and the complete preview as a separate event linked with `view_of`, using one `ledger.AdmitBatch` transaction. It acknowledges success only after that transaction commits. A failure or crash leaves both events or neither; the same frozen filter can retry a failed transaction without duplicating either event. Admission masking and secret flags carry into the preview. Large bodies are prepared before the canon transaction; their material metadata commits in the same batch. The material writer permits several outstanding preparations on one writer so both reply and card can join that transaction; each retains its own recovery marker until cleanup. This internal filter does not define the reserved conversation or result-body wire payloads; their consuming implementation supplies those adapters.
- Counters: ceiling breaches, and internal vocabulary, ids, hashes and paths in the default view (a word list per language as data; patterns for ids, hashes and paths). The check never judges wording. A rewrite before showing is not built.
- Tuning: the reactions job of Lachesis recognizes the person's reactions, and Core applies the contract's step as a setting change that records its evidence. The person edits budgets in settings.

**C-52 Typed results (new).** Contract: [host-protocol.md](host-protocol.md#typed-results), [conversation-and-memory.md](conversation-and-memory.md#results), [materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result).

- `kit/result` validates a body against the schema and size limits of its type and renders `Text` deterministically from the body, so no model writes `text`.
- Tools: `present_result`, `read_result` (returns the revision read) and `save_result`. What the model sees and the stale-revision rule are in the results section above.
- A revision is one ledger event with the whole result.
- Save is a Core effect with the key of `result_save` (CP-05); destinations are those of the saving section.
- Summary-first writing is a background run given the latest outline revision.

The #104 increment implements these boundaries in `kit/result`, `internal/core/results` and `internal/core/exec`. Core supplies the trusted person, agent, conversation and turn; none is accepted from tool arguments. `present_result` takes `type`, `title`, `summary` and a complete `body`, with `result_id` and `base_revision` only for replacement. `read_result` takes `result_id` and returns the complete current result and its revision. `save_result` takes `result_id`, `revision` and a resolved `destination`; its binding requires an exact matching request from the person in the current turn. A grant alone does not constitute that request. The three operations use the existing tool registry, authority gate and leases, run in Core, and require no network or approval interaction.

Body validation is a closed schema over [the kit types](../../kit/result/body.go): exact field names, no unknown or duplicate keys, one complete JSON value, required fields present and non-null unless explicitly nullable. Validation also checks references, tagged block variants and cell value types. The initial limits are 1 MiB of body JSON, 1,000 elements per collection, 100 table columns and 10,000 chart points in total. A caller may tighten these limits. Titles are at most 200 Unicode scalar values and summaries at most 600; the two-sentence summary rule remains an agent instruction. Text is deterministic, complete, data-only and never fetches an asset or URL. Secret-shaped strings are masked before rendering, storage and acknowledgement.

| Body | Implemented fields and constraints |
| --- | --- |
| `summary` | `outline` sections have `heading` and optional `notes`; `key_points` and `decisions` are strings; `open_questions` have `question` and `answer`; `scope` has `includes` and `excludes` string arrays; `length` is a positive number of screens. |
| `table` | Each column has `name`, `type` (`text`, `number`, `date`, `boolean`) and `unit`; each row and optional `totals` match the columns. Dates are `YYYY-MM-DD`. |
| `diagram` | `kind` is `flow`, `sequence` or `tree`; `direction` is `up`, `down`, `left` or `right`; nodes have unique `id` and `label`; edges have `from`, `to`, `label`; groups have `label` and node ids. All references resolve; a tree is connected and acyclic. |
| `chart` | `kind` is `bar`, `line`, `area`, `pie` or `scatter`; each series has `name` and inline `values` of `{x,y}`. `x` is text or a number, numeric for scatter; `y` is numeric, nonnegative for pie. `x_axis` and `y_axis` each have `label` and `unit`. |
| `document_draft` | Ordered `blocks` are closed variants: heading (`text`, `level` 1–6), paragraph/quote (`text`), list (`items`), table (`table`), code (`text`, optional `language`), image (`asset`, `text` alternative description), citation (`anchor`, `text`). `from_summary` is null or `{result_id,revision}`. |
| `comparison` | Each target has `label`, `old`, `new` and `hunks` with zero-based `old_start`, `old_count`, `new_start`, `new_count` within the corresponding text. `apply` is null or an opaque request id. Save is refused; applying is a separate flow. |
| `plan_view` | Ordered entries have unique `id`, `kind` (`goal`, `project`, `todo`, `area`, `memo`, `resource`), `title`, `state` (`planned`, `active`, `blocked`, `done`, `cancelled`) and nullable `parent`, which must precede the child. `confirm` is null or an opaque confirmation id. Save is refused. |
| `file` | `asset` is `{asset_id,revision}`, `name` is nonempty, `size` is nonnegative bytes and `media_type` is PDF, PPTX or ODP. Saving copies verified bytes into an independently owned material. |

Asset and result references have positive revisions. A citation anchor has either `event_id` or `asset`, plus a nonempty half-open `start`/`end` passage range. Strings, including code and links, remain inert data.

The results schema is installed through a caller-supplied numbered canon migration after the ledger. It stores only head locators and save claims. Each complete result revision is one append-only `conv_event`; even a preview above the generic ledger text spill threshold stays inline and creates no material. A rejected body or stale replacement appends nothing. `present_result` returns only the card fields and an initial text excerpt (2,048 Unicode scalar values by default); `read_result` is the explicit body-reading operation. A saved result keeps its revision and appends a status event pointing to `saved_as`. Request assembly skips every `result` scaffold (creation, replacement and saved-status events), so these storage bodies never become model messages. The separate `provider_item` tool acknowledgement remains byte-for-byte history; only an explicit `read_result` output includes the body.

Save resolves and authorizes its destination without publishing, then commits its CP-05 claim and allocated record id, creates the record outside the canon transaction using that same resolved destination, and finally commits the receipt and saved status. A refused container lookup creates no claim and leaves the preview editable; repeated committed claims return their receipt without looking up the destination again. Concurrent/repeated calls have one writer. An interrupted publication or receipt commit stays `unknown`, blocks further preview revisions and is never automatically repeated. A destination is `{kind,container?}`: memo inbox for summaries; resource for other savable types unless the caller resolves the thread's project, area or goal. Container ids are resolved by a trusted ownership-checking lookup, never treated as paths. The initial records writer saves one Markdown file with typed result data and provenance under `versions/records/`, and one Git commit for saved version 1. Names are human slugs with collision suffixes, never record ids. Before changing a tracked file, index or ref, the records writer journals the intended parent/commit, file and blob ids in canon and retains a durable token for its Git index lock. Reopening completes only that exact interrupted publication, checks all other working files, and releases only a lock with its own token; different target bytes, a different ref or a foreign lock are refused. Recovery repairs repository consistency so later saves can proceed, while the original unknown effect remains for reconciliation without redispatch. It checks the repository head against a canon checkpoint and verifies tracked working files; it refuses both committed and uncommitted outside changes instead of silently incorporating them. File saves clone the referenced asset so conversation deletion cannot remove the saved bytes. Later material editing, indexing and reconciliation of unknown effects belong to their consuming flows.

This increment exposes package operations and turn bindings. Core's client service still grants no conversation or results capability; result protocol requests, TUI rendering and the corresponding headless surface scenario attach when that service is wired. It does not implement C-51's streaming reply filter.

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

#80 implements `internal/core/accounts` as a callable Core store. One store owns each fenced canon writer, and one actor per account serializes credential admission, replacement, status changes and scheduled refresh. Account metadata lives in canon; the person/provider/account key selects one opaque access-and-refresh record in the secret store. No token enters canon or the account projection. `Credential` returns only the requested account's opaque provider credential. The provider credential keeps its bearer as `secret.Secret`, including under nested formatting, and `Authorize` applies it only through `secret.InjectHeader`; the constructor and printable representations retain their existing API. ChatGPT OAuth admission also requires `chatgpt.tokens.use.direct`.

The actor refreshes ten minutes before either known expiry (halfway through a shorter lifetime), with a daily idle keep-alive when expiry is unknown or farther away. Existing turns finish before the exchange; new turns wait until the credential replacement finishes. Separate accounts may refresh concurrently. Before contacting the issuer, the actor durably records needs sign-in. It replaces the secret record before committing the new expiry and status or releasing access to the bearer. Interruption, a failed exchange, a missing secret or failed persistence therefore cannot reuse an uncertain refresh token. A failed exchange stops the timer and returns needs sign-in without retry or account fallback; recovery requires replacement credentials. A successful keep-alive preserves not eligible or usage capped. Caller cancellation does not cancel an exchange shared with other callers; closing the store cancels and joins its actors. The trusted exchange callback has a 30-second deadline and must honor cancellation. Sign-in replacement obtains turn exclusion before changing metadata and holds it through the metadata commit. Replacement may supply verified granted scopes; omitted scopes preserve the existing grant, and an explicit empty list removes it. A temporary keychain read failure before any exchange preserves the account state and returns credentials unavailable; due idle refresh reads back off for one minute and resume when storage recovers. Missing or malformed credentials still require sign-in. Returned credentials are per-call snapshots for trusted adapters; callers must keep their use inside the pinned turn and must not retain them for later requests.

The store tests use real canon and private secret files, injected issuer/storage failures and a virtual clock. #81 connects the sign-in service and fixed-issuer refresh exchange to this store, installs client relay operations, and exercises owner binding through the real client socket in `e2e/m1/chatgpt-signin.json`. Live OAuth and request-surface acceptance remain pending the owner-run [V1 check](../../evidence/v1-chatgpt.md); provider-side revocation remains separate from local sign-out.


The sign-in keeps state, nonce and PKCE verifier only in Core. Pending attempts are scoped to the initiating connection, consumed once before exchange and cancelled on replacement or disconnect. Core validates structured and pasted callbacks identically, including the issued dynamic client id and returning registration. The fixed OpenAI issuer's RS256 signature and issuer, audience, expiry, issued-at and nonce claims are checked before accepting an identity; a returning account must retain its verified subject. The token response must grant `chatgpt.tokens.use.direct` before the account can be used for inference.

The service owner appends `accounts.SignInSchema` to numbered canon migrations and supplies its person, account store and leaser. `NewSignIn` persists one host id; `Begin` selects only a saved registration belonging to that person and host. `SignInRelay.Operations` installs the account request, listener report and redirect handlers; the supported-combination row must grant both `accounts` and `sign_in_relay`. The connection id is assigned by Core and differs across reconnects even when a client repeats its public id. Legacy empty payloads decode but cannot perform a sign-in.

#112 wires this store and the existing sign-in relay into installed Core even
without a configured conversation model. The composition appends SignInSchema
after its existing turn migrations and opens one secret store and one account
store on the same fenced canon writer. The production issuer remains fixed;
only the in-process test composition injects an HTTP transport. The client
combination advertises accounts and relay independently of conversation.
Core startup, backup, restore and the read-only doctor share
`boot.ApplicationMigrations`, which appends the sign-in schema after the fixed
canon catalog, so each recognizes the account schema installed by the same binary.

Account list projects metadata for the connection's person. Sign-out runs on the
existing account actor and obtains the secret store's existing mutation
reservation. It waits for active turns, persists `needs_sign_in`, then deletes
the existing secret record while keeping new turns excluded. Cancellation while
waiting changes neither metadata nor credentials. Once reserved, retirement
finishes or fails closed even if the caller disconnects. A crash or storage
failure after intent leaves the account unusable and the request retryable.
Repeated deletion of an absent credential succeeds. Registration metadata
and the independent owner binding remain for reauthorization. No refresh rule
or token storage format changes. The thin client handlers expose this existing
store through list and sign-out operations, with no second store or relay.

The TUI consumes the same relay operations through correlated client requests.
Its loopback and pasted-URL routes are exercised in `e2e/m1/tui-accounts.json`
with the production account store, signature validation and owner-binding path,
a synthetic issuer, and the built TUI in a headless PTY. No real account or token
is needed for this client acceptance check.

Owner binding is a singleton canon record independent of provider credentials. The serialized canon writer binds the first successful sign-in and compares every later login's person, issuer and subject without updating the binding. A different provider account cannot replace or delete it. Competing first sign-ins cannot both establish different owners.

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

The startup coordinator creates the workspace and protected state directories while holding the root lock, composes a Core-owned `kit/proc` owner and probes the wrapper after reconciliation and before READY. The startup snapshot carries availability and a stable cause; the shell/file service receives that same wrapper, including its first-call recheck. Failure reports `shell_file_tools_unsupported` and never prevents the client handshake or enables an unsandboxed fallback. Reverse shutdown closes process admission and kills and joins owned commands. The #50 acceptance canary checks canon, secrets, material revisions, symlink aliases, the client socket and its hard-link aliases with network both blocked and authorized; `e2e/m1/sandbox.json` covers a missing or failing launcher with a live real Core and TUI. That scenario uses connection-only boot and establishes client-service continuity. The configured conversation route and its installed-Core reply proof are specified in [host-protocol.md](host-protocol.md#conversation-payloads).


- Network: a command a plugin profile names gets it by default, and asks first only when the profile marks it as outward or irreversible ([integrations.md](integrations.md#command-profile)). Any other asks once, with once, always allow and deny.
- First release: connected sources are mounted read-only for the shell. Edits to a person's files go through file tools with revision checks and the recoverable form of [materials-and-knowledge.md](materials-and-knowledge.md#editing-a-persons-files-in-place). Deletion by file tools goes to a trash.
- A local plugin server added by command gets its own sandbox instance, its data directory and only the allowance on its card.
- A shell write is recorded with changed paths and hashes before and after, found by a size-and-time manifest diff of the working directory.

`internal/core/exec` registers the file tools and Core-selected command prefixes with the existing registry. Its file sandbox permits the workspace and a dedicated private version directory; its command sandbox denies that version directory. One executor serializes these operations per workspace and starts only the explicit `lina file-tool` entry through the verified wrapper. Both paths retain the caller's protected Core paths and have no unsandboxed fallback. The executor-only manifest operation runs in the file sandbox and supplies the trusted workspace revision plus the shell receipt's changed paths and before/after hashes. A command's failure does not discard its observed writes. A nonzero exit with observed changes is `unknown`, retaining the observed revision, changed-file receipts and authority reservation until reconciliation; only a nonzero exit with no observed writes is `failed`.

File execution validates a bounded request before touching a file. `read_file` returns bytes and a content-and-mode revision; `write_file`, `delete_file` and `restore_file` require that revision (`absent` for creation). `apply_patch` accepts the same Codex patch text in custom-tool and function-tool form, validates all add/update/delete/move operations and exact context before writing, and checks each captured file revision again at the destination. Paths are relative to the Core-selected root; traversal, symlinks, special files and `.git` paths are refused. The sandbox remains the OS boundary, including read-only Git metadata.

Before replacement or deletion, the file worker retains and fsyncs both versions in a separate private version directory. It records a same-filesystem recovery slot before moving the existing file into that slot, then verifies the captured bytes against the checked revision. New bytes are published only by a no-clobber link into an absent destination. A concurrently recreated destination is never replaced or removed. A mismatched capture is linked back only if the destination remains absent; the outcome is still unknown. External editors need no cooperating lock.

The receipt and saved-version record carry the capture's relative recovery path as well as the before/after revisions and opaque saved-version id. The actual captured inode stays in a reserved `.lina-recovery-*` directory beside its former path, so an editor holding an already-open descriptor can continue writing without losing those bytes. An observed concurrent change returns unknown and keeps the recovery information through the worker reply and effect ledger. A completed receipt describes the final observation, not future writes through another process's open descriptor. Captures remain readable through file reads after reopening and participate in workspace manifest revisions. A recovery-like directory name is not proof of worker ownership and never hides its files from a command receipt; this also keeps later writes through captured open inodes observable. These live captures share the workspace's access scope, whereas the immutable before/after version store remains private to the file worker. Restore from a saved-version id uses its immutable bytes and the current destination revision. A multi-file patch is prevalidated but is not a filesystem transaction: an interrupted commit is unknown with retained recovery information. Retention and permanent purge of saved versions and captured inodes remain the owning retention workflow's responsibility.

Network command arguments carry argv and the network request only. Core supplies the allowed prefix, target, service-profile policy and scope key; model arguments cannot broaden them. The registry parks an unapproved network call in the existing durable authority service before its handler starts. Once permits that intent, deny prevents execution, and always allow applies to the same prefix and target until revoked. Resumption dispatches the stored arguments under a fresh deadline. These services still have no tool/approval operation on the connection-only client protocol; their protocol and TUI scenarios accompany that adapter.

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

Canon owns one fixed, append-only application migration catalog, shared by Core startup and backup. The published development order is retraction at version 11, turn records/control/outcomes at versions 12/13/14, and compaction at version 15. Runtime composition never appends the turn schemas by the current list length. A database created by the current dev catalog upgrades without recreating existing turn tables, and later migrations append after these fixed numbers. No release has shipped: incompatible older development layouts are refused with a published-catalog error before writer initialization, without a compatibility shim, renumbering or normalization.

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

The compaction pass counts the complete provider request at each stage, with a strict below-threshold dispatch condition. A session transform retains the compacted prefix across subsequent loop appends while leaving current input, the packet and current-turn protocol items unchanged. Old tool outputs keep their call ids and position; deterministic notes retain an event locator for `expand`. In-turn fitting first uses an already completed preparation, then summarizes the remaining earlier history including its user tail. No stage removes events before successful publication.

Auxiliary summary requests use the conversation model and reasoning effort, carry only the selected segment and its direct child summary in a fresh nonce envelope, and declare no tools. The fixed summary contract travels in a developer input item because auxiliary Responses requests omit the separate instructions field. A complete terminal response and all fixed summary fields are required. Stream failure, incomplete output or publication failure leaves the unresolved events in the request path and prevents dispatch. Continuations durably record the ended turn and original input reference before fitting, rebuild the packet and send the original input once. An oversized continuation returns `request_too_large`, sends nothing, and records complete and per-part sizes. These APIs attach through the loop transform and composition callbacks; no new client-protocol or TUI endpoint is introduced.
`compaction.Builder` resolves authorized, masked canonical event bodies and the immediate child root before invoking the auxiliary summarizer. A completed preparation can be reused after new verbatim/current-turn events arrive only when its summarized sources, root and revocation epoch are unchanged; its publication frontier is refreshed and checked again at commit. The new node and its content-free compaction audit event commit together. `Expansion.Read`, exposed through the agent-bound `exec.ExpandTool`, pages source events in ledger order across a node’s descendants, reads full material revisions, preserves secret masks and refuses retracted, foreign or unavailable sources. Missing material bytes fail the page explicitly.

The summary store exposes `Frontier`, `Publish`, `Node` and `Rebuild` as internal package operations. A frontier identifies the conversation's append-only ledger count and last event, current root id and revision, and revocation epoch. Publication checks it inside canon's fenced writer, then proves that child coverage, direct covered/skipped event ids and caller-supplied verbatim event ids partition the complete ledger frontier without gaps, duplicates or foreign events. Exact event rows in `sum_cover` represent the ranges without losing skipped-event reasons; direct event ownership and child ownership are unique. A later compaction extends the current root, with its level and event time bounds derived from provenance.

An epoch advance clears the affected nodes' text and dropped-detail fields and marks them invalid, including ancestors, in the removal transaction. Reads refuse invalid, degraded or older-epoch nodes. `Rebuild` prepares the tree bottom-up from visible direct event locators and freshly rebuilt immediate children, runs the supplied summarizer outside transactions, and replaces all changed nodes in one transaction only if the frontier and epoch still match. It retains node ids and links, records retracted events as skipped, and advances node revisions. A failed summarizer or write leaves the entire tree unpublished or invalid. An unrelated revocation conservatively requires stale trees to be rebuilt before use, matching preparation snapshot epoch checks.

These operations add no TUI or client-protocol endpoint. The consuming compaction flow owns model selection, authorized masked body/material resolution, the nonce envelope, request fitting, compaction audit events and exact-body expansion; the storage layer never calls a provider or silently truncates a failed summary.

**C-19 Retraction (kept).** One transaction suppresses the targets, deletes index rows, invalidates derived rows by revocation epoch and advances it ([conversation-and-memory.md](conversation-and-memory.md#correction-retraction-and-deletion)). The removal list `backups/removals` is fsynced before the tombstone commits.

The removal coordinator resolves identities and evidence from canon, then syncs one content-free removal intent before committing its canon changes. The intent is the recovery decision: if canon cannot commit after that sync, reads and ordinary writes remain closed until replay completes. `canon.Store.Reconcile` drains existing operations, excludes new ones during the change, and keeps access closed on recovery failure; its callback receives the fenced writer used only for reconciliation. Startup opens and recovers the coordinator before publishing the canon store to consumers. Replay checks the removal list against canon's acknowledged prefix and applies missing operations in one transaction; repeated replay changes neither the targets nor the epoch.

A preparation takes a person/agent-scoped retraction snapshot. It excludes forgotten item references and their source events from model input, and excludes stale summaries and other derived input by epoch. Before dispatch it checks that the snapshot's epoch still holds. Extraction checks both the cited event ids and their conversations while the item is in trash and after permanent deletion. Recovery is available strictly before 30 days; expiry selects and deletes eligible trash in the same writer transaction. Tombstones retain identity and provenance, with a reference to their acknowledged removal operation, and never retain content.

Search and ledger fallback in `recall_person` use the explicit `retract_visible_event` projection. Ordinary ledger reads retain the original messages for authorized conversation display; preparation filters model history through its retraction snapshot. Exclusions follow the complete replay/`view_of` lineage, including copies admitted after forgetting, and are rebuilt from their roots after restoration. Memory recall and profiles use `retract_visible_memory`, which excludes items whose direct or transitive item evidence reaches a suppressed source. The dependent canonical items retain provenance for maintenance; their search rows are removed and their references cannot enter a packet. Memory insert/update triggers check extraction exclusions at commit, including a job prepared before forgetting. Explicit trash recovery may restore one item whose evidence is shared with another forgotten item without removing the other item's exclusions. Permanent memory deletion removes every revision. These are package operations; no memory/retraction client endpoint is introduced here, and whole-generation backup orchestration and conversation-content deletion retain their owning flows.

Retraction uses the search stores' delete triggers to remove FTS postings with their source text, and removes the ledger's Hangul bigrams and pending backfill rows in the same transaction. Index insertion triggers discard a late backfill for suppressed evidence. Recovery requeues a source only after its last exclusion is removed; the normal ledger reader rebuilds its masked search text. Tests check both query results and physical index rows, including after old-snapshot replay.

**C-20 Memory items (changed).** The table has the fields of [conversation-and-memory.md](conversation-and-memory.md#memory-items). There are no storage caps; limits apply only to the rendered standing profile (lines and tokens). The write checks of that section run at commit, and a failed check refuses with the reason.

```go
type RecallScope struct{ Agent AgentID } // sole constructor of recall candidates
func (s RecallScope) Holders() []Holder  // the agent and "all"; filtered before ranking
```

The memory store keeps the current item and immutable revision snapshots in canon. `RecallScope` also binds the person and whether the read is a projection or goes to an external model; SQL excludes other holders and people before text matching or a result limit. Local views can read the person's named scope classes; projections exclude `private` and `never-leaves`. External model calls exclude `never-leaves`; raw ledger fallback and other items citing the same protected event cannot bypass that restriction. Protected event ids are collected across the person's holders without exposing those holders' records, then applied to the authorized candidates. A common item's foreign conversation evidence retains its event id, source agent and time, but not its source quote. Native event provenance is resolved again inside the fenced memory write. Replay and `view_of` copies resolve to one canonical event; a lineage with a different speaker or owner is refused. Exact duplicates within one subject, holder, scope and validity interval add evidence to the same item and advance its revision only when something changed. A stated successor cannot be replaced by a derivation.

`exec.Memory` composes the ledger and memory stores without an L3-to-L3 import. It verifies large source bodies before entering canon's writer, then uses those immutable bytes while checking canonical metadata in the transaction. `memory_write` takes holder and scope decisions only from the trusted conversation binding, rejecting those fields in model arguments. It returns its id, revision and remembered receipt only after commit; a commit failure returns no acknowledgement. Native event evidence is implemented here; promotion, correction, retraction and verified non-event source adapters belong to their owning flows.

**C-21 Person model (changed).** The views are those of [conversation-and-memory.md](conversation-and-memory.md#person-model). `recall_person` at depth 1 makes one auxiliary request over at most N items (P: 12), with citations checked against the retrieved set and capped at 10 s. Deeper work is a background run. The person brief and the maintenance pass are jobs for `moirai-worker` ([cognition-and-life.md](cognition-and-life.md#memory-jobs)). Duplicate clusters inside one subject use normalized equality, token overlap and, when available, embedding similarity; never across subjects.

The `recall_person` tool requires a nonempty retrieval query alongside the question, so an omitted query cannot silently choose the most recent items. The initial executor uses a limit of 12 combined memory items and own-ledger hits, with depth 0 as the default. Depth 1 makes one cancellable auxiliary call with a 10-second deadline and no retries. Its structured sentences retain only citations to the retrieved id and revision; sentences left without citations are dropped. A depth above 1 returns the background-required outcome to the caller. The standing profile is an ephemeral view ordered by the latest committed revision, with line and tokenizer budgets supplied by assembly. These operations are callable Core/tool services; no memory operation is exposed by the current client protocol, so acceptance uses real canon and ledger package scenarios. The indexed memory matcher and the ledger search service are described in C-22.

**C-22 Korean search (changed).** FTS5 with the trigram tokenizer for ledger and memory, plus a Hangul bigram posting table for words of two syllables. The acceptance set of the contract, by id, is a particle-attached three-syllable word and a two-syllable word, on the ledger (imported conversations included) and on memory. Each index records its tokenizer and fallback. Vectors are derived data in sqlite-vec when an embedding endpoint is configured.

The two-syllable index stores distinct adjacent pairs of composed Hangul syllables. It does not bridge spaces, punctuation, another script or a removed secret span. Search text is a literal phrase: quotes and FTS operators in the text do not become query syntax. Index rows use the same secret-free projection as recall, commit with their source, and are rebuilt for existing eligible rows when the index is introduced. Holder and conversation access, current validity and retraction filters apply before ranking or limiting hits. The synthetic corpus in `testdata/korean/search.json` covers attached particles, imported text, duplicate occurrences, separated syllables and literal quotes.

The ledger store exposes `Search` with a required agent and person scope, optional origin, speaker and occurrence-time filters, and an explicit replay opt-in. A person event establishes conversation ownership; conversations with another person or no established person are excluded, including their agent/tool replies. The same scope gates pending backfill before any source bytes are read. The default limit is 20 hits and the maximum is 100. The migration durably queues existing events; an insert trigger also queues imported rows. Native admission writes the secret-free text and both indexes in its event transaction. Search drains the requesting person's and agent's pending rows in batches of 128 before reading hits. It reads material bytes outside canon transactions, and a missing or unverifiable material fails the search instead of returning cached text as a complete event. Index metadata records `trigram` and `hangul_bigram`. One-character queries and two-character queries outside composed Hangul have no postings in this increment. Ledger search is a package boundary; it adds no TUI or Core client capability.

Memory recall uses the same literal trigram/bigram choice over its normalized content, retaining case and whitespace normalization. A materialized person/holder/scope/validity set precedes text matching. The internal restricted-event id set spans all holders of the person so a common item cannot bypass another holder's protected evidence; returned candidates still require the requesting holder or `all`. Within that set, ordering by latest revision and the history option are unchanged. SQL triggers maintain both indexes on insert, normalized-content change, state change and deletion in the source transaction. The deterministic SQLite function `lina_hangul_bigrams` shares the ledger extractor, traversing text once before deduplicating and sorting postings. Only active rows are indexed; expired and superseded active rows remain searchable only through history. The forward migration backfills existing active rows. Empty queries still enumerate scoped items, and short non-Hangul queries retain scoped literal matching for compatibility. Evidence quotes and glosses are not indexed.

**C-23 Skills and growth (changed).** Skills are `SKILL.md` folders in `versions/skills/`; built-in skills are embedded. The agent writes through a Core tool. A reversible-write check classifies a diff: only instruction text, no new executable file, and no tool, connection or permission in the front matter. A reversible write is adopted at once as a new version with a one-line notice and a one-action undo, and an undone content hash is not written again. Anything else goes to the explicit-acceptance path as a `summary` result and is built by the work engine ([cognition-and-life.md](cognition-and-life.md#skill-suggestions)). The catalog index holds each skill's name and description (its one-line trigger), and Atropos preloads at most three bodies by FTS over the descriptions (P). The prune and merge job and the persona growth step ([growth](cognition-and-life.md#growth)) use the same path.

The #71 store publishes a metadata-only catalog in canon and keeps instruction files in immutable Git trees in `versions/skills/`, one folder per skill. The composing owner installs `skills.Schema` in a numbered canon migration. A write merges the submitted files with the current bundle, checks both the existing and resulting bundle, and requires the expected revision. Automatic writes accept regular, non-executable UTF-8 Markdown with only one-line `name` and `description` front-matter scalars. Unknown front-matter fields and executable/support-code files return `skill_requires_explicit_acceptance`; text inside a Markdown code example remains instruction text. The automatic path adds no connection or permission.

Git objects and immutable revision refs are synced before the fenced canon transaction publishes the head and catalog entry. Only a successful transaction returns the one-line notice and undo handle. A stale write or undo changes nothing. Undo creates a new commit containing the prior text (or removes a newly created skill from the active catalog), and records the rejected bundle hash in the same transaction. Neither another patch nor another undo can reintroduce that hash. Earlier adopted-version handles still read their original files; catalog snapshots therefore apply changes to subsequent preparations. A built-in handle includes the compiled catalog digest without loading its body; a different executable generation refuses that handle instead of returning replacement text, and its caller must prepare again. Once patched, the built-in baseline is retained in Git for undo. Names must match their skill folder IDs, so the common catalog cannot alias two procedures under one name. Catalog reads never load version bodies. A failed filesystem write or canon commit publishes no notice or new version; immutable objects from an interrupted attempt may remain unreachable from the published head.

`exec.RegisterSkills` binds `skill_index`, `read_skill`, `write_skill` and `undo_skill` to the normal registry, authority gate and effect ledger. Its committed tool receipt contains the notice; an executable change returns a summary proposal for the explicit-acceptance/work flow without adoption. The embedded meta-skill describes this procedure. These are package interfaces: Core's current client service exposes no skill operation, so #71 has no observable TUI/protocol scenario. Context selection, client presentation of notices and proposal acceptance attach in their consuming flows.

**C-28 Importing a Hermes install (changed).** Contract: [memory importer add-ons](conversation-and-memory.md#memory-importer-add-ons), [Hermes import](conversation-and-memory.md#importing-a-hermes-install), [filesystem.md](filesystem.md#importing-another-memory-system), [integrations.md](integrations.md#memory-importers). The optional Hermes adapter lives in `internal/addons/importer/hermes`, outside the Core layers. Production packages outside `internal/addons/` must not import memory importer add-ons; Core, onboarding and default commands remain independent of them. Foundation registers the adapter and `testdata/hermes/` for build, vet, analyzer and test checks. An installed adapter runs as a background job started by the person, with no model call and deterministic classification. The complete import flow comprises:

- snapshot readers (SQLite via the online backup API on a read-only connection; Honcho through an HTTP client whose transport rejects any method but GET);
- the schema gate and the maps the person confirms;
- the contract's ordered stages, one transaction per conversation;
- `legacy_ref(system, instance, kind, key)`, unique and written in the same transaction as its record;
- a reverse mapper whose canonical hash must equal the stored source hash, and a resolver for old ids;
- a secret scan with flags, an `import_sealed` event, and a backup generation at the end.

The snapshot boundary in `internal/addons/importer/hermes` selects an independently registered schema by source kind, never a descriptor inferred from the source under import. The initial registrations are `hermes-lcm` (35 tables) and `hermes-state` (23 tables and two views), derived from schema-only metadata supplied for acceptance. Both have `user_version = 0`; acceptance requires the complete registered column layout as well as that version. SQLite checks `user_version` and the exhaustive ordered table and view `table_xinfo` column descriptors before backup and again on the staged database before returning it. A mismatch names the version, table or column and publishes no snapshot. Honcho compares the published OpenAPI document structurally with its accepted description before reading data, permits only described GET routes on the supplied origin, refuses redirects, and stores each response without rewriting its bytes. Remote Honcho requests require HTTPS; HTTP is allowed only on literal loopback addresses. The read credential lives only in the adapter reader for that run and is released on close; it is never sent to the worker. Page enumeration and canon adoption belong to the ordered stages, not this reader.

Sidecars are regular files read within the handed-over root and copied exclusively into separate import staging with SHA-256 verification. Missing files are returned as unavailable for the later stage to report. Failed copies remove their partial output and never overwrite existing files. Secret scanning returns sorted, merged byte spans into the unchanged original, including binary sidecars; agent projections mask them and derived projections exclude them. Detection covers recognized credential shapes, credential assignments, authorization values and private-key blocks, including unterminated blocks; it is a heuristic, not proof that arbitrary secrets are absent.

Fixtures contain synthetic rows in independently reconstructed layouts matching the registered SQLite metadata, including FTS hidden and shadow columns and the two known host views. The original schema exports are not stored in the repository. No Honcho API description is registered: its public reader refuses the source before network access until a published description is obtained and registered. That registration waits for the owner to supply the Honcho address and read credential at import time. The synthetic OpenAPI fixture tests the internal HTTP mechanism only and cannot enable the public reader. These primitives expose no TUI or client-protocol operation; the later import stages provide that entry point.


The staged importer prepares all registered SQLite rows and confirmed profile maps before a canon write. LCM messages retain numeric source order, host copies join by profile-local lineage and UID (a matching payload version stays distinct), and host-only rows follow. Compression lineage stops at explicit fork, delegation and reset markers. Honcho matching considers every matched host/LCM session copy; a unique same-session, same-role text prefix within 60 seconds is preferred, then unique exact text in that session. Ambiguous matches become standalone `honcho-only` events. Matching never supplies person proof; the existing provenance classifier runs on the complete copy set.

`canon.ImportSchema` is an explicit schema fragment for the optional import host, appended after that host's existing migrations. It does not renumber the default Core migrations or enable an import command. The prepared plan applies stages 3–8 in one fenced transaction per conversation: events and relations, sidecar material records, Honcho messages, legacy summary records and the remaining history. References and per-column sensitivity spans commit with their records. Unsealed events, relations and event spans live in `import_event`, `import_event_relation` and `import_event_span`, outside `conv_event` and every native evidence view. The later seal publishes those stable event identities into the live ledger; staging alone cannot feed live reads or person-memory extraction. Legacy summaries remain outside the native summary tree, and histories never create memory. Cross-conversation links order dependent conversations after their targets; a dependency cycle is refused before writes. A failed conversation rolls back its records, references and receipt, while earlier committed conversations can be resumed.

A `legacy_ref` stores the full source namespace, canonical row hash, original batch/time, preserved typed fields and the name of any field mapped to the canonical record body. The reverse mapper combines those fields with the adopted body; it reads neither the source nor a cached snapshot. SQLite NULL, integer, real, text and blob remain distinct; text/blob bytes use base64 inside the typed representation, integers retain decimal precision, and the canonical hash is SHA-256 over sorted-key JSON. Reconciliation checks every mapped row in both directions and reports per-table snapshot rows, mapped rows, distinct destinations and references. Excluded indexes, views and operational tables retain their counts and omission reasons. An unchanged conversation manifest returns without writes, even under another batch id; changed source rows, attribution or destinations conflict rather than silently rewriting an imported range.

Present sidecars require the Core-owned assets material writer. Preparation syncs the verified bytes and a recovery marker; adoption inserts native material metadata and links its identity/revision to the staged output event in the same transaction. Original source fields remain reversible. The host closes the plan after its enclosing transactions; rollback removes unadopted bytes, and committed native material reads verify the stored digest. Repeating an already adopted conversation discards unused preparations and adds no material. Missing sidecars remain explicitly unavailable.

The deterministic Moirai tasks carry only a conversation identity and manifest hash. Core's registered `ApplyJob` validates the task and worker echo against its prepared plan, committing the conversation with the existing job receipt and watermark. The M1 import scenario uses the real worker and Unix job service. No model or source credential reaches the worker. Live Honcho page decoding remains gated on the missing registered API description; internal synthetic adapter records exercise the Honcho stages without bypassing that gate. Staged ranges are not queued for the native search backfiller. Final verification, publication through `import_sealed`, a backup and optional promotion remain the next import increment (#118), not a consequence of a successful staging job.

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

The Telegram adapter uses the pinned `gotgbot/v2` client for individual API calls;
LINA owns the polling loop. Construction is local and starts no platform request.
Main runs `Adapter.Run` under its lifecycle context independently of READY. A
25-second long poll has a 35-second request deadline; read failures use cancellable
1–30-second backoff and respect retry-after. The next offset is acknowledged only
after admission commits. A failed admission stops that poller so a restart reads
the durable cursor. One main poller may run for a numeric bot account at a time.

Core installs `telegram.Schema` after `channels.Schema`. Approval callbacks use a
dedicated inbox source and a small pending-button table; their inbox row, pending
marker and cursor commit together and never dispatch a conversation turn.
Recovery rechecks the epoch, current paired numeric sender, binding and tools
scope and the callback message's originating bot before asking authority to
consume a pending approval. `PresentApproval` durably binds the returned platform
message ID, bot, chat and topic to the canonical request. A callback must match
that presentation; altered callback data cannot answer a different request. A
failed presentation commit leaves the buttons unusable and returns an uncertain
outcome without resending. A non-owner or foreign bot's button cannot
answer the approval or remove its buttons. The accepted decision records the
paired person and the actual chat/message IDs. Typed text never takes this path.

The adapter renders Markdown as native Telegram entities, preserves fenced code
across shaped parts, and keeps approval arguments literal. Its send/edit cooldown
honors the full platform flood wait while the gateway queue owns durable retry
deadlines and per-part receipts. Only an explicit formatting refusal allows a
plain fallback; unknown outcomes are never resent. Edits retain their message
identity, and streaming remains off. Poll conflicts have a bounded retry budget;
bot/self/anonymous-authored input cannot start reply loops.

The library receives a placeholder credential. A private HTTPS transport inserts
the secret token only for Telegram's fixed API origin, refuses redirects and
returns sanitized errors. Decoded token echoes are masked before admission.
Tokens are never process environment values, event fields or ledger input.

The #97 package increment provides `gateway.Adapter`, `channels.Store` and `gateway.Queue`. The composition root installs `channels.Schema` in a numbered canon migration after the agent and inbox tables. The channel dispatch extends `agent_bindings` with a cascading foreign key, so agent trash removes dispatches in that same transaction and restore never revives them. Binding changes record the owner, agent and scope. The most specific topic binding wins; the delivery also retains the original topic when it uses its parent binding.

Connections start denied. Pairing uses a random 128-bit code, stores only its SHA-256 hash, expires after five minutes, and consumes it once. Platform identities are canonical positive decimal user ids. Pairing cannot silently replace an already paired identity. This increment implements the default owner-only admission mode; the optional addressed external-participant mode remains for its consuming flow.

`Gateway.Admit` commits the inbox row, a bot/native-event receipt and the adapter cursor together before calling the turn/steering dispatcher. The adapter supplies both its previously committed cursor and its next opaque cursor; a stale cursor rolls back the entire transaction. Duplicate native events are acknowledged without moving the cursor or dispatching, even after a binding changes. Denied input advances the receipt and cursor without creating an inbox row. A dispatcher error or crash after commit never causes automatic input replay; stored input remains available to recovery. Turn claims and interrupted-turn notices belong to the turn/lane coordinator.

The outbound queue retains the ledger reference and adapter-shaped parts. It requires a committed agent ledger event for the bound agent and conversation, and verifies the current pairing, binding and an admitted reply source, or explicit may-start scope, and serializes sends across topics within a bot/chat. A hard lease encloses a durable `sending` claim, one network call, and settlement. Each claim has a monotonic attempt number. Only an explicit refusal before delivery makes a part retryable at its persisted retry-after time; successfully sent parts are not repeated. Missing or ambiguous outcomes, transport errors, and `sending` rows found after fencing the old epoch become terminal `unknown`. A blocked chat marks all its bindings unreachable and stops its pending sends. The caller runs queue recovery once before starting adapters.

Package scenarios use real canon and SIGKILL barriers before admission commit, after commit and during a send. This increment adds no TUI or Core-protocol operation; platform pollers, credentials, channel approval interactions, live turn composition and app delivery views attach in their owning increments. No channel scenario can yet be driven through the #34 protocol harness.

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
- The waiting producer interface returns candidate keys and re-reads each source within one canon transaction. A completed source is marked handled and omitted; a missing source is omitted; other source errors refuse the snapshot. Sources supply only ask-only items addressed to this person. Source references and revisions remain outside the display-only `view.Waiting` type. An answer re-checks ownership, revision and the source's answer mapping, then commits the source change and one decision with the person, requesting agent and answer together. Existing approval records are read through `waiting.ApprovalSource`; their answers still pass through the authority service with its trusted decider, and its attributed record is the decision rather than a second approval log. `waiting.RunInputSource` reads `needs_input` generations and delegates the answer to the run owner, which records the person and agent with the follow-up before resuming. A repeated identical answer returns that same decision. A repeated answer cannot create a second decision.
- `runs.RunningList` combines `runs.RunListSource` over canonical run records and delegated-task producers, re-reading their current records. Input-needed rows come first, then complete or partial results ready for review, then other in-progress, waiting and landed rows. Waiting rows carry one scheduler-owned reason; unknown states or unexplained waiting fail the snapshot. Opening or dismissing a landed row records its removal from the list in canon without deleting its source or result; opening live work keeps it visible.
- Run landings carry a durable generation key and landing instant; `runs.TransferNotices` acknowledges a generation only after the lane outbox accepts it durably. Question and landing generations remain distinct even at the same clock instant. The lane masks the summary before persistence and owns a durable notice outbox and serializes delivery with the whole conversation turn. Already queued person input precedes a notice. Pending landings in the same conversation and ownership scope, at most 10 seconds from the first landing (inclusive), form one event; an idle lane delivers immediately. A delivery commits its conversation event and outbox acknowledgement together before subscriber publication. A failed commit retains the pending notices for explicit recovery and never retries in a loop. The display rows and notices are Core APIs in this increment; client 1.0 waiting/running payloads remain reserved until their surface implementation.
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

Restore prepares a private copy before publishing any area. Removal replay and authority invalidation finish in that copy: pending effects become unknown, captured approvals and standing authority become unusable, durable claims are cleared, and account metadata requires sign-in. Terminal effect receipts survive unchanged. The restored canon epoch is the maximum of the captured epoch, the target canon epoch and its control-file epoch; an exhausted epoch is refused. The next Core start mints a greater epoch through the ordinary startup path. Restore does not replace the target identity, epoch or lifecycle records. It validates a halt marker before proceeding and durably clears that latch only after successful reconciliation, before reopening access. A corrupt target canon can be replaced using its independent, valid nonzero control epoch and the validated removal history; invalid epoch records fail closed.

A pending publication lives under `staging/restore.pending`. Core checks for it while holding its lock, before opening services or minting an epoch, and refuses startup until the restore is completed. Publication saves the replaced areas, installs the replayed removal list and all captured areas, and installs canon last. Each rename and its parent directories are synced. Repeating restore resumes an interrupted publication, reacquires the installed source's lock and reapplies its latest removal history before retiring the marker. Both Core and the backup command refuse a pending publication. The marker is retired only after the whole generation is durable.
`lina backup --state-root <path>` requires Core to be stopped, holds the stable exclusive Core lock through publication, and refuses a running Core without writing state while explaining how to stop it (`systemctl --user stop lina-core.service`, or Ctrl+C for a foreground Core).

The `backup` flow requires the exclusive Core lock and quiesced writers. It reconciles the removal journal before capturing canon, uses the online SQLite backup API, verifies all recorded material revisions and copied version-space, derived and workspace files, then publishes `manifest.json` last. Its schema-1 manifest records artifact/schema versions, capture epoch and revocation watermark as well as the complete inventory. A killed copy has no published manifest; hash, journal, cancellation and I/O failures publish no generation. Restore remains a separate whole-generation operation.

**C-33 Diagnosis (kept).** The `kit/diag` emitter takes closed enums and opaque ids only, so a free-text, path or payload field cannot be written ([self-diagnosis-log.md](self-diagnosis-log.md#event-schema)). Gateway events are written under `conversation`. Events go to `system/events/` through their own writer, never the canon writer. Failure, unknown, denied and cancelled results commit synchronously. A chain root exists per boot, turn and task. A preallocated reserve lets a failure commit on a full disk. `diag.VerifyCitations` is the test oracle for the model, interruption and storage injections. Tokens, cost and latency live in the canon `usage` table, not in events.

The #41 package increment implements the closed emitter, strict twelve-field codec, boot/turn/task root constructors, query filters and citation oracle. `internal/core/eventstore` takes a caller-supplied `database/sql.DB` for a dedicated SQLite event database and a typed full-disk classifier. It requests WAL/FULL on one reserved connection, commits every append and releases a separately allocated reserve for one full-disk retry. [The diagnosis contract](self-diagnosis-log.md#storage) defines its ownership, query and reserve behavior. The SQLite binding remains #39 work; package and scripted SQL tests do not establish reopen/crash durability or an actual full-disk commit. Core startup, flow roots and the conversation query tool attach in their consuming issues; this increment changes no protocol or TUI operation.

## Contract change proposals

The owner direction is already in the contracts and is not a proposal. The Go design found five gaps. The owner accepted all five on 2026-10-20, and the text of each now lives in the contract named for it. The ids stay.

**CP-01. Accepted 2026-10-20.** [main-authority.md](main-authority.md#terms), Epoch: the main stores a new epoch durably before it dispatches, issues or accepts anything. C-04 takes the lock and opens the event writer first and dispatches nothing before the epoch is durable, as [runtime.md](runtime.md#recovery) says. The coordinator binds the event epoch only after the atomic epoch write and directory fsync and opens canon afterward.

**CP-02. Accepted 2026-10-20.** [host-protocol.md](host-protocol.md#envelope), `epoch` row: an unsigned 64-bit integer written as a decimal string without leading zeros and compared numerically. The epoch record, the sentinel, the cursors of C-42 and the fixtures use this form. The epoch file has no trailing newline; malformed or exhausted records fail closed instead of resetting the counter. Client sessions parse the canonical decimal directly as `uint64`, including its maximum value; requests must carry the welcomed epoch and never pass through a floating-point conversion.

Topic cursors validate the same canonical epoch representation. Their sequence is an unsigned 64-bit counter starting at zero for the empty topic; the first committed frame is one and exhaustion refuses further publication instead of wrapping. Receivers compare epochs numerically, ignore already-applied or older-epoch frames, and latch a sequence gap until a snapshot is installed. A snapshot cannot move behind an applied cursor or an already-observed newer epoch. Replaying a retained frame twice therefore applies its content only once.

**CP-03. Accepted 2026-10-20.** [filesystem.md](filesystem.md#state-root-and-layout), `system/` row: LINA Core's control files live under `system/core/` (lock, epoch record, run sentinel, halt marker, restart request, main id), are written by LINA Core, are never in a backup generation and are preserved by a restore, except that C-04's verified recovery clears the halt latch. The epoch must not fall after a restore, so it does not live only in canon. Orderly shutdown removes only the run sentinel and socket; it retains the epoch and the stable lock inode.

**CP-04. Accepted 2026-10-20.** [host-protocol.md](host-protocol.md#envelope), `idempotency_key` row: for an effect asked inside a conversation turn the task component is the turn id. The effect ledger scopes the key by task id or by turn id. `turn.EffectKey` hashes an unambiguous tuple of that scope, action/tool name, schema-normalized target and stable call id (the intent). The key excludes the boot epoch; authority binds the arguments and actor identity separately, returns a stored receipt for an identical key, and never repeats an unknown effect.

**CP-05. Accepted 2026-10-20.** [materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result): the idempotency key of a result save is built from the result id, the revision, the action and the destination, as [host-protocol.md](host-protocol.md#typed-results) says. C-52 uses the key of `result_save`. The #104 implementation hashes the unambiguous tuple of result id, revision, `result_save` and the normalized destination; call ids, turns and boot epochs do not change it. A committed claim is never dispatched twice, including when its outcome is unknown.

**Open points at the Go level**, with the default in brackets: the SQLite binding (V2); the Git backend for the records repository (a pure-Go library, measured with thousands of files); the Telegram and Discord client libraries; whether `moirai-worker` reads canon directly or through the job API at scale; concurrent turns per Claude account (2).

**Not designed here:** the LINA app (a private repository), the library viewers and meeting notes, the Google plugin, Jev, Klotho supervision and the QA map, the news feed, LIFE, the launcher, OpenViking, and the mobile host.

## Decision index

Titles are the headings above. Earlier ids C-01 to C-42 keep their number where the subject survives.

- Kept: C-03 to C-07, C-15, C-16, C-19, C-26, C-32, C-33, C-38, C-40.
- Changed: C-01, C-02, C-08 to C-14, C-17, C-18, C-20 to C-25, C-27 to C-30, C-34, C-35, C-39, C-42. Contract decisions replace the earlier ones. The main cases: the Moirai worker is a separate service, there is no hidden sandbox mode, no automatic replay, no automatic retries by Core, six tool classes and per-agent ceilings withdrawn, workers unconfined, survival tier withdrawn, key pinning withdrawn.
- Withdrawn: C-31 (confirmation list; the approval rule decides), C-36 (desk and work Mac; placement is not a contract), C-37 (model proxy instance; the proxy is removed), C-41 (placement across machines; parallel hand-off decides).
- New: C-43 to C-59.
