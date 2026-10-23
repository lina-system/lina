# Worker recovery implementation

Contract: [Stall and failure](../../../docs/design/work-and-delegation.md#stall-and-failure).

The #135 recovery path persists worker bindings and the one continuation allowance
in canon. Installed Core marks unfinished sessions unknown before
admission, resumes their existing Codex thread and reads the exact turn, paging
its history without starting another turn. Missing or incomplete items, lost
requests, failed reads and effects without independent evidence remain unknown.
Thread completion alone cannot settle a command's external effects. A confirmed worker failure stays visible as Failed in running and progress views while its effects remain unknown; this observation does not release its directory reservation or authorize input. The event
path and a five-minute safety sweep share this reconciliation. The sweep makes
no model call. Confirmed successful completion evidence enters the normal result inbox;
its conversation response is a separate reaction and never starts a worker turn. A continuation intent commits before input; an uncertain send is
never repeated, including after restart. The same task, thread, directory and
prior receipts are retained. A second settled failure becomes a durable decision
in Waiting for me; keeping it paused or stopping it grants no further execution.
The first observed failure retains its normal conversation result response, with execution tools withheld. That response cannot start another worker; the recovery controller still owns the single continuation. A second failed attempt uses the decision row without an autonomous model response. A person cancellation disables automatic recovery. Restoring a backup disables captured recovery and queued dispatch authority while preserving observations and receipts. All adapters reject interrupt
reasons outside user, budget, Node policy and the stall rule.

Headless dispatch commits terminal snapshots, attempt-specific receipts and the next recovery state in one canon transaction. Codex dispatch retains the native thread and exact turn before work bookkeeping, so restart can observe the original attempt and recover its real worktree result without launching a turn. Recovery revalidates the originating channel and goal scope; quota replacements do not consume the one ordinary recovery. A
complete failure with no observed tool activity can release one new attempt in
the existing worktree, without resetting review or CI allowances. Tool activity
or a cut stream retains unknown. Claude and Antigravity provide no read-only
persisted-thread API: absent a committed terminal snapshot, restarting Core
cannot manufacture one or replay their commands. Their second settled failure
uses the same durable decision surface. `e2e/m2/worker-recovery.json` covers
SIGKILL, read-only settlement, the once-only continuation, unknown effects and
restart persistence; `e2e/m2/headless-worker-recovery.json` covers Claude dispatch, and `e2e/m2/work-dispatch-recovery.json` covers a dispatched Codex task producing a file, commit and pull request before Core dies.

#135 commits a failed attempt's terminal observation and recovery reservation together. A completion from that old attempt cannot deliver the next attempt's result. Unknown effects retain their reservation; the second settled failure creates a decision. A reconciled success enters the normal result inbox without another worker turn.

#930 admits a goal worker's committed recovery through the dispatch scheduler without requiring a conversation `turn_calls` receipt. The existing goal-action authorization and once-only recovery allowance still gate the attempt. `e2e/m2/goal-worker-recovery.json` covers a recovered result and a second failure through installed Core.

#995 keeps conversation-worker output by full worker thread identity in the
live observer. A delayed completion consumes only its own output, including
when reconciliation already delivered that result. The installed-Core scenario
`TestM2WorkerDelayedCompletionOutput` recovers one turn, continues the worker,
and delivers the old completion between the new output and completion. It
checks both stored results, file readback and result retention after restart.
