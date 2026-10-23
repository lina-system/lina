# internal/core/worker/dispatch implementation notes

## Implementation notes

Design authority: [Go Core](../../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-29 dispatch integration

`internal/core/worker/dispatch` wires the adapter into installed Core's `dispatch_work` tool and existing work ledger.
Migration 31 adds the dispatch journal; a committed conversation tool receipt gates execution.
Identical retries load the durable admission before any manager handoff, preserving older manager definitions and ownership without resetting execution state.
The journal admits queued work independently and reserves intersecting write areas transactionally at handoff; unknown execution retains its reservation.
Core allocates through its shared `work.Area`, records the pinned workspace on the task and starts the worker in `work/tasks/<task-id>` from the detached base.
The independent parent clone keeps the GitHub origin for worker publishing and the local allocation source separately.
Adapter observations live outside the checkout in `work/runs/`; receipt provenance names those files.
Core checks design references, protected refs and write areas, and persists an adapter-derived artifact/receipt.
Terminal observations publish to the source conversation and survive restart.
Recovery retains uncertain reservations without replay.
The adapter does not advertise unsupported approval replies, steering or work tools; manager design acceptance, parallel scheduling and merge remain their own flows.
See [Antigravity headless adapter](../../../../docs/design/work-and-delegation.md#antigravity-headless-adapter) for the evidence boundary and account-free acceptance limits.

### C-57 chooser integration

#138 connects the chooser to `dispatch_work` with Antigravity and Codex.
The optional `system/core/worker-choice.json` table has `provisional: true` and `rules` of `{worker, tags, priority}`; all tags must match the definition's kind or repository.
Quota and capacity cannot be overridden.
Equal candidates retain installation order (Antigravity, then Codex).
Core reads the table at startup and rejects malformed rules.
The initial resource estimate is one CPU, 512 MiB memory and 1024 MiB disk per active reservation, deducted from the measured main-host resources.
Canon migration 43 replaces the repository-wide unique index with fenced area claims and durable worker quota.
A late quota snapshot cannot erase a newer observed stop; admission rechecks quota before reserving the attempt.

### C-58 correction delivery

#144 connects the completion reactor's dispatch delivery to that judgment.
A missed work definition queues one new generation of the same task, branch, worktree and worker.
Its packet contains the unmet definition and only P0/P1 finding records, retaining their ids; P2/P3 stay in the review record.
The return receipt and generation offset commit before launch.
Completed worker processes are restarted with the retained packet; Core does not claim to resume a live session after its process was joined.
Each generation retains its own result artifact.
A stale completion cannot overwrite the queued correction.
An unchanged corrected head retains its previous failed check and closes against the new receipt without a second review.
