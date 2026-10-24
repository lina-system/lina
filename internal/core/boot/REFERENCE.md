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

### Binding chip and moves (#788)

`boundChip` reads the admission decision and the append-only choices, so the
chip follows a model, tool or person decision. `ChipFor` shows it only when the
final target is not the fresh focus. Live input, history, channel replies and
every later tool call republish the person's `item_done` with the chip.

`binding_effects` keeps what a bound turn did, in call order: a created record's
id, an edit's previous bytes and revision, a steer's text and target, and work
that started (marked as having left the person). `moveBinding` validates the
chosen target against the ranked offers and the current state, then re-attaches
each effect: a created to-do changes its parent project in one saved version, an
edit restores the wrong record and writes only the changed fields onto the right
one in a single records publication, including fields the edit cleared. A later
move reads the effect from where the previous move left it. The retained edit
records which fields changed, so a destination that already holds one of those
values does not drop it on the next move. A `worker_input`
steer stores the session's task, the same identity tool binding uses. A steer
sends one withdrawal and one delivery through the durable follow-up queue,
whose retained keys are its receipts. Effects that left the
person are listed with reason `left`.

`binding_moves` stores each move with its outcomes, the label (the chosen
target) and the state snapshot. `bindingStats` counts moves per hundred bindings
by decider, and `exportBindingCases` writes one valid intake case per move under
`derived/intake-cases/`; both read only canon, so a restart keeps them. The move and corrected input choice
commit together; retries return the retained receipt. Replay uses the original
input and the admission snapshot, without personal name memories.

### Live topic recall (#873)

`conversation_memory.go` collects ranked memory ids through `recall.Match`, then
loads their current scoped items and evidence in one `memory.Store.Items` read.
This runs in Atropos' existing optional collector with relationship context.
Standing profile/name/relationship items win duplicate identities. Only selected
topic references join pre-dispatch revision validation; `topic-memory-v1`
reconstructs the same projection on replay without a new search.

Replay also validates structured `recall` results against current source
identity, revision and citations during episode loading and before dispatch.
It walks canonical summary/record dependencies and retains checks for recorded
citations that a current source no longer carries. Legacy episodes bind missing
owner metadata through their canonical input rather than exported text.
Live replay recall sets the shared search's projection scope and checks each
result and citation through `replay.Source.RecallVisible`, reusing the export
source validator. Excluded results are dropped; storage errors fail the read.

`conversation_recall.go` binds one live family in the ordered tool preparation
table. It uses ordinary Core read grants, checks channel memory/tool scope and
agent availability on execution, and fences reads with the revocation epoch.
Expansion uses `compaction.Expansion` and the shared restriction set from
`recall.RestrictionCTE`. It rejects protected event chains, copies and summary
roots before returning a page, as well as missing material bytes. Replay
reads (including `recall_person`) use projection scope. New declarations remain present
through tool follow-ups. Old replay layouts and source tags stay unchanged.

Reference inspected before implementation: LCM-X `tools.py` at
`709099b3a397a7893a7abae8802ebe7bf605f79e`.

- **Seen**, `lcm_recall` at 3812: cross-conversation retrieval, positive bounded
  limits, explicit hard session exclusions before fusion, and a query deadline.
- **Seen**, `lcm_expand` at 4400: raw-message or summary-source expansion, explicit
  cross-session node scope, offsets and limits.
- **Inferred mapping**: LINA uses its shared deterministic search, person/agent
  authorization, exact source reader and immutable request evidence. It does
  not copy the reference's optional model reranker or eight-second budget.
- **Unknown**: the installed Hermes/plugin revision and owner-history retrieval
  quality on this machine. Local reference checkouts were absent; the pinned
  public source was inspected read-only. No reference code was copied.

`TestLiveTopicRecall`, `TestLiveTopicReplay` and
`TestLiveRecallScopeAndExpansion` cover preparation, negative matches, channel
scope, exact reconstruction, source revocation and tool authorization.
`TestM1LiveTopicRecallInstalledCore` drives a real installed Core with a scripted
provider through topic selection, recall, summary expansion, restart, export and
replay. Storebench's `topic_memory_read` reports the shared ranked-id and scoped
item retrieval latency; it does not measure full turn preparation or claim
owner-question recall quality.
