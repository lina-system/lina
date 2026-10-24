# internal/core/boot implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-01 control-file increment

The control-file increment (#36) starts with `lina core --state-root <path> --socket <path>`.
The state root must already exist, and installation supplies `system/core/main.json`; Core never invents a replacement identity.
`lina install` verifies the build bundle, creates a random main id in a new private state root, and registers the `lina-core.service` user unit with explicit state-root and socket arguments.
Existing state and services are refused, including an install's own previous identity; installation is not an update or implicit restart.
The concrete layout and failure behavior are specified in [runtime.md](../../../docs/design/runtime.md#the-lina-command).

### C-04 startup increment

The #36 startup coordinator executes these boundaries in order.
Event writer, read-only canon peek, canon open/probe/migrate, epoch mirror and reconciliation have explicit service interfaces for their owning implementations.
In this increment the CLI has no event writer or canon implementation and serves only the handshake; an absent or empty canon directory is allowed, but an existing canon store without its service is refused before minting an epoch.
The coordinator passes a null epoch to startup events by leaving the event writer unbound until step 5; it supplies the new epoch only after the file and directory fsyncs succeed.
Canon's writer cannot open before that boundary.

### Live conversation compaction (#1114)

The turn owns `compaction.Session`; its resolver checks appended provider items
against `turn_items`, `turn_calls` and admitted steering. Builder callbacks remove
only the protected items still owned by the turn journal from the conversation
ledger frontier. Completed events keep their existing atomic publication path.
The adapter's window/counter pair drives assembly, fitting and hard admission.

After delivery, `conversation_preparation.go` enqueues a snapshot through the
existing Core jobs service. The private worker socket resolves conversation jobs
and retains at most eight volatile builder snapshots. Aux/Apply receipts contain
only tickets. Foreground publication rechecks configuration, sources and epoch;
restart discards speculative text. No periodic producer or extra process starts.

Request-size audits and an oversized turn's continuation marker contain no
conversation text. The marker, diagnostic sizes and terminal outcome commit in
one transaction. The existing ordered-prefix contract rejects summarizing items
after a retained input; that case remains a recorded refusal, with no fabricated
summary or incomplete reasoning round trip. Its system notice and bounded `item_done.details` restore from canon; diagnostics do not create authored result cards. Outcome reads use a context independent of turn cancellation.

`e2e/m2/conversation_compaction_test.go` exercises the installed Core, real tools,
the worker socket, summary publication, the retained tail and restart using a
synthetic provider. Focused boot regressions cover follow-up fitting, adapter
budgets, committed preparation adoption and refused continuation diagnostics.

After integrating provider capability selection, live fitting recomputes cache
boundaries from retained ledger records. Auxiliary preparation fingerprints
exclude foreground input positions. Steering's input-binding evidence remains
protected transient context and cannot become summary source text. A summary's
bounded value-condensation call contributes to the same preparation's reported
usage; missing usage remains unknown.

### Request and outcome regression repair (#1226)

New request evidence uses `persona-tui-work-method-v2`; v1 evidence rebuilds
with its original full-schema instructions. The enabled-tool instruction block
carries names; native declarations retain
complete schemas. The catalog transform recognizes older full-declaration
blocks when replaying recorded requests. Loader receipts and declaration order
remain unchanged. Large committed authored results survive a later request-size
refusal, with the diagnostic kept as a separate system notice. The TUI prefers
the authored result by default and exposes notice Details through card navigation.

### Throwaway test runtimes

`startConversation` takes test-only `runtimeOption` values. `throwawayState()` opens canon with `Config.NoSync`, agents with `NoSync()` and records with `NoSync()` for roots the test deletes, so fixture setup stops paying a disk sync per starter revision or record commit. `boot.Config.NoSync` is the test seam that selects it: the boot tests set it for their temporary state roots, `cmd/lina` never sets it, and no argument or environment variable reaches it. `ReplayIntakeCase` passes it directly because it deletes its own private root before returning. Tests that need durable canon or agents behaviour live in those packages and do not use the option.
