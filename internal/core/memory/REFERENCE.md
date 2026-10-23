# Memory jobs: behavioral port specification

Reference: Honcho revision `607438d20c408575d1c9257f325c4885da871e82`.
Paths below are relative to that upstream tree. Server code is a behavioral
reference only; no server implementation is copied. LINA's contracts take
precedence over differences in the reference.

- **Seen** (`hermes-plugin-honcho/tests/test_turn_author_peers.py:67-107`):
  bot authors remain separate from the human even when names or pins match.
  LINA checks canonical speaker and person attribution before extraction and
  again at Apply; summaries, automation and agent text cannot be person evidence.
- **Seen** (`src/deriver/queue_manager.py:327-386,654-709`): derivation consumes
  bounded batches. **Seen** (`:576-584`): upstream retries are at least once.
  **Inferred mapping**: LINA groups consecutive person events in a configurable
  short window, uses immutable tasks and commits progress with idempotent Apply.
  An empty eligible batch is not scheduled for a model call.
- **Seen** (`hermes-plugin-honcho/tests/test_async_memory.py:763-821`): racing
  flushes send a batch once and independent sessions can progress independently.
  **Inferred mapping**: source lineage counts once, equal common statements from
  different agents merge evidence, and job receipts prevent duplicate application.
- **Seen** (`hermes-plugin-honcho/dialectic.py:53-56,96-102`): effective cadence
  is `min(base + empty_streak, base * 8)`; a nonempty result resets the streak.
  **Seen** (`:110-120`): discard results when turn age exceeds `base * 2`.
  LINA uses explicit turn counters, never sleeping tests.
- **Seen** (`hermes-plugin-honcho/dialectic.py:67-95`): the public client rejects
  publication by superseded runs. **Unknown**: the installed client revision is
  unavailable in this checkout. The design records that its token guard differs.
  LINA requires generation, current evidence revisions and revocation epoch
  checks regardless; a reader only consumes a ready, topic-matching result.
- **Inferred / LINA contract**: a brief is derived state with a citation on every
  sentence. It never creates a fact. Exact duplicate merging never crosses
  person, subject, holder, scope, kind or validity boundaries. A three-statement
  supersession chain exposes only the current statement in ordinary recall.
- **Inferred / LINA contract**: maintenance records editable preauthorization,
  rechecks it at commit, proposes everything else with an expiry, and produces
  no notice when no item or proposal changed. Normalized equality can be merged;
  token-overlap clusters are proposals, not authority to equate two statements.
- **Unknown / not ported here**: upstream model quality, embedding thresholds,
  and LCM-X compaction behavior. This increment adds no compaction or LCM-X port.

Verification uses synthetic sources, the real fenced memory store and Core job
service, plus a pure cadence comparison with the reference implementation.

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-21 recall acceptance

These operations are callable Core/tool services, tested with real canon and ledger package scenarios.
Explicit client memory actions also have installed-Core acceptance through the [TUI memory requests](../../../docs/design/tui.md#memory-and-agent-command-requests); the condition-by-condition evidence is listed under [G1-13](../../../docs/design/go-roadmap.md#1d-memory-context-and-skills).

### C-21 memory job interfaces

The memory-job increment exposes Core-owned preparation, pre-dispatch checks and `ExtractionApply`, `MaintenanceApply` and `BriefApply` callbacks in `internal/core/memory`.
They register with the existing job API; the worker still reads immutable tasks and submits their results through `Apply`.
The composing schema owner installs `memory.JobsSchema` alongside `jobs.Schema` and the shared budget schema in a numbered migration.
Worker startup never migrates canon.
These are package and job-socket capabilities; installation and conversation composition select their schedules and model bindings.
No new client command or TUI memory screen is introduced.

### C-21 memory job acceptance

Synthetic package tests and `e2e/m1/memory-jobs.json` exercise these operations with the real worker binary and Core job socket; model quality and installed scheduling remain composition concerns.
