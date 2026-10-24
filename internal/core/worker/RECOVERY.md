# Worker recovery implementation

Contract: [Stall and failure](../../../docs/design/work-and-delegation.md#stall-and-failure).

The #135 recovery path persists worker bindings and the one continuation allowance
in canon migration 52. Installed Core marks unfinished sessions unknown before
admission, resumes their existing Codex thread and reads the exact turn, paging
its history without starting another turn. Missing or incomplete items, lost
requests, failed reads and effects without independent evidence remain unknown.
Thread completion alone cannot settle a command's external effects. A confirmed worker failure stays visible as Failed in running and progress views while its effects remain unknown; this observation does not release its directory reservation or authorize input. The event
path and a five-minute safety sweep share this reconciliation. The sweep makes
no model call. Transport-loss notices and confirmed terminal results have
separate durable identities and are deduplicated independently. Previously stored
notices remain immutable when a confirmed result is admitted under the published
`:terminal` identity. A late loss notice cannot hide a recorded terminal result.
Confirmed successful completion evidence enters the normal result inbox;
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

#999 checkpoints conversation-worker input before dispatch and after the adapter
returns its binding. A pending input uses the existing unknown record with no
exact turn, retaining its reservation and retry allowance. This also covers an
active snapshot that becomes idle before the adapter receives input. A refused
input restores the earlier record; uncertain input is never replayed. An intent
without an exact turn remains held after restart. The separate
`e2e/m2/worker-followup-recovery.json` scenario kills installed Core after a
follow-up produces a file without notifications: an acknowledged turn is read
back and its result delivered once, while a missing reply retains uncertainty.

#1132 uses Codex's input admission callback under the adapter control lock to
separate steering from a new turn, including completion between Core's first
snapshot and input. Steering retains its exact turn, reservation and activity
even when the reply, post-input snapshot or checkpoint fails. A definitive
refusal restores the prior record. Adapters without admission callbacks retain
the conservative new-turn intent. The follow-up recovery scenario also kills
installed Core after steering without a reply or after a failed snapshot, then
verifies exact-turn readback, one result delivery, reservation release and no
replayed input. Model and worker endpoints are synthetic; Core, files and SQLite
are real.

## Terminal text-call recovery

ACP and Codex use `worker.DetectTextCall` over the shared `textcall` evaluator.
`TurnText` keeps assistant fragments private until the terminal reply, replaces
Codex complete items over their deltas, and bounds memory. Native tool evidence
(including MCP bridge admission) prevents replay. Thought and plan activity keeps
its existing path. Ordinary assistant text is released once at completion.

A complete permitted call emits the existing `ToolCallEvent`; Core supplies the
caller identity and executes its ordinary work-tool handler. ACP uses its session
MCP bridge internally. Codex retains a local pending request and consumes Core's
answer without writing an unsolicited JSON-RPC reply to the worker. Both delay
completion until that answer, reject failed results and discard the raw text.
Codex snapshot/reconciliation of a handled turn retains this local outcome rather
than reading the contaminated transcript back into output.

An unrecoverable call ends with `text_tool_call`. Dispatch persists `fresh_session`
with its recovery intent and pins the same worker even when initial assignment
used the chooser. Start bypasses ACP resume/load and Codex resume. Existing files,
branch, task definition and recovery allowance survive; the packet excludes the
contaminated assistant turn. The second failure reaches the existing decision
projection. Adapters without session work-tool support receive no declarations.

`TestWorkerTextCallsThroughRunningCore` in `e2e/m4/text_tool_calls_test.go` exercises
both protocols through a running Core, real Git worktrees, tool handlers, canon
and process supervision. Synthetic workers cover coordination, underscore inbox
and receipt calls, incomplete/internal calls, fresh-session recovery, and the
second failure. No provider calls or private transcripts are used.
