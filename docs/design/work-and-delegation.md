# Work and delegation

This contract fixes how LINA Core plans, delegates, supervises and accepts work. It covers the work engine and its ledger, the CRW contract schemas and fixtures the engine is built from, the work and knowledge records (the 목표 (goal), 일 (project), 할 일 (to-do), 계속 챙기는 것 (area) and 메모 (memo) of the person, with the to-do model), intent cards, parent and child roles, how every agent shares the work engine, background runs (LINA's own sub-agents, for research and analysis), workers (the coding tools the user installed, driven through worker adapters) and the task packet (the work definition), work tools and completion receipt they work with, the development method (the work definition, the choice of worker and the operation of the pull request), how work is split into tasks and handed off in parallel, how a finished worker is noticed by event and a dead or stalled one is recovered, how the worker owns a delegated pull request to green and the manager session checks it against the work definition at merge, how work handles effects with unknown outcomes, how results reach GitHub and get merged, and what the progress display shows. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). Finishing this document does not mean any part of it is implemented or accepted. Runtime proof belongs to the implementation issues that consume this contract.

## Scope

This contract covers:

- the work engine: queue, work threads, ledger, routing, capacity, budget and result acceptance
- agents and work: one queue, work area and set of workers for every agent, and the requesting agent on every task
- background runs: LINA's own sub-agents, how a conversation starts them, what they may touch, their limits, states, results and durability
- the CRW contract schemas and fixtures as fixed input, and the start gate they set
- the user-facing frame of work and knowledge (목표, 일, 할 일, 계속 챙기는 것, 자료 (resource), 보관 (archive), 메모), the 할 일 record with its personal and flow views, how these records are stored, and the intent-card data model
- supervisor, parent and child roles, delivery and verdicts
- the development method: the work definition, the choice of worker and the operation of the pull request
- splitting work into tasks, area leases, parallel hand-off, completion events, stall handling and the running list
- workers: the coding tools the user installed or signed in to (Codex, Claude Code, Antigravity and Devin), the adapter boundary, the Codex adapter, compatibility, readiness, assignment, control and approvals
- the task packet (the work definition handed to the worker), LINA's work tools and the completion receipt of a worker, the worker's review and CI fixes, and the manager session's check of a delivery against the work definition before Core merges or closes
- effects and unknown outcomes inside work
- GitHub work on delegated tasks: what the worker writes, and LINA Core's merge or close
- the progress display data (work cards, progress lines and the running list), observation, handover and discovery of work, and what the person is told of work: outcomes and decisions
- the operational switch from CRW

This contract does not cover, and links instead to:

- product families, canon, writers, agents and the role rule itself: [product-families.md](product-families.md)
- grants, epochs, receipts, observed results, authorization and the approval policy: [main-authority.md](main-authority.md)
- the envelope, delivery rules, effect states, idempotency keys, the `node` connection and the supported-combination table: [host-protocol.md](host-protocol.md)
- the language of LINA's components, installed tools, conversation tools, the effect ledger and the sandbox, the model path, usage accounting, packaging and the update order: [runtime.md](runtime.md)
- plugins and the GitHub plugin (identity, field-level canon, notifications, reconciliation): [integrations.md](integrations.md)
- how cards, plans and work cards are drawn: [surfaces.md](surfaces.md)
- Klotho's drafting, project supervision and the QA product map: [cognition-and-life.md](cognition-and-life.md)
- conversation threads, agents' conversations, the memory entry of verified results, the preset methodologies that use these records, and how an agent learns its person: [conversation-and-memory.md](conversation-and-memory.md)
- materials, their projections and passage anchors: [materials-and-knowledge.md](materials-and-knowledge.md)
- the state root, the work area's place in it, where versioned text lives, and the layout of the records repository: [filesystem.md](filesystem.md)

## Terms

**User-facing frame.** The person meets work and knowledge as 목표, 일, 할 일, 계속 챙기는 것, 자료 and 보관, with 메모 as the inbox. Plans defines each. PARA is the organizing idea, expressed as work units and never as directories: the main navigation is 일, 계속 챙기는 것, 자료 and 보관, with 목표 above 일 and 할 일 inside, and the inbox holds what is not linked to a unit yet. Every file, note, conversation result and person record hangs off one or more units. Opening an 일 shows its goal, done condition, 할 일, progress line and materials together on one page, which takes the place of a project folder. Moving between 일, 계속 챙기는 것, 자료 and 보관 (an 일 finished into 보관, a 자료 promoted to an 일) is a state change of the unit, never a file move. The other terms below, such as task, work thread, attempt and execution generation, belong to the work engine and do not appear in the default view. No surface shows an initiative, an issue, a cycle or a milestone as a LINA concept; a GitHub issue appears only as GitHub's own object, linked to a code 할 일 of a developer user.

**Task.** The work engine's record of one 할 일 handed to an executor. For code work a task is the hand-off unit: the work one worker finishes as one pull request, with one done condition, and its id names its worktree, its branch and its area lease. A task is not a second object for the person: the 할 일 is the object, and the task is its execution side (see the mapping under Plans). It references acceptance criteria, and it owns its work definition, its pull request, its execution record and the records that decided its verdict. It records its task owner, the person it is done for, and its requesting agent, the agent whose conversation asked for it, or none when the person started it directly ([main-authority.md](main-authority.md)).

**Write area.** The set of path globs a task may change, declared in its work definition. Two tasks with overlapping write areas are never in flight together.

**Area lease.** The work engine's hold on a task's write area, from hand-off until the task has landed, is closed or is abandoned.

**Completion event.** A record that a worker's turn ended, that it reported a typed failure, or that its process or session ended. It is a row in LINA Core's database, keyed by task, attempt and, when a pull request exists, its head SHA.

**Landed.** A code-change task has landed when a read of GitHub shows its pull request merged into the base branch. A task without a pull request has landed when its manager session judges it `verified` and LINA Core records the verdict. A background run has landed when it has ended with an outcome (complete, partial, failed or cancelled). A pull request that is closed unmerged is a closed task, not a landed one.

**Work thread.** The execution of one task by one executor: a worker thread, or a command sequence on a Node. A work thread is a different object from a conversation thread.

**Manager session.** The long-lived agent thread of the persona that owns a managed 목표 or 일 (project), one per managed unit. It holds the parent role, resumes across project events and makes the project's work judgments. It is not a per-task worker or a script. LINA Core records and enforces its decisions (see Supervisor, parent and child).

**Sub-agent.** One of LINA's own lightweight helpers: a bounded model loop inside LINA Core that does research, analysis, multi-file reading, drafting or a read-only check in the background for a conversation, on the person's model accounts. It has no persona, name, memory or conversation of its own. It is not an agent (a persona), not a worker (an installed coding tool) and not the subagents that a worker runs inside itself.

**Background run.** One execution of a sub-agent for one brief. It has its own record in the work ledger, its own usage and state, and a result (see Background runs).

**Attempt.** One run of a work thread. An attempt records the worker and its version, its thread and its turns, so that every card links to work through task, attempt, thread and turn.

**Execution generation.** A numbered round of a child's work on one task. A `needs_changes` verdict for a result that misses the work definition opens the next execution generation on the same child. Review and CI fixes belong to the worker's turn and do not open a generation through LINA Core.

**Binding epoch.** The generation of LINA Core's binding to a worker thread. A new binding epoch starts when control returns after a handover and when a remote thread is bound again.

**Protected ref.** A ref in the parent clone that a worker must never move: `dev`, `main` and the branches of other tasks.

**Task packet.** The first input a worker receives: the work definition (see Development method), the LINA assignment id, and how to return the completion receipt. It also carries references (id and revision) to the materials and memory projections the task may read.

**Completion receipt.** A receipt in the sense of [main-authority.md](main-authority.md), issued by a worker thread under its task's grant. It states that a deliverable is ready (for code work it names the pull request and its head SHA) or that an execution ended without one. A receipt is necessary for a judgment and never sufficient.

**Verdict.** The manager session's judgment on a completion receipt or on a delivery, recorded by LINA Core.

**Design-conformance review.** The manager session's check at merge that a code-change delivery meets its work definition: the done condition, scope and design reference. It runs as the owning persona on the person's model accounts, with its evidence and verdict recorded by LINA Core. Review findings and CI failures are handled inside the worker's turn (see Judgment and delivery).

**Apply record.** LINA Core's ledger record that a task's result was merged or otherwise applied, with the evidence that shows it, the agent that requested the change and, when the result was applied through an apply request, the person who accepted it. A pull request that LINA Core closes is recorded in the ledger with its reason and is not an apply record.

## Work engine

The work engine is part of LINA Core. It owns:

- the work queue and one work thread per delegated task
- the work ledger, append-only, and the task state projected from it
- capacity reservation, permissions and budget for work
- enforcement and recording of result acceptance
- the supervisor role and the records and lifecycle of the parent manager sessions
- the passage of questions, progress and results between conversations and work threads
- background runs of LINA's sub-agents, from their start to their landing

The work ledger is canon, and the work engine is its only writer. Moirai reads it by task id and never writes it. Codex thread records are never canon. Recovery starts from the work ledger, receipts and artifacts. The projection is rebuildable from the ledger at any time.

Rules:

- **One coordinator.** The manager session coordinates its project's work, and LINA Core enforces the coordination. LINA Core and a worker never manage the same sub-execution twice. A worker's own steps and its subagents are the worker's internal execution; LINA Core does not own their scheduling, retries or cancellation. The manager session judges deliveries. LINA uses each worker as one executor. It does not use a vendor's own coordinator (agent teams, managed child sessions) or the vendor's own worktree option.
- **Judgment and enforcement.** Judgment belongs to the manager session and comes from the ledger, GitHub's current records and its check against the work definition. LINA Core keeps the ledger, receipts, leases, bidding across Nodes, delivery with receipts, recovery and merge execution. It encodes no judgment about how to split, join or interpret work and starts no separate review thread of a worker.
- **Kind of work.** Conversation and work are split by the kind of work, not by tools. Work that needs a branch or worktree, outlasts a tool deadline, needs parallel execution, or is expected to exceed the turn budget ([runtime.md](runtime.md#turn-budget)) goes to the work engine, and the conversation hands it off at once and keeps going. The hand-off changes where the work runs and nothing else: its done condition and its checks are the same as they would have been in the turn. Checking a file, a short command or a document edit, within the turn budget, happens in the conversation with the conversation tools of [runtime.md](runtime.md).
- **Routing.** Coding work goes to a worker thread, and so does other worker work that needs a worktree, such as an audit of a repository. Research, analysis, multi-file reading, drafting and read-only checks are not worker work: they go to a background run (see Background runs). Their result is a cited brief whose citations carry passage anchors ([materials-and-knowledge.md](materials-and-knowledge.md)); it is the sub-agent's claim, not a `verified` result. Device work goes to the Node on that device. Worker and Node routes use the same grants, ids, effects and receipts. When these rules and the choice rules of Development method leave the destination or the worker open, Atropos may ask Jev to choose among the candidates that code fixes; the choice is a routing judgment, never permission to act ([cognition-and-life.md](cognition-and-life.md)).
- **Agents.** Every agent uses the same work queue, work area and workers. The work ledger records the requesting agent of every task, and the task's questions and results return to the conversation that asked, which belongs to that agent. When the requesting agent is deleted, its tasks and background runs keep running; their questions and results go to the person's home cards, and the person can hand them to another agent's conversation ([product-families.md](product-families.md)). A request that one agent makes of another is an agent exchange, not a task ([conversation-and-memory.md](conversation-and-memory.md#agent-exchanges)). When the answering agent needs work, it files a task as usual: that agent is the requesting agent, and the task's questions and results return to the exchange. A question there whose decision owner is the person raises the "needs the person" flag of the exchange.
- **Workspaces.** Only file-producing work gets a worktree. LINA Core creates one worktree per task from the base commit and starts the worker with it as its working directory. The worker creates the task's one branch there, with the name the work definition gives (it carries the task id), commits, pushes with its own GitHub setup and opens the pull request, so a file-producing work thread has exactly one branch and one pull request. Work with no file output runs in a non-git work directory in the work area and completes by its artifacts and receipts. A hosted worker (Devin) works in its own environment: LINA Core creates no worktree for it, and the work definition names the repository and the base. Where a repository has no GitHub remote (a local document project), the worker's branch and head SHA stand in for the pull request and the rest is the same, without checks to read. Finished and abandoned workspaces are cleaned up automatically after retention.
- **Draft and apply.** Only delegated results go through a draft and an apply request. A document or 목표 edited through its own conversation thread is saved at once as a saved version. For code work the draft is the worker's pull request. In development mode LINA Core applies it by merging after the manager session's check (see Judgment and delivery); an apply request that waits for the person is the form for parts without a repository. The work panel shows a delegated result as a `comparison` result ([host-protocol.md](host-protocol.md#typed-results)). Only an apply request can accept its hunks, and a result that no apply request covers is shown for reading only.
- **Profiles.** Each worker profile (direct, single, complementary) records its model, reasoning effort, tools, budget, session persistence, completion-signal capability (a protocol event, a process exit, or session polling), quota-signal capability and unsupported scope. A profile names a model or an effort only when it sets one, and then only for the threads it starts; it never changes the worker's provider settings (see Assignment). Choosing a profile is a proposal. The work engine decides whether the work runs.
- **Liveness.** The heartbeat interval H is the interval of the events the adapter receives from the worker; for a worker that is read at an interval (see Completion signals), H is that interval. A work thread is stale after max(3H, 90 s) without an event. Stale is only the floor of the stall rule in Event-driven flow; it does not act by itself. A worker lease lasts at most 5 minutes. An area lease is a different hold and lasts as long as its task is in flight. A background run has no worker events: LINA Core reads its progress itself, and a model request or tool call in flight counts as activity until its own deadline (see Background runs).
- **Budget.** LINA Core reserves and settles budget for every worker thread, including subagents inside the worker, and for every background run. At the cap the work engine sends `turn/interrupt` to a worker thread under the budget policy, and a background run waits with its checkpoint and says what is done and what is not. The cap is the person's own budget. No cap of time or of calls ends a background run (see Background runs). Codex's own goal token budget is never used. Usage accounting is in [runtime.md](runtime.md).
- **Optional configuration.** An installed worker is needed only for delegation. When it is missing or fails readiness, only delegation is unsupported. Conversation is never blocked by work, and the conversation and other tasks continue while work runs or waits. Background runs need no installed worker, only a signed-in model account.
- **Execution owner.** The work engine runs the code, work and skill creation that Klotho requests; Klotho's own limits are in [cognition-and-life.md](cognition-and-life.md).
- **Memory.** Only a result with a `verified` verdict may enter LINA memory, under the writing rules of [conversation-and-memory.md](conversation-and-memory.md). A verified work result is common knowledge, known to every agent, unless the person says otherwise.

### Run to done

Once the person gives a goal, the work engine owns it until its done condition
is proved. The conversation does not have to remind it of the next step. This
is the work engine's normal behavior, not a new user-facing concept or a
learning mechanism. The manager defines the goal, task graph, scope, checks
and required evidence up front; Core rejects a missing done condition or a
cyclic dependency graph. A code goal includes the whole applicable lifecycle:
plan → assign → build → green CI and clean review → merge → QA on the merged
result → deploy where there is a service → verify it actually runs.

Opening a PR or merging it is not goal completion. Each task retains its
worker attempt, current head, review and CI evidence, merge commit and QA
result. A service goal additionally names its deployment target, deployment
receipt and runtime verification evidence. Goals without a service record why
deployment is inapplicable and still verify their declared outcome. The final
short summary identifies the merged heads, QA results, deployment if applicable
and verification evidence, including a capture where the done condition calls
for one. Core closes only after all of these required evidence items exist,
refer to the current targets and report success. Claims, missing observations,
pending checks and unknown outcomes never substitute for evidence.

After every committed task, merge, worker or Node event, Core derives work
from the current ledger state. Event delivery does not tell it what to do and
cannot nominate an arbitrary command. The same derivation runs once at startup
and in the shared five-minute reconciliation sweep to recover lost wakes.

| Event and current state | Derived next action |
| --- | --- |
| Goal admitted with a valid task graph | Reserve compatible capacity and assign every ready task. |
| Dependency merged; a task has no other unmet dependency | Release its dependency wait immediately and fill a slot, independently of its predecessor's QA. |
| Worker finishes the current attempt | Read its receipt and current PR state; keep a named CI/review wait or submit the delivered head to the manager's work-definition check. |
| Current head has green required CI, a review with no open P0/P1 and a verified manager verdict | Merge that exact head through the merge slot. Re-read gates at dispatch if the head or observations changed. |
| PR conflicts with the base | Return it once to the same worker/work area, preserve files and receipts, and require the repaired head's CI and review handling before merging. A second failure needs a decision. |
| Worker process/session is confirmed dead or reports a recoverable failure | Reconcile outstanding effects, then resume once as a new attempt in the same work area. An observation timeout is not proof of death. |
| Merge is observed | Record the merge commit, release dependents and start QA for that exact commit once. |
| QA fails | Record the evidence and return repair work to the owner; its next merge needs fresh QA. Do not deploy the failing result. |
| All tasks have successful QA on their merged commits | Deploy the scoped service if required and authorized, otherwise verify the goal's outcome. |
| Deployment is observed successful | Verify the running target against the deployed revision, not just the command's exit code. |
| Verification proves every done condition | Close the goal and deliver one short evidence summary. |
| Node load/capacity changes or a slot is released | Recompute capacity and fill all compatible ready work, across Nodes. |
| A named wait expires or startup finds an in-flight action | Read its authoritative handle/receipt; resume from observed state. Never infer failure from elapsed time alone. |
| An effect needs the person or the goal is materially ambiguous | Retain one deduplicated decision with its wake condition; unrelated work continues. |

Each task is progressing, waiting on a named condition with a deadline, or
being recovered. A deadline triggers observation, not cancellation. Questions
are reserved for money, accounts, publishing/deployment or other irreversible
effects outside existing authority, missing information and real ambiguity in
the goal. Existing permission persists; no repeated approval is needed for a
previously authorized action. Exhausted recovery and failed evidence remain
visible in the running list and cannot close a goal.

A transition and its event commit together in the append-only work ledger.
Task state is a rebuildable projection. Derived actions have durable identities
bound to goal/task, attempt, stage and head or merge commit. Claiming an action
and reserving a slot are transactional. Effects execute outside the canon
transaction, with the existing grant/epoch fences and durable receipts. Event
replay and concurrent reconciliation cannot dispatch twice. A crash after
dispatch leaves an unknown outcome that must be observed before another action
can start; a restart never treats an unanswered receipt as permission to retry.
A later event remains eligible even if it arrived while an action was running.

Capacity comes from each connected Node's observed CPU load, available memory,
worker readiness/quota, resource caps and current reservations. Stale or unknown
load is not spare capacity; a yielding or disconnected Node gets no new work.
Core reserves resources before dispatch and considers all ready tasks in
stable dependency order. An area lease or lack of a compatible worker gives a
named wait. It does not withhold unrelated ready work, impose a fixed fleet
slot count or wait for a fresh person message to refill a released slot.
Capacity shrinkage stops new assignments without killing healthy work.

The implementation ports behavior from
[devflow](https://github.com/thisisjun786/devflow/tree/c78f378f652a985210e99228fa6467ea17f94c45):

- **Event-triggered filling and admission** (`lib/bidding.sh`). **Seen:**
  completion/failure/landing rebids queued work; admission reserves capacity and
  rejects unavailable workers and low resources; an unknown startup receipt
  prevents reassignment. **Inferred:** Core can derive the same actions from a
  transactional ledger. **Unknown:** this revision uses cores/8 for its slot
  ceiling and load in bidding scores; it does not prove fully load-sized slots.
  LINA computes available slots from live load and resource requests instead.
- **Post-merge QA** (`lib/qa.sh`). **Seen:** only an authorized landed job can
  enqueue QA; the running request is immutable and an uncertain interrupted
  effect is not blindly restarted. **Inferred:** merge evidence should key QA
  and its receipt. **Unknown:** devflow coalesces pending heads, which does not
  prove QA for every merge. LINA retains a QA obligation for each merge.
- **Merge and conflict recovery** (`lib/delivery.sh`, `lib/stack.sh`).
  **Seen:** landing is pinned to checked head/base and green checks; dependencies
  must really be merged into the base; a conflict is sent to its existing
  worker once. **Inferred:** recorded gates and attempt identities can drive
  the same sequence without a conversation reminder. **Unknown:** the read
  code does not establish an end-to-end deploy/runtime verification stage;
  those require LINA's explicit evidence and authority boundaries.
- [Kubernetes controllers](https://kubernetes.io/docs/concepts/architecture/controller/)
  and [Hermes P9](hermes-patterns.md#p9-events-over-polling).
  **Seen:** compare desired/current state; deterministic event identity,
  hand-off receipts and level-triggered reconciliation recover missed wakes.
  **Inferred:** event-driven next-action derivation can converge on the goal's
  done condition. **Unknown:** these sources do not prove safe retries for
  LINA's external effects. Its own receipts and grants remain authoritative.

Headless acceptance gives one code goal with five dependent tasks to stub
workers and sends no further person message. It kills one worker and introduces
one merge conflict, verifies recovery and continued capacity filling, and
requires merge, QA, deploy and runtime verification evidence before the goal
closes. Additional acceptance covers non-service goals, missing or stale
checks, restart, replay, unknown effects, capacity changes and a real decision
wait. No test relies on elapsed-time luck.

### Default posture

LINA acts and does not ask. When an agent knows what the person wants or what needs doing, it proceeds and shows the work as in progress, in the progress line and in the 할 일 row with its workers ([surfaces.md](surfaces.md#progress-at-a-glance)). The visible in-progress state is the confirmation. A question such as "should I start?" or "shall I run this in parallel?" is a defect.

- An agent asks only for an effect that reaches outside the person or cannot be undone (sending as the person, payment, permanent deletion, publishing; [main-authority.md](main-authority.md#approval)), for a choice that changes the outcome a lot, and for information it does not have. Each is a row in "Waiting for me" ([surfaces.md](surfaces.md#waiting-for-me)).
- Everything reversible is decided by the agent and reported in one line as what was done, with one action to undo it where the effect can be undone.

### Work area

The work area is `work/` under the state root ([filesystem.md](filesystem.md)). Every agent's tasks share it. Its layout:

| Relative path inside the work area | Holds | Writer |
| --- | --- | --- |
| `clones/<repository-id>/` | The parent clone of each repository that delegated work uses | LINA Core |
| `tasks/<task-id>/` | The task's worktree, or its non-git work directory | LINA Core creates it; the task's worker writes inside it under its grant |
| `runs/<run-id>/` | The scratch directory and transcript of a background run | LINA Core creates it; the run writes inside it under its run scope |

- The work area is not part of the backup generation. Everything needed to recover work is canon and LINA materials: the work ledger, receipts and artifacts.
- When a task without a pull request has a result ready, LINA Core keeps the output files as LINA materials of the task, so an unapplied result survives a lost disk. A code-change result with a GitHub remote needs no copy: its branch and pull request are on GitHub, which is canonical for code ([integrations.md](integrations.md)). For a repository without a remote, LINA Core keeps a Git bundle of the branch, made by LINA Core itself as a backup copy; it is never pushed or merged from. The result artifact of a background run is kept as LINA materials of the run when it lands, so a lost work area loses only a run's transcript and scratch files.
- A worker writes only inside its own task directory and the git directories it needs to commit. It never writes a protected ref in a parent clone.
- Task and run directories are removed only under the retention rules of this contract.

## Background runs

A background run is the execution of one of LINA's own sub-agents. It keeps the conversation free: research, analysis, multi-file reading, drafting and read-only checks that are too long or too heavy for a turn run in the background, inside LINA Core, on the person's own model accounts, and the result returns to the conversation as a short notice. A sub-agent is a bounded model loop on the loop core of the LINA kit with a different tool set. It is not a second conversation engine and not a coding harness: it writes no code into a repository, opens no pull request and judges no delivery. Coding work goes to a worker (see Workers). Moving work to the background changes where it runs and nothing else: a run holds the same done condition and the same checks that the work would have had in the turn ([runtime.md](runtime.md#turn-budget)).

### Starting a run

- The conversation starts a run with one tool. The tool returns the run id and the state `working`, or `waiting` when a limit holds the run back, within milliseconds, with an optional line for the agent to say at once. The conversation has no tool that waits for a run to end, and a run's work never runs inside a turn.
- A run is started by a conversation, by LINA Core itself when a turn reaches its hard budget ([runtime.md](runtime.md#turn-budget)), or by the work engine for a request of a job for the whole main, such as Klotho's research ([cognition-and-life.md](cognition-and-life.md)). A run started for a job has no requesting agent, and its landing goes to the person's home cards. A run starts no run and starts no work (depth 1).
- The start carries a brief that stands alone: the goal, the scope, what is not to be touched, the done condition and the checks that establish it, the result type that the card will use, a tool allowlist, and optional references, by id and revision, to the materials and memory projections the run may read. The brief sets no time cap and no call cap. The run starts from the brief with no conversation history. A form that copies the conversation into the run is allowed only when the brief cannot be written in a few lines.
- The agent chooses by a scale rule that LINA Core's instructions give it: a small job is done in the turn, a medium job goes to one run, and a large job goes to a few runs in parallel with disjoint scopes. LINA Core's limits enforce what the rule only steers.
- Before a run starts, LINA Core checks the requesting agent and person, the run scope ([main-authority.md](main-authority.md#sub-agent-runs)), the person's remaining budget and the limits below. A start that would take the total past the person's budget returns `refused: budget` and runs nothing, and the agent tells the person that the work has not been done. A power-user setting can require the person's approval before every start; it is off by default.
- A run uses the model the person chose for background runs. Until the person chooses one, it uses the conversation model.
- A run's request carries LINA Core's instructions for background runs, its brief and its tool declarations. It carries no persona, emotional state, skill catalog or conversation history. Memory and materials reach it only as the projections its brief names, drawn from the requesting agent's personal knowledge and common knowledge.

The start tool's implementation has a receipt barrier: Core commits the id and initial state, the conversation records/returns that receipt, and only then releases the queued execution. The start tool never waits for a model or a result. Admission checks and brief data are copied into the run; stopping the originating turn cannot cancel it.

### The run record

The work ledger holds one record per run: the run id, the native start-call identity and brief digests with the original receipt, the requesting agent, the conversation and turn that asked (or the promotion at the hard budget, or the job), the person the run is done for, the brief and its references, the allowlist and run scope, the usage so far, the state, a checkpoint after each completed tool round trip, the result artifact and the times. Usage records name the run and the requesting agent. The transcript of a run is kept in its run directory in the work area, and the developer view shows it. Secret-shaped text and sensitive JSON fields are masked before the record, transcript or result material is persisted and before model input is sent. JSON structure and exact numeric evidence are preserved.

### Limits

These limits decide how many runs start. They never decide how much one run does or how well.

| Limit | Default |
| --- | --- |
| Depth | 1 |
| Concurrency | 4 running runs per agent and 8 per person; further starts wait in order |
| Time and tool calls | None. A run has no fixed time cap and no call cap |
| Tokens | Drawn from the person's one budget |

- A run ends in one of three ways only: its done condition is met; it fails for real (it is stuck, its process is gone, or the same error repeats), in which case LINA Core retries once from the checkpoint and then reports honestly what was done and what was not; or the person stops it. Nothing else cuts a run, neither its age nor its number of tool calls nor the size of its output.
- The model account and the person's budget are the other limits. When the account is at its limit ([runtime.md](runtime.md)) or the budget is spent, the run stays waiting with its checkpoint and says why, and LINA Core does not retry in a loop. The run is not wrapped up, and it does not hand back a shorter result.
- A run whose request would reach the compaction threshold has its older tool outputs condensed as in step 2 of [Compaction and resume](conversation-and-memory.md#compaction-and-resume). If it still does not fit, the run goes on in a continuation request that carries a summary of its work so far, as a conversation does. It is not ended.

### States

- A run is `waiting`, `working` or `needs_input`, and it ends as `landed` with one outcome: complete, partial, failed or cancelled. `needs_input` is a question the person owns; the run stops and holds only itself.
- Complete means the done condition is met and the checks that the brief names have run. Partial means the run ended with its done condition not fully met, because it failed for real, because it needed something outside its scope or because a check that the brief names did not pass, and it holds something usable: its result says what is done, what is missing, what was verified and what was not, and that the run can be resumed. Failed means nothing usable came out, and the result says what was tried. Cancelled means the person stopped it, and it keeps what it had gathered. A partial result is never presented as the answer.
- The person can stop one run or all runs. A stop is idempotent. A cancelled run stays cancelled even if its execution later completes, and it keeps the artifact of what it gathered. The person's immediate stop ([main-authority.md](main-authority.md)) includes background runs.
- A run outlives the turn that started it. Interrupting or ending that turn does not stop the run, and a run depends on no client and no open conversation.
- A follow-up ("also check X") is a message to a running run. The conversation sends it with a tool, LINA Core delivers it as steering input at the run's next round trip, and it reaches only that run. A waiting run takes it into its brief. A follow-up never cancels the run.

### Landing

- When a run lands, LINA Core stores its result as LINA materials of the run, with the result type from the brief. It resolves each cited passage anchor with the kit's citation check; a result with an unresolved citation lands as partial with that citation marked. It records the landing in the work ledger and hands the notice to the conversation ([conversation-and-memory.md](conversation-and-memory.md#background-results)).
- A result is a claim, labelled as an agent report. It has no `verified` verdict, and it enters memory only as [conversation-and-memory.md](conversation-and-memory.md) allows.
- The notice states what was verified and what was not. It says "done" only when the done condition is met; otherwise it says partial, failed or stopped, and what is missing.
- When a background run produces a typed result, LINA Core validates the body as it does for `present_result` ([host-protocol.md](host-protocol.md#typed-results)), writes the result to the conversation ledger, and the card of that result is the landing notice. A run that was asked for a document writes it from the latest revision of the confirmed outline ([conversation-and-memory.md](conversation-and-memory.md#results)).
- A result is derived data under the revocation epoch rules: it records the epoch it was built at, and evidence retracted before the notice is delivered is marked in the notice instead of being delivered silently.

The implementation retains checks as structured evidence: a successful named tool call and resolved citations are mechanically verified; `claimed` remains explicitly unverified even when the model reports completion. Result material publication is idempotent by run id and keeps earlier revisions. A failed publication retains a pending landing for retry, including automatic resumption after Core restarts, without restarting the model loop. Call intents and results survive interruption: a completed call is reused, an uncertain scratch write is not repeated, and its uncertainty prevents a complete outcome. Credential-shaped text and sensitive JSON fields are masked before retained run data is written and before tool-output context reaches a configured preparation callback. Pending notices expose both person-owned `needs_input` questions and terminal landings; acknowledging the question cannot suppress the later landing. They contain metadata and material references, never the run's body or transcript; evidence is checked again when the notice is read for delivery. A cancelled outcome cannot be overwritten by a late result or recovery.

A landed material has an explicit open action in both its running row and its
conversation result card. Opening reads the pinned material body in bounded
pages; the notice itself carries only metadata. Opening or dismissing the row
removes it from the live running list and future snapshots while the retained
conversation card remains readable. Pending effect questions use the same lane
outbox at a turn boundary, independently of the final landing, and their delivery
grants no authority for further actions. Its existing question card accepts
authenticated direction, decline or cancel. Accepted direction returns to the
originating conversation through idempotent input admission; the answer and its
pending delivery remain in the run record across restart. An answer never clears
an unknown effect or expands the run scope.

### Durability

- Before recovering an interrupted turn, Core confirms a committed explicit start admission as its successful receipt. Calls without a committed admission remain aborted and are never executed by recovery. An admitted start awaiting receipt publication consumes no execution retry.
- A repeated start tool call is scoped by its trusted origin and native call id. It returns the same committed receipt even after restart or a later budget/account hold, refuses a different brief under that identity, and does not allocate a second run or capacity slot. Only new admissions need the current new-work authorization.
- Recovery restarts already-released queued work without requiring unrelated activity. Unreleased receipts and explicit budget/account holds remain parked; recovered work directories are recreated from durable state before execution.
- A run is a durable record. It survives a restart of LINA Core: a run that was working is resumed once from its last checkpoint under the new epoch, and a second interruption lands it as failed, or as partial when it holds something usable, with an honest account. A run makes no effect outside its scratch directory, so no effect is left unknown.
- At its expected time without having landed (15 minutes by default), a run gets one check-in from LINA Core: a read of its progress, with no model call. The expected time is a point to look, not a limit, and a run that is making progress goes on. A run that records no model or tool event for the stale interval (see Liveness) is stalled; LINA Core resumes it once from its checkpoint, and if it stalls again it lands as failed, or as partial when it holds something usable, with an honest account of what was done and what was not. No wall time ends a run.
- A run is resumable from its checkpoints. A partial result keeps its artifact, so a follow-up run can continue from it.

Implementation boundary: the run package supplies admission, release, steering, stop, progress checks, restart recovery and durable pending notices. Core's composition root supplies the current epoch, shared-budget/authority checks, provider, read/scratch adapters and projection/condensation preparation. The conversation's turn-boundary notice dispatcher is the separate G1-35 scope; no background-run TUI or client-protocol capability is advertised by this package increment.

## Contract input from CRW

CRW ([codex-relay-workflow](https://github.com/thisisjun786/codex-relay-workflow)) is the operator tool that coordinates Codex work until the operational switch. The work engine is LINA Core's management session, built anew in Go for LINA's system: it keeps how CRW works (the work ledger and its states, relay delivery and acknowledgement, and the supervisor, parent and child roles), with CRW's contract schemas (`contract/schema/`) and fixtures (`contract/fixtures/`) as fixed input. CRW code is never copied, vendored, translated or linked. The schemas and fixtures state the behavior that matters; CRW's own code and host paths stay with CRW.

### Start gate

- The start gate for the work engine is a pinned CRW schema and fixture version.
- The pin record names the CRW commit, the content hash of every adopted schema document and fixture file, and the adopted fixture domains. It lives in the LINA repository next to the work engine and is listed in the release manifest.
- Work that needs the pinned schemas starts only after the pin: the work ledger and delivery records, delegation, orchestration, worker dispatch from the product, and the takeover of CRW records.
- The pinned schemas fix only the records of the work ledger. The worker adapter round trip does not depend on the pin. It is an acceptance item of the first implementation issue and runs first.
- Conversation, memory, persona, TUI, LINA APP screens, materials, cognition outside the work engine, LIFE and the OS base never wait for the pin.

### Use

- The completion receipt that a worker submits is defined by LINA's own completion receipt schema, at the schema version of the worker adapter (see Completion receipt). The worker adapter round trip uses it before the pin.
- The pinned schema documents for relationships, completion receipts, delivery attempts, acknowledgements and verification verdicts fix the shape and allowed values of the matching ledger records. LINA adds fields only for LINA ids and evidence and never redefines a pinned field.
- A ledger record of a receipt maps the completion receipt onto the pinned shape by adding fields only, so a receipt is never defined twice.
- Single delegation uses these records from its first implementation, so delegation records are never built twice.
- LINA runs every adopted fixture against the work engine with its own runner. Expected values come from the fixture, never from LINA's observed output. A failing fixture is fixed in LINA, never in the fixture.
- A fixture that exercises a CRW-only surface (its CLI, sockets, MCP bridge or plugin hooks) is not adopted.
- Moving the pin is one change that updates the pin record, the release manifest and every affected record shape together.

## Plans

Planning is built into LINA Core. The records of work and knowledge, their plans, bindings and summaries are kept by LINA Core (see Storage of records), and clients read and change them only through LINA Core ([host-protocol.md](host-protocol.md)). No external planning service is ever a dependency ([product-families.md](product-families.md)). Every user gets this same work and knowledge management. A developer user differs only in being able to link code work to GitHub (see Development mode).

The person meets work and knowledge in a frame taken from PARA (Projects, Areas, Resources, Archive), in words that need no project-management background. The Korean words and the structure are the default; a card sort with non-developers confirms the words.

| Object | Meaning | What LINA tracks |
| --- | --- | --- |
| 메모 | A captured thought with no place and no category. The inbox. | Nothing until it is placed. A memo surfaces when it is searched, referenced or triaged. |
| 할 일 | Work for one sitting. It belongs to an 일, to a 계속 챙기는 것, or stands alone. Checklist lines live inside it; there are no sub-to-dos. | Its fields (see The to-do record). A small 할 일 is never forced into a hierarchy or an execution. |
| 일 | Work that ends. It has a done condition, and may have a target date and ordered stages. It belongs to a 목표 or stands alone. | Its done condition, its 할 일, its dates and its progress. |
| 목표 | An outcome the person wants, in one sentence in the person's own words. It has a done condition; its end date is optional and may be coarse. It exists only when the person wants one. | Progress, blockage and completion. |
| 계속 챙기는 것 | A standard to keep up, with no finish. It holds 할 일 and 일. | Its 할 일 and 일, and when it was last reviewed. |
| 자료 | Reference material the person keeps. It is what the library holds ([materials-and-knowledge.md](materials-and-knowledge.md)). | As the library defines. |
| 보관 | A state any object can enter when it is no longer active. | The object, its history and the date it was archived. |

Rules:

- What separates these is whether LINA is responsible for tracking the thing and whether it can be finished, not its name. The person is never asked to classify. LINA asks only when it must, and then one question: can it be done in one sitting (할 일); does it end, and how will I know (일); is there a standard to keep up with no finish (계속 챙기는 것); what outcome do I want (목표). The one placement LINA asks about is the line between something that ends (an 일 or a 목표) and something that continues (a 계속 챙기는 것), and only when it cannot tell.
- The depth is 목표 (optional), 일, 할 일, checklist line. A checklist line has no date, owner or state of its own; a step that needs one is promoted to a 할 일. A 목표 is never required to create an 일 or a 할 일.
- A 목표 is one sentence: the outcome it reaches, in the person's own words. That sentence is the 목표's name. Its first page, its 일 and their stages explain and deliver it; they never replace it with a list of topics.
- An 일 has a mandatory done condition. Its target date is optional and states its precision (day, month or quarter). Its stages are optional, ordered groups of its 할 일. There is no milestone object: a checkpoint that needs its own owner or blockers is a 할 일 with a deadline that other 할 일 depend on.
- LINA promotes a 메모 to a 할 일 or an 일 when its kind is clear, says so in one line and offers one action to undo it. A 목표 exists only when the person accepts its draft. The 메모 inbox offers four actions: make it a 할 일, 보관, 나중에 (later) and merge into an existing item.
- Finishing an 일 archives it with its 할 일 and materials, and LINA says so in one line with one action to restore it. 보관 is a state, not a deletion.
- A plan is the confirmed structure of an 일: its stages and 할 일, with their order and dependencies. A plan drafted by LINA becomes a plan at exactly one point: the draft is saved and the user confirms it. A plan the user writes directly on the plan screen is a plan without further confirmation.
- A plan that LINA drafts for the person to confirm is presented as a `plan_view` result ([host-protocol.md](host-protocol.md#typed-results)): the ordered entries with their kinds, titles and states, replaced completely on each update. Its confirm action is the plan confirmation point above, and no other path turns a `plan_view` into plan objects. A plan that already exists is shown in the same form when the person asks.
- Decomposing a 목표 into 일, and an 일 into stages and 할 일, and where it starts, is Klotho's planning function ([cognition-and-life.md](cognition-and-life.md)).
- Free text attached to a record is versioned text: every edit creates a saved version that can be compared and reverted.
- The latest dates are owned by the fields of the 일 and the 할 일. Real precedence is owned by blocked-by relations. A task never copies dates or precedence into its own text.
- Product acceptance criteria are owned by card criterion items. A 할 일 references criterion ids with their revision.
- Where a 목표 or an 일 stands is computed from the actual results of its 할 일. When results are missing or stale, it is unknown. "On track" and "at risk" are judgments that LINA proposes and the person can override.

How the work engine's terms map to the frame:

| Work engine term | What the person sees |
| --- | --- |
| Task | The 할 일 it executes, with that 할 일's state. For a developer user a code 할 일 maps to one pull request and optionally a GitHub issue. |
| Work thread, attempt, execution generation | The 할 일 shown as 하는 중 (started) and, on request, its run history. Never named. |
| Parent role (one per 일) and supervisor | LINA coordinating the 일. Never named. |
| Plan | The confirmed stages and 할 일 of an 일. |
| Milestone | A target date of an 일, or a 할 일 with a deadline. |
| Initiative | A 목표 draft that Klotho proposes. |
| Issue | A 할 일 in LINA's own records. A GitHub issue is GitHub's own object and is only linked. |

### Storage of records

Goals, 일, 할 일, 계속 챙기는 것, memos and knowledge documents are human-readable records. They are files in a Git repository under a PARA folder layout, and LINA Core is their only writer. The database indexes the files and can be rebuilt from them at any time. Machine state stays in the database: the conversation ledger, grants, receipts, the work queue and work ledger, cursors, leases, claims and idempotency keys. A cursor moves only after the write it covers has succeeded.

- A record is one Markdown file with small typed front matter: id, kind, state category, dates, recurrence, blocked-by ids, tags and the ids it belongs to. Dates are ISO 8601, with a zone for timed values and plain dates otherwise. LINA Core allocates the id. It lives in the front matter and never in the path. Promotion and archiving change fields of the record (its kind or state), never its id or its file. The file name carries a slug for people to read.
- The folder layout and the place of the repository in the state root are defined in [filesystem.md](filesystem.md#records-repository).
- Git words stay hidden from people who do not use Git: every change is a saved version. The revision of a record is the commit that holds it, and a write against a stale revision is refused with `stale_revision`.
- Recurring completions and other per-day churn are recorded in the work ledger and in the repository's history, not as new files.
- A fact that carries a status is a field of one record. Documents, cards, summaries, memory items and persona text cite that record or render from it. When a status changes, LINA Core searches the records and knowledge documents for every mention of the item and fixes or flags each one in the same pass.
- A change made to the repository by another tool is detected by LINA Core, validated, and shown to the person as an outside change. LINA Core never merges it silently.
- For a developer user, a 할 일 can link to a GitHub issue and a pull request. The link is a field of the 할 일; the issue and the pull request stay GitHub's ([integrations.md](integrations.md)).

### The to-do record

A 할 일 is one record with two views. The personal view follows fast-capture list tools and reads groups 1 and 2. The flow view follows issue-tracker tools and adds group 3. A person who never opens the flow view never needs group 3, and a 할 일 created from one sentence has none of it set.

| Group | Fields | Shown to |
| --- | --- | --- |
| 1 Capture and plan | title, note, checklist, `when`, `deadline`, `repeat`, `important`, tags | Everyone |
| 2 Belonging | the 일, the 계속 챙기는 것, the 목표 (derived through the 일), and where it came from (a memo, mail, a meeting) | Everyone, as breadcrumbs |
| 3 Flow | `state`, `blocked_by`, `waiting_on`, `assignee` (a person, an agent or a worker), the pull request and issue link, the execution state | The flow view and the detail view; developer and power users |

- **Capture.** Capture succeeds with a sentence alone. Nothing requires clarification, and no count of unclarified items is kept. A burst of short messages is one utterance. A one-line add shows a row of chips (date, 일, tags, repeat) before saving. Date parsing, Korean included, is LINA's own: the model parses and the chips confirm the interpreted date. An ambiguous date is never saved silently. The TUI may also take deterministic tokens: `#` for an 일 or a 계속 챙기는 것, `%` for a tag, `!` for priority, `{}` for a deadline, and plain words for dates.
- **Dates.** Two fields with different jobs. `when` is the day or time the person plans to start. `deadline` is the hard cutoff with an external consequence. It stays empty unless a source shows it, and then it carries a pointer to that source. It is one-time, so a recurring 할 일 carries none. One date control sets `when`, with "add deadline" as the secondary action. Reminders attach to `when` only; the deadline gets a pre-warning in the views. Rescheduling moves only `when`, never a deadline and never an external calendar. LINA rewrites `when` only to reschedule an overdue 할 일, says so in one line and offers one action to undo it. A duration is optional and never required.
- **Today rule.** A 할 일 appears in 오늘 (today) when `when` is today or earlier, when `deadline` is today, or when it repeats today. Overdue `when` items appear in a separate block, 밀린 것 (overdue), with one-tap rescheduling. A 할 일 with no date stays reachable in 언제든 (anytime) and is never hidden.
- **Recurrence.** A recurrence is a subset of the RFC 5545 recurrence rule plus a from-completion flag, because "every 3 days after I did it" and "on the 1st" differ. A recurring 할 일 is one live record that advances on completion, with no hidden template and no generated instances. It advances only to a future occurrence, so an absence does not create a backlog; an overdue recurring 할 일 postpones to its next occurrence. It may have an end date. An edit applies to this occurrence only or to all future ones. Dependencies do not carry to the next occurrence. The person's own wording of the rule is kept. LINA Core advances recurrences and fires reminders itself, without a model call and without `moirai-worker`.
- **Views.** Views are lenses over the same records, computed by LINA Core. Personal views: 오늘, 예정 (upcoming), 언제든, 나중에 (later) and the 메모함 (memo inbox). Flow views: a board whose columns are the state categories, with a group-by control (일, assignee, priority, tag), and a list. There are no cycles; a "this week" lens derived from `when` and `deadline` covers the need. 오늘 is partly curated: LINA adds candidates from mail and meetings, the person can reorder it, and LINA may propose a day plan from free calendar time. LINA never commits a day plan on its own.
- **State.** The state categories are fixed: `backlog` (default label 나중에), `open` (할 일), `started` (하는 중), `completed` (끝) and `canceled` (안 하기로 함). The labels are translatable; the categories are the contract. Unchecked means any category before `completed`; checked means `completed`. A person who never opens the flow view never sees `state`. Overlays are derived and never stored: 막힘 (blocked: not done, with a direct blocker not done), 확인 필요 (the person owns an open question) and 검토 중 (a pull request ready for review). The work card states under Progress display stay separate from `state`. A model's sentence never sets `completed` for delegated work.
- **Waiting.** `waiting_on` records who the 할 일 waits for, since when, and the next-check date. `blocked_by` derives the blocked overlay; `waiting_on` is stored data. If the other side is waiting on the person, that is the person's own 할 일.
- **Dependencies.** `blocked_by` lists the 할 일 that must finish first; the inverse is computed. Dependencies are finish-to-start only, with no lag. One field has two enforcement modes by assignee. For a person it is a soft signal: the dependent shows 기다리는 중 (waiting) and can still be checked, and a non-developer meets it only as a hint ("do this first, it unlocks that"). For an agent or a worker it is a hard gate: the dependent is released only after its predecessor has landed (see Parallel hand-off). When a blocker resolves, the relation becomes a plain related link. In the default UI dependencies exist inside one 일 or between the 일 of one 목표; across goals they are set in the detail view. LINA Core allocates ids and rejects cycles.
- **Completion.** Completion is inferred from evidence, not from the person ticking boxes: the person's own sent message, an approval mail, a later statement. A closing proposal carries the evidence and closes with one word. A past deadline never means done. Before LINA calls a 할 일 open or due, or moves it, it reads its originating source again. Payment status always needs evidence; conversation alone cannot show payment.
- **Candidates.** A candidate is an object (source link, confidence, numbered handle) and a state before 할 일. LINA adds a 할 일 on its own only when a deterministic test passes: the item is addressed to the person, comes from a person, asks or promises something, and has no open equivalent. Every automatic addition prints an undo handle. A class whose automatic additions the person rejects too often falls back to candidates. Unanswered candidates are bundled and shown again with an explicit scope for "add all"; they are not dropped. A 할 일 that survives several briefings without action gets one question (done, move, drop); after as many more it is parked and shown only in the weekly review. The thresholds are starting values set by measurement (see Deferred).
- **Priority and tags.** Everyone has an `important` flag. An optional four-level priority (none, normal, high, urgent) is a power setting. Tags are free-form with no hierarchy, and the agent suggests an existing tag before it creates a new one. Estimates are opt-in.
- **Code work.** A 할 일 in a connected repository maps to one pull request: opening the pull request moves it to `started`, and it completes under Judgment and delivery, never by a merge or a model's sentence alone.

How an agent manages 할 일 (capture, briefing, review) is the methodology of the LINA preset ([conversation-and-memory.md](conversation-and-memory.md#preset-methodologies)).

### Development mode

- Connecting a GitHub repository to a 목표, an 일 or part of one puts that part in development mode. Only a developer user connects GitHub; everything else about work and knowledge management is the same for every user.
- In that part a code 할 일 has one branch and one pull request, both written by the worker, and it may link to a GitHub issue. LINA Core applies the draft by merging the pull request after its review (see Judgment and delivery). The GitHub plugin and field ownership in a connected part are defined in [integrations.md](integrations.md).
- Development mode changes how work is done inside. The person is not shown issues, pull requests, diffs or checks by default; they are in the Details of the task or draft ([surfaces.md](surfaces.md#default-view)).
- Parts without a repository stay document-centered, and both can live in one 목표.
- Document 일 are local by default. Connecting a remote repository is optional.

## Intent cards

Intent cards are a common feature of LINA APP. Their data is owned by LINA Core; LINA APP shows the cards and takes input. The card tree states what must be true. 목표, 일 and 할 일 state when and how it gets delivered. The two are axes of one graph:

- A 목표 or an 일 cuts across cards. It names the cards it covers and never includes their descendants automatically.
- A 할 일 that is product work hangs on one or more cards.

The plan has four layers. How they are shown is defined in [surfaces.md](surfaces.md).

| Layer | Object | Writers | Rule |
| --- | --- | --- | --- |
| 0 First page | One page per 목표, or per 일 that stands alone: why, what it becomes when done, where it stands, what waits for the user, recent changes (what, why, who) | LINA and the user | Co-edited. LINA rewrites it when the work layer has new results. Every edit is a saved version, appears in recent changes and can be reverted. "What it becomes" is drafted by LINA and confirmed by the user. |
| 1 Map | The card tree | LINA organizes | "Take a look" requests and Klotho's periodic tidy write the organization directly. |
| 2 Page | One card: a summary at the top and the user's intent field | LINA writes the summary; the user writes the intent | LINA and automatic tidying never overwrite the user's intent text. |
| 3 Work | The 할 일 handed to agents, with criterion references, execution record and evidence | LINA | Derived from the work engine. |

Card rules:

- The intent field saves automatically as the user types. Each save is a saved version in the card's history.
- A "take a look" request asks LINA to read the saved intent and carry it into the card's summary, the map, criteria drafts and task drafts. Drafts enter the plan only through the plan confirmation point.
- Each criterion item has a fixed id that survives moves and rewording, and a revision that advances on every edit.
- Every change that LINA or tidying makes to a card appears in the card's history.
- A card's execution state is one of none, planned, in progress, delivered or verified, derived from observation of its 할 일. When observation is missing or stale the state is unknown, never none or verified.
- From gaps between cards and work, Klotho drafts 할 일 only. Plan confirmation stays at the one confirmation point.

## Supervisor, parent and child

| Role | Held by | Owns | Never |
| --- | --- | --- | --- |
| Supervisor (optional) | LINA Core | Oversight across several 일 | Changes permissions, scope, branches, worktrees or report recipients by being attached or detached |
| Parent | Manager session of the owning persona, one per managed 목표 or 일 | Project judgment: next tasks, work definitions, dependencies, stacked work, delivery checks, verdicts, merge order and follow-ups | Bypasses Core's grants, leases, receipts or merge preconditions |
| Child | The work thread that does the task, one per task | Its own task packet | Merges, judges its own delivery, or inherits the parent's full permissions |

Rules:

- A parent works without a supervisor. Its manager session is the owning persona at work, resumed across events of its project. It is not a new persona for each event.
- The manager reads current project state, selects the next issues by dependencies and milestone order, writes each brief as a work definition, fills available slots including stacked work on open predecessors, checks every finished result against its issue, decides merge or close, and files follow-ups. In a project without GitHub, the same judgment uses its 할 일 and done conditions.
- LINA Core owns the mechanics beneath both parent and child: ledger, receipts, area and worker leases, bidding across Nodes, delivery with receipts, protocol-based stall and death detection, once-only recovery in the same work area, merge execution and a QA run after each merge. The manager interprets results and chooses follow-up work; Core enforces the recorded choices and permissions.
- A child has an explicit role and scope. A request outside its scope is blocked.
- Roles come only from LINA Core's link records. Titles, folders and conversation links never create a role.
- A dependent task is released as soon as its predecessor has landed, or earlier when the manager records a stacked start on that predecessor's open branch under Parallel hand-off. Its base contains the predecessor's work in either case. A task that no other task depends on never waits for another task.
- A task waiting for a decision holds only itself. Independent tasks continue and are never made to wait for it.
- The design-conformance review is not a child and not a separate work thread. It is the manager session's check against the work definition at merge, and supplies evidence for the verdict that Core records.

### Manager session lifecycle

- **Start.** When management of a 목표 or 일 starts, LINA Core binds one manager session to that unit and its owning persona. The session reads current plans, dependencies, milestones, open tasks and deliveries before choosing work. The binding survives idle periods and events; capacity and dispatch stay with Core.
- **Owner deletion and handoff.** Deleting the owning persona ends its manager binding. Tasks keep running under the existing agent-deletion rules, and Core preserves their work definitions, artifacts, receipts and pending events. Deliveries awaiting judgment become a named wait for manager reassignment, with a next-check deadline, on the person's home cards. When the person hands the project to a live agent, Core records the new manager binding and resumes delivery handling from the work records; the new manager checks pending results before Core can accept or merge them. The deleted persona's private conversation and memory follow their deletion rules and are not transferred, and grants naming only that persona confer no authority on the replacement ([product-families.md](product-families.md#agents)).
- **Resume and batch.** Each project event resumes that session. Core claims the project's pending events as one batch for one turn, preserving every event and receipt; arrivals during the turn form the next batch. The manager acts from current ledger and external state, not remembered status. Its context accumulates per project in the lossless conversation ledger and summary tree ([conversation-and-memory.md](conversation-and-memory.md#manager-session-context)).
- **Busy fallback.** When the session is busy longer than a configured wait limit, Core dispatches pending events for tasks outside the active batch to parallel turns of the same owning persona. These are bounded event handlers, not additional project managers. Claims exclude tasks already held by the manager or another handler, and area leases and merge slots still apply. Receipted results return to the manager's next turn so it reads what happened without doing it again.
- **Visibility.** The person may read the manager session and its state in the project's Details. This view is read-only: it supplies no input, approval or control handover. Instructions and decisions still enter through the person's conversation and the bound control paths.
- **Code update.** A code update restarts manager execution under the new code and epoch, retaining its project binding, lossless context, pending events and receipts. Core reconciles in-flight effects before resuming the session; restarting never replays an effect whose outcome is unknown (see Effects and unknown outcomes).

## Development method

LINA ships no coding harness. It runs no review loop of its own around a worker, installs no hook, skill or plugin into one, and keeps no stage workflow. Codex, Claude Code, Antigravity and Devin are used as the user installed them. LINA's development role is a method, which the development preset carries ([conversation-and-memory.md](conversation-and-memory.md#preset-methodologies)). How to implement, split, join or interpret work is the judgment of the worker and the owning persona in its manager session. The worker writes the code, the branch and the pull request body, opens the pull request and owns it to green. The manager checks the work definition and decides merge or close. LINA Core keeps the flow moving: start, reliable delivery with receipts, true state, recovery, merge on green, QA after each merge and report.

### Work definition

Before it hands a task off, the manager session writes the work definition. It states:

- what: the change wanted
- why: the goal it serves, in a sentence
- scope: the paths and behavior the task may change
- what not to touch: the paths, behavior and decisions that are out of scope, and who owns them
- the done condition: one condition that CI or a reviewer can establish, such as named checks passing and the behavior the design describes
- the design reference: the contract and section the work must conform to
- the write area: the path globs the task may change (see Splitting and parallel hand-off)

Two things are part of every done condition for code-change work. First, a change that alters behavior updates the sections of its design reference in the same pull request, so the design and the code stay in step. That update is part of done, not a later task. Second, automated checks prove the behavior. These are unit tests, plus headless end-to-end tests where the change can be seen from outside the program. Such a test drives the project's interfaces by script and needs no window and no person. The checks run in CI. The person checks only the user interface and the experience. The design reference is written before the code, and the person's language applies to the summaries they see ([conversation-and-memory.md](conversation-and-memory.md)). LINA's own development applies this rule in the [roadmap](go-roadmap.md#phase-rules).

The work definition contains no review procedure and no request for a verification report. The task packet is the work definition with the assignment id, the way to return the completion receipt (the submit tool of LINA's work tools), the branch name to use, and the references to the projections the task may read. The worker writes the commits and the body of the pull request, and opens it. LINA writes none of them.

### Choosing the worker

LINA chooses the worker for a task from the situation, the person's subscription quota and the host's capacity, and hands the task off. The situation is the kind of work and the repository. Quota is what each worker reports about its remaining quota; an unknown quota is a value, and for a worker that reports none LINA Core uses its own usage accounting ([runtime.md](runtime.md)). The choice follows an editable table of rules, marked provisional, which the power settings show ([surfaces.md](surfaces.md)). A worker whose quota row is ok is preferred, an unknown row is usable but unmeasured, a blocked row is unavailable until the stated reset (or until the next successful call when no reset is given), and a reset time is never inferred ([runtime.md](runtime.md#usage-and-cost)). If a worker stops on quota, rate limit, login expiry or repeated connection failure, LINA hands the same task packet to the next suitable worker without asking the person. Two workers never share a worktree.

### Operating the pull request

The worker owns its pull request to green. Inside the work thread it waits for CI and the review bots, fixes every CI failure and the P0 and P1 review findings once, and waits for the checks at the resulting head before reporting done. P2 and P3 findings are reported with the result and do not block the merge. The review and CI fix loop belongs to the worker's turn; it never goes through LINA Core as a round trip.

At merge the manager session checks the result against the work definition's done condition, scope and design reference, then asks Core to merge or close. It sends work back only when the result misses that definition, never to triage review findings or CI failures. The steps are in Judgment and delivery, and the only GitHub writes LINA Core makes on delegated work are merging and closing (see GitHub writes and merges).

**Prose polish.** Once a worker has opened a pull request, a polish step runs beside the review and CI and never gates them. It rewrites only person-facing prose: the pull request body here, and likewise the issue bodies LINA drafts and release notes. Facts, code, paths and links stay unchanged, which LINA checks mechanically by comparing them before and after; when the check fails, or the polish fails, the original stays. Changes to UI copy get a polish suggestion attached to the pull request and are never committed automatically. The polish model is a provider setting: by default Argon through the person's Antigravity when they have it, and the step is skipped otherwise. It never blocks a merge.

## Splitting and parallel hand-off

LINA splits work into tasks, orders them, and hands every task that can start to a worker at once. The work definition of each task, the choice of worker and the operation of the pull request are LINA's development role. This section fixes what concerns many tasks at a time. Splitting is internal to LINA: the person confirms the 일 and its done condition and does not review the split. Its tasks appear as the 할 일 of the 일 and, in the running list, as outcomes (see Progress display).

### Splitting into tasks

The manager session splits work into tasks that one worker can finish as one pull request. The criteria below guide the owning persona's judgment, not Core code. They are recommended defaults and are tuned from LINA's own merged pull requests.

1. One done condition and one concern per task. The done condition is checkable by a command or a CI job, and the product still works after the task merges.
2. Scope decides first and size second. A task is sized for one worker in about 15 to 30 minutes of worker time. About 400 changed hand-written lines is a soft budget, with tests counted and generated files, lockfiles and pure deletions not counted. Line count alone never splits a task: two to six small related fixes in one package may be one task.
3. A task whose work cannot be written as one work definition with one done condition is too big and is split again. So is a task that touches more than one layer, or that a person would need more than about three hours for.
4. Tasks that touch disjoint write areas can run in parallel. Tasks that share files, or where one needs the other's result, are ordered by a dependency. A dependency between tasks is a blocked-by relation of the plan (see Plans); for a task handed to a worker it gates dispatch unless the manager records a stacked start under Parallel hand-off. A stacked start never bypasses an overlapping write-area lease.
5. A refactor is its own task and lands before the feature that uses it. Tests ship in the task that changes the logic; a test-only task for existing code may run first.
6. Storage-format, security and concurrency changes stay small and alone: one task, with no other concern in it.
7. A decision that two or more tasks must agree on, such as an interface, a schema or a naming scheme, is made once and written into the design reference of the work definition. Tasks that depend on it wait for it or carry the same reference. A task that has to change the design reference is its own task, and the others depend on it.
8. Wide, shallow slices are preferred because they stay parallel. Layers, foundation first, are used only when a lower layer defines what the upper ones call.
9. Given a pile of old defects, a worker first marks each one fixed, still present or not reproducible, with evidence. Tasks are made only from those still present.

### Write areas

- Every task declares a write area in its work definition. Two tasks whose write areas overlap are dependent: LINA Core orders them and never runs them together.
- LINA Core holds an area lease for each task from hand-off until the task has landed, is closed or is abandoned. The lease follows the liveness of the task's worker (see Liveness): a task judged abandoned frees its lease, and nothing else frees it earlier.
- Hotspot files are shared resources: dependency manifests and lockfiles, generated code, registries and index files, changelogs, and any file that recent history shows many tasks changing. A task that needs a hotspot file takes the whole hotspot area, or the change moves into one dedicated task that the others depend on.
- A worker cannot be trusted to stay inside its area. The design-conformance review reads the pull request's diff against the declared write area. A change outside it misses the work definition's scope and goes back to the worker as `needs_changes`. This is part of the check at merge, not a separate review.

### Parallel hand-off

- Every task whose dependencies are satisfied and whose area lease is free is handed off at once. It is limited only by the person's subscription quota for each worker and by the host's capacity. LINA Core never holds back a task to wait for another task that it does not depend on.
- Quota is read per worker from the worker's adapter as quota rows in which unknown is a value ([runtime.md](runtime.md#usage-and-cost)). Where a worker reports none, LINA Core infers it from the worker's quota errors and its own usage records. A worker that stops on a quota or rate-limit error is parked until its window resets, and the task is not retried on it; the task goes to the next suitable worker (see Choosing the worker).
- Host capacity is what the host can run beside the person's own work, within the install's declared resource caps ([non-competition.md](non-competition.md)). A device whose Node is disconnected or yielding receives no new hand-off.
- Concurrency follows the load-sized Node capacity and transactional reservations of Run to done, further limited by quota, declared resource caps and write-area leases. There is no fixed fleet or per-repository slot count.
- One task, one worker, one worktree, one branch. LINA Core creates the worktree and the branch, named by task id, and starts the worker with that worktree as its working directory. A worker's own worktree option is never used, and two workers never share a worktree. Git refusing one branch in two worktrees is a second guard, not the first.
- The manager session selects work and its order; the work engine reserves capacity, obtains Node bids and dispatches the recorded assignments. The one-coordinator rule of the Work engine holds: a worker is never used through its own team or coordinator feature.
- When a task has landed, the work engine releases at once every task that was waiting only on it.
- A task that cannot start has exactly one visible reason: a dependency, an area held by another task, a worker out of quota, or host capacity.
- When slots are free, the manager may start a dependent task as a stacked pull request on an open predecessor. It records the predecessor and its head as the task's base, and Core checks capacity and disjoint write-area leases before dispatch. The dependency remains: the dependent cannot land before the predecessor. The worker brings the stack onto the landed base and owns its resulting checks to green before the manager checks the delivery.

## Workers

Workers are coding tools the user installed or signed in to: Codex, Claude Code, Antigravity and Devin. LINA drives each worker through one worker adapter. Codex is the first, driven through its app-server protocol over stdio; Claude Code, Antigravity CLI and Devin are further adapters on the same boundary. Each worker thread is a per-task coding-agent session: it implements the task packet, opens the task's pull request, waits for CI and review bots, fixes every CI failure and the P0 and P1 findings once, and reports done with a completion receipt only when the resulting head is green and no P0 or P1 remains. Review and CI stay with that session and never return to the manager for triage.

A worker runs with the user's own installation and configuration: its version, login and model provider, MCP servers, plugins and integrations such as `gh`, trust settings, repository instructions such as `AGENTS.md` and skills, sandbox and approval mode. LINA Core and the worker run as the user's own OS user ([runtime.md](runtime.md)). LINA does not ship, pin or configure the worker, does not override its model provider, does not install a plugin, hook or skill into it, and does not isolate it from the user's setup. What LINA adds is given per thread: the task packet and LINA's own work tools. LINA plugins never reach a worker; a worker uses the integrations connected in the worker itself ([integrations.md](integrations.md)). Workers serve every agent. A worker's model calls go through the worker's own login; LINA's provider credentials never reach a worker. LINA gives a worker no review procedure, stage workflow or completion hook.

LINA gives a worker two things: its per-thread instructions (the task packet) and LINA's own work tools. LINA Core reads GitHub's records and the completion receipt, checks protected refs, and reads the user's Codex rollout files directly for discovery (see Observation, handover and discovery).

### Adapter boundary

Every worker adapter offers the work engine the same operations:

- start a task: open a thread in the task's worktree or work directory, with the task packet and LINA's work tools
- continue it: input to an idle thread, or steering of an active turn
- interrupt it
- stream progress: turn and item events, and usage when the worker reports it
- route the worker's questions and approval requests to the originating conversation, and return the answers
- carry calls of LINA's work tools, receipts included, to LINA Core
- report the worker's name and version
- signal completion: a typed finish (done, or failed with a category such as quota, rate limit, login expiry or connection) as an event; the adapter polls only for a worker that offers no event (see Completion signals)
- declare the worker's capability set when the thread starts; LINA Core checks it, and a missing capability fails the dispatch visibly
- report the worker's remaining quota where it exposes one; unknown is a value

### Compatibility

- The adapter records the installed worker's version with every attempt.
- LINA lists the worker versions each adapter has verified in the release manifest ([runtime.md](runtime.md)). When the installed version is not on the list, LINA tells the user that the version is unverified, with the version it found and the verified ones, and still delegates.
- LINA refuses delegation to the worker only when the adapter can't connect to it or the protocol handshake fails (for Codex, `initialize`), and names the cause.
- LINA never installs, updates or pins the worker.

### Codex adapter

- The adapter is a stdio JSON-RPC client written over Go types generated from the JSON Schema that `codex app-server generate-json-schema --experimental` produces. The types are generated for each verified Codex version and committed with the list of verified versions, as generated upstream code under the vendoring rules of [dependencies.md](../policy/dependencies.md): never edited by hand, and regenerated on sync. With an unverified version, the adapter uses the types of the newest verified version.
- The schema baseline is recorded separately from runtime verification: extracting a schema and compiling its Go types does not add a worker version to the verified-version list. The committed generator reproduces the types offline from the unmodified schema. Optional settings remain omitted, request ids preserve string/integer identity, and unknown tagged variants retain their wire JSON for the adapter to classify.
- The app-server is experimental and is not a production-supported interface. The list of verified versions and a round trip on each listed version absorb that risk. A new experimental field is used only after LINA records a judgment for it.
- Closed sets of variants are switched on exhaustively, and the `exhaustive` analyzer in `foundation` fails a switch that misses a variant.
- Wire values come from the generated types, never from documentation examples.
- The session opens with `initialize` carrying `capabilities.experimentalApi: true`, then `initialized`.
- Methods used: `thread/start`, `thread/resume`, `thread/read`, `thread/list`, `thread/loaded/list`, `thread/archive` (explicit close only), `turn/start`, `turn/steer` (`expectedTurnId` required), `turn/interrupt`, `skills/list`, `account/rateLimits/read`.
- Server requests handled: `item/tool/call`, `item/commandExecution/requestApproval`, `item/fileChange/requestApproval`, `item/permissions/requestApproval`, `item/tool/requestUserInput`, `mcpServer/elicitation/request`.
- Notifications consumed: `turn/*`, `item/*`, `thread/tokenUsage/updated`, `account/rateLimits/updated`, `serverRequest/resolved`, `error`, `hook/*`.
- The adapter calls no other method. LINA never changes the user's Codex settings or plugins.
- The same JSON-RPC shape never implies the same capability. `turn/completed` is never evidence of a product effect.
- LINA Core starts the installed Codex as `codex app-server --listen stdio://`, a stdio child with LINA Core's environment and the user's own Codex home. On a remote device the registered Node starts it. When its standard input closes, the child exits normally. The only client is LINA Core, or on a remote device that device's registered Node.

### Readiness

Delegation to a worker is ready when:

1. the installed worker is found; an unverified version is reported and passes (see Compatibility)
2. the adapter's session opens (for Codex, `initialize`; for another worker, the first step of its own protocol)

When a check fails, delegation is reported as unsupported with its cause. Conversation readiness is separate and can pass while worker readiness fails.

### Assignment

1. For file-producing work, LINA Core creates the worktree and records the parent clone's protected refs in the ledger.
2. The adapter starts a thread with the worktree or work directory as its working directory, LINA's work tools and the task packet. For Codex this is `thread/start` with `cwd` and `dynamicTools`, and the task packet goes out in the first turn. Model and effort come from the worker profile when it names them, and otherwise from the worker's own configuration.
3. LINA Core compares the returned working directory, model and effort with the request. A mismatch stops the assignment.
4. LINA Core records the worker and its version, the thread, and the instruction sources and skills the worker reports (for Codex, `instructionSources` and `skills/list`).

Code-change work ends in a pull request that the worker opens, and goes through Judgment and delivery. Work without a pull request, such as an audit, needs no source change and completes by its artifacts. Both run on one task with a task packet and end in a completion receipt. The assignment judgment looks separately at the instruction received, the settings requested and the settings returned.

### Control

- An idle thread receives `turn/start`. An active thread receives `turn/steer` with `expectedTurnId`.
- Right before any input, LINA Core checks the thread, the turn, the process generation and the binding epoch again. A thread id alone never controls anything.
- The protocol session and its process state establish whether a worker is active, waiting, dead or stalled; screen text never does. Recovery resumes the thread, or restarts a dead worker, once in the same work area before escalation, preserving the task, branch, receipts and the record of its review and CI fixes. Recovery does not grant another review-fix pass (see Stall and failure).
- An open question or approval blocks ordinary input to that thread.
- A worker thread on a remote host is not controlled until that host's thread, generation and epoch are bound again.
- `turn/interrupt` is sent only with intent to cancel. The allowed reasons are a user request, the budget cap policy, a Node policy cancel (an observed disconnect from the main, grant expiry or revocation), and the stall rule: a worker that is alive but stalled past the stall rule is interrupted once so that its thread can be resumed (see Event-driven flow). Any other automatic interrupt goes to the user as a decision.
- A cancel is final only when `turn/completed` reports `interrupted`.
- Pause stops LINA's delivery to the work thread. It does not stop execution. Paused work never resumes automatically.
- Close keeps files, branches, worktrees and receipts and never deletes the thread. `thread/archive` is sent only on an explicit close.

### Completion signals

LINA Core, or the Node on a remote device, starts every worker as a child process or a protocol session it owns. The end of a turn, a typed failure and the end of the process therefore reach the adapter as they happen, and LINA Core never polls a worker to learn that it finished. Each adapter turns the signal its worker gives into a completion event. The signals below are the expected defaults, confirmed when each adapter is built:

| Worker | Signal the adapter consumes | Read at an interval |
| --- | --- | --- |
| Codex | The turn-completed and thread-status-changed notifications of the app-server session, and its account rate-limit notifications | No |
| Claude Code | The final result message of its streaming output, then process exit. Exit without a result is a failure | No |
| Antigravity CLI | The result event and status of its streaming output, then process exit. Success is confirmed from the pull request, never from the exit code | No |
| Devin | The status of its session, read about every 10 seconds only while a task is in flight and until a terminal state | Yes: the one worker with no outbound session event found |

- A completion event ends a turn. It is not a delivery. For code-change work the worker reports done only after it has waited for CI and the review bots and handled CI failures and P0 and P1 findings inside its turn (see Operating the pull request). The completion receipt names the resulting green pull request head; the manager still checks the work definition and Core records the merge before the task has landed. A session that dies or stalls before delivery follows once-only recovery in the same work area, not a done signal.
- LINA installs no hook, plugin or setting in a worker to obtain a signal.
- A worker that reports a typed failure (an error code, an authentication or billing category, a rate-limit category) produces a failure event with that type.

### Approvals

- When the worker asks for an approval (for Codex: a command, a file change or permissions), the adapter routes the request to the originating conversation, and the person's answer goes back to the worker. On a remote device the Node relays both.
- The worker's own sandbox and approval mode decide what it asks. LINA imposes no sandbox or approval policy of its own on a worker.
- Arguments of the worker's tools are never rewritten (`updatedInput` is never used).
- A linked worktree shares the common `.git`, so the protected-ref check is what keeps a worker's result to its own branch (see GitHub writes and merges).

### LINA tools for workers

- LINA's work tools reach a worker through its adapter: for Codex, `thread/start.dynamicTools` in the namespace `lina`. LINA Core identifies the caller by thread, turn and call ids. For another worker, the adapter uses the per-run mechanism of that tool that installs nothing in the user's setup. Where a tool has no such channel, the adapter builds the receipt from the tool's final result and the pull request it opened, and marks it adapter-derived.
- The tools are: submit a completion receipt; ask a question; send a coordination request (to LINA Core only, never to another worker); read the task projection, which is the LINA materials and memory projections the task may read. GitHub work and other integrations need no LINA tool; the worker uses its own setup.
- LINA Core declares the tools and handles `item/tool/call`. The receipt and question schemas are LINA's own and are versioned with the worker adapter.
- Only the worker thread itself submits receipts.
- A projection read returns only the scope the task allows, carries a revision, and never returns a revoked item. A write happens only on an explicit request and passes the grant check.

### Completion receipt

The completion receipt is how a worker reports back, and the only receipt a worker submits. Its schema is LINA's own and is versioned with the worker adapter, and each receipt carries the schema version it was written under. In the ledger it maps onto the pinned CRW shape by adding fields only (see Contract input from CRW).

- A completion receipt names the assignment id and the execution generation, a result word, the output (for code-change work the pull request and its head SHA, otherwise the artifact paths), the GitHub writes the worker made, and the commands it ran with their outcomes. It reports the outcome of CI and the worker's one pass of P0 and P1 fixes, including anything still unresolved. The receipt of a fix generation also states how the result now meets the work definition sent back by LINA Core.
- What a receipt states is a claim. LINA Core reads it against the pull request's head and GitHub's review and check records, and for work without a pull request against the artifacts (see Judgment and delivery).
- A turn that ends without a completion receipt is not a delivery. LINA Core records it, never reads the worker's final text as a receipt, and may ask for the receipt as ordinary follow-up input (see Control).
- The result word is a structured field bound to the schema version:

| Result word | Task state | Notes |
| --- | --- | --- |
| `DONE`, `NOOP` | ready for review | `DONE` is never `verified`. |
| `BLOCKED`, `UNSAFE`, `NEEDS_HUMAN` | input needed | The original word is kept and shown. |
| `BUDGET_EXHAUSTED` | stopped or failed | Decided by the budget policy. |

### Remote devices

- On a remote device, the registered Node starts and restarts the device's installed worker as its own stdio child. Events and server requests (approvals, questions, dynamic tool calls) reach LINA Core over the `node` connection ([host-protocol.md](host-protocol.md)), and answers return the same way. LINA Core never connects to a remote app-server.
- For coding work, the Node creates the worktree in the device clone, records the protected refs and reports them. The worker on the device pushes its branch and opens the pull request with that device's own GitHub setup, and its receipt names the pull request and its head SHA. LINA Core reads the pull request from GitHub. No commit travels between the Node and the main.
- Remote coding work is unsupported until the Node's relay of the worker is verified and listed in the supported-combination table.

## Event-driven flow

LINA Core reacts to a finished worker within seconds and does not poll for it. The reaction is one path whatever the worker: a completion event is recorded, claimed and acted on.

Every task is making progress, waiting on a named thing with a deadline, or being recovered. A wait records what will wake it and when it must be checked again; an expired wait is reconciled and moved forward or recovered. Work never sits idle without one of these states.

### Completion events

- A completion event is a row in LINA Core's database before anything reacts to it. Its key is task, attempt and head SHA. A claim on an event is idempotent, and an event whose head SHA is not the task's current head is ignored.
- The same queue carries conversation messages and completion events, so one reaction path exists. Core dispatches them mechanically to the bound manager session; project judgment stays in that session.
- Events for one project are handled together from its current ledger state, preserving each event's receipt. The batch and busy fallback of Manager session lifecycle keep the manager from building a backlog of separate turns for them.
- Reaction time is measured from the event to the first read of the pull request. The end of the flow is bounded by CI and review, which LINA does not control.

### Reaction

When a completion event arrives and the task has a pull request, LINA Core:

1. reads the pull request once: its state, head SHA, review threads and the check results at that head ([integrations.md](integrations.md#completion-signals));
2. delivers the ready receipt to the manager session for the pull request operation of Judgment and delivery; review and CI fixes remain in the work thread, and the manager checks the work definition at merge;
3. when the merge has landed, releases the tasks that were waiting on it at once (see Parallel hand-off).

A head that moved after LINA Core's last read is read again, and the merge is pinned to the head that LINA Core last read; the review is not repeated.

Optional: when the repository has required checks and auto-merge, LINA Core may request the merge pinned to the head the manager checked against the work definition after the worker has delivered green. GitHub enforces the checks at merge. The merge instant then moves to GitHub; the task lands when GitHub reports it merged. This is off by default.

### Stall and failure

- A task has failed when its worker's process or session ends without a final result, or the worker reports a typed failure. A quota or rate-limit failure parks the worker (see Parallel hand-off).
- A dead worker is detected from the end or confirmed absence of its process or protocol session. A live worker is stalled when it makes no progress and has no named wait within its deadline for the longer of the stale floor (see Liveness) and twice the median duration of finished tasks of its size class, never more than three hours. Silence prompts a read of protocol state; screen output or a timeout reading the session never proves that it is dead. Events that repeat without progress do not reset the stall interval.
- Every 5 minutes LINA Core re-reads, with no model call, every task marked in progress or watched, and does the same once after a restart. This catches a signal lost to a crash. It is the reconciliation of signals that integrations.md describes, and it never replaces the events.
- A failed, dead or stalled worker is reported at once in the running list while LINA Core recovers the task. LINA Core resumes its thread with a short continuation, or restarts it when the process or session is dead, once as a new attempt of the same task in the same work area, preserving its files, branch and receipts. When the cause is quota, the task goes to the next worker by the same choice instead. Recovery comes before escalation to the person; a second failure or stall goes to the person as a decision. There is no third automatic attempt.
- A dispatch again follows Effects and unknown outcomes: it happens only when the earlier attempt has no effect with an unknown outcome.
- A Node that disconnects is handled by the Node rules, not the stall rule: the effects of its running turn are unknown and are reconciled first.
- A landing that leaves another task's pull request unmergeable sends that task back once to the same worker as a rebase request. LINA Core never resolves a conflict itself, and a second failure goes to the person.
- None of this interrupts the person. The running list changes; surfaces.md governs the few events that notify.

## Judgment and delivery

A worker returning a completion receipt and the manager accepting the result are completions on different layers. The worker owns the pull request to green: it waits for GitHub's CI and review bots, fixes every CI failure and the P0 and P1 review findings once inside its turn, and reports done only when the resulting head is green and no P0 or P1 remains. It reports an unresolved failure honestly instead of reporting done. The manager session checks that delivery against the work definition at merge; Core records the judgment and enforces the merge preconditions. This check re-runs no test or command and starts no separate review by a worker. Core's QA run after each merge is separate from delivery acceptance.

- A verdict is `verified` or `needs_changes`. The manager session makes it and LINA Core records it in the ledger with the records that decided it (the head SHA, the check conclusions, the review records and the check against the work definition). A `needs_changes` verdict names the unmet done condition, scope or design reference; review findings and CI failures are never a separate return from the manager.
- Every task, with or without a pull request, runs on one task packet and ends in a completion receipt. Only code-change work, which has a pull request, goes through steps 5 to 7. Work without a pull request ends at step 4.

Delivery of every task:

1. LINA Core registers the assignment before the worker's first output.
2. For each execution generation the worker submits a completion receipt for the actual output path. It is held until the end of the turn is observed.
3. LINA Core records the receipt in the ledger and acknowledges it.
4. **Read.** For code-change work LINA Core reads the pull request the receipt names, once: its state, its head SHA, its review records and the conclusions of the required checks at that head. It checks that the head matches the receipt and that the protected refs did not move. A pending check or review stays a named wait with a deadline in the worker's turn (see Event-driven flow); it never becomes a findings round trip through the manager. For work without a pull request the manager reads the artifacts against the done condition once and judges `verified` or `needs_changes`, and Core records the verdict.

Code-change work continues:

5. **Check the work definition.** At merge the manager session checks the delivered result and the pull request's diff against the done condition, scope (including the write area) and design reference. This is the design-conformance review. It does not triage GitHub findings, classify failed checks as P1 or direct their fixes. No delivery is checked by a panel of several verifiers.
6. **Return a missed work definition.** Only when the result misses the work definition does the manager send work back through Core to the same worker, once, as a new execution generation, naming what was not met. An idle thread receives it with `turn/start`. The worker owns that generation's CI and review fixes too. After its receipt the manager checks the entire resulting head against the work definition, including its diff and write area, so new changes cannot bypass the done condition, scope or design reference; it starts no separate review or CI fix loop. A remaining gap blocks the task and closes the pull request with that reason, without a `verified` verdict, and is reported to the person, who can ask for a corrected work definition (see Messages and questions).
7. **Merge or close.** When the manager judges the work definition met, Core records the `verified` verdict and merges that head through the merge slot only when the required checks hold at the head it last read, no P0 or P1 finding is open and the pre-merge safety rule of [product-families.md](product-families.md) holds. P2 and P3 findings are recorded with the result and do not block the merge. A worker that reports a remaining CI failure or P0 or P1 after its fix pass has not delivered green: the manager asks Core to close the pull request with that reason and record the task as blocked without a `verified` verdict, rather than returning review or CI work. The merge ends as an apply record (see GitHub writes and merges), and the task has landed when one read of GitHub shows the pull request merged (see Event-driven flow). Core then starts QA for that merge and delivers its result to the manager for follow-up judgment. Core also closes a pull request on the manager's decision, with its reason written on it, in the case of step 6 and when the task is cancelled.

Every message LINA Core sends to a work thread, and every result a work thread returns, passes through separate delivery layers: `transport_accepted`, `received`, `agreed`, `applied`, `verified`. Each layer is recorded with its own evidence, and no layer implies the next. Delivery layers are not effect states ([host-protocol.md](host-protocol.md)). A worker's background completion notice is never a delivery acknowledgement.

### Messages and questions

- A message has a logical message id, a payload, a recipient, an attempt key and an engine receipt, each recorded separately. A message is claimed before it is delivered.
- Messages to and from work threads follow the resend rules of [host-protocol.md](host-protocol.md): the same logical id and content, and only after a rejection before delivery is proven.
- The messages of an agent exchange are not messages to or from a work thread. Their delivery states and resend rule are those of the exchange's mailbox ([conversation-and-memory.md](conversation-and-memory.md#agent-exchanges)).
- Delivery, rendering and acknowledgement are recorded separately. A child result that arrives late is recorded as a candidate against its stale revision.
- When no recipient is active or the task is paused, LINA Core keeps the task's inbox and operational notices.
- Questions and results return to the conversation that made the request and to its current reply thread. The user never relays messages. A follow-up instruction for a task reaches only that task.
- Follow-ups, approvals, cancels and handovers are bound to their target task or thread id and never become execution authority for another thread.
- A worker asks through the LINA question tool. When `item/tool/requestUserInput` or `mcpServer/elicitation/request` arrives, LINA Core routes it as a question to the originating conversation.
- A question whose decision owner is the user (`decision`) is answered only by the user. No model thread answers it. Every answer is recorded with the person who gave it.
- A question or approval that reaches the person is written in plain words in the decision shape of [Reply shape](conversation-and-memory.md#reply-shape): what happened, why it matters, the recommendation first and the one reply that approves it. The worker's own text, ids and logs are in Details. An unanswered question is a decision row or a blocked row in "Waiting for me" ([surfaces.md](surfaces.md#waiting-for-me)); it never interrupts a reply that is being written.
- A question is shared between tasks only when decision owner, permissions, budget, goal and dependency revision are all identical. A task waiting for a decision never blocks unrelated tasks.
- The questions and results of a background run return the same way, to the conversation that asked, through its background notice ([conversation-and-memory.md](conversation-and-memory.md#background-results)).

## Effects and unknown outcomes

Effect states and idempotency keys are defined in [host-protocol.md](host-protocol.md), the effect ledger in [runtime.md](runtime.md), and epochs, reconciliation and the task owner's disposition of irreversible effects in [main-authority.md](main-authority.md). Work applies them as follows:

- When LINA Core restarts, running worker turns are cut. Their effects are unknown. After the restart LINA Core reconciles with `thread/resume` and a `thread/read` snapshot.
- On reconnect, LINA Core reconciles with `thread/loaded/list` and `thread/read`. When a pending server request does not arrive again, the attempt is marked unknown and needing attention, and the originating conversation is told.
- When a Node restarts or disconnects, the effects of its running turn are unknown and are reconciled. On a Node policy cancel the Node sends `turn/interrupt` and reports STOPPED or UNKNOWN.
- A GitHub write or merge with an unknown outcome is settled by reading GitHub's state ([integrations.md](integrations.md)). It is never repeated automatically.
- A stalled or failed task is dispatched again at most once, as a new attempt of the same task and never as a replay of an earlier command. It is allowed only when the earlier attempt has no effect with an unknown outcome. A worker that is alive and silent has no unknown effect by silence alone. When a worker's process is gone or its turn was cut, the effects of that turn are unknown until LINA Core has reconciled them with a read of the thread and of GitHub; the task is dispatched again after the reconciliation settles each of them as applied or not applied. An effect that stays unknown is never re-run by a dispatch again; it waits for reconciliation or the task owner's disposition ([main-authority.md](main-authority.md)).
- A task whose merge has an unknown outcome releases none of its waiting tasks until a read of GitHub shows the pull request merged.
- A dispatch again is a new attempt and the continuation of the same task, not a second delivery of its first work definition: it starts from what the thread and the branch already hold.
- A turn completion, a model's final text, an exit code and a policy echo are never evidence of an effect or of authority.

## GitHub writes and merges

The role rule is in [product-families.md](product-families.md): LINA Core merges and closes the pull requests of its delegated work, and a person may merge, fix and open pull requests. Pre-merge safety and a result record are mandatory for every merge. The GitHub plugin, the GitHub-side merge preconditions and the recording of merges by people are defined in [integrations.md](integrations.md). This section fixes the parts that belong to work.

- The worker does all the GitHub writing of its task (push, pull request, edits to its body, review replies) with its own GitHub setup, such as the `gh` login, SSH or a GitHub MCP server in the worker. LINA does not issue, inject or restrict a GitHub credential. LINA Core authors no delegated push, commit or pull request, and edits no pull request body.
- The GitHub writes a worker makes are reported in its receipt, and LINA Core records them.
- At assignment LINA Core records the parent clone's protected refs in the ledger. When a receipt arrives, and again right before a merge, LINA Core compares the receipt's head SHA and the protected refs with what it reads. On any mismatch it does not merge and tells the originating conversation.
- LINA Core's own GitHub writes on delegated work are merging and closing the task's pull request. It serializes merges into a shared base branch through merge turn slots, whichever agent requested the task. It takes the slot, reads the head, runs the protected-ref check and the preconditions of [integrations.md](integrations.md), merges with that head pinned so that a commit pushed after the read cannot be merged unseen, and releases the slot. When the repository has a merge queue, LINA Core hands the pull request to the queue instead; queued is an intermediate state, and the task has landed when GitHub reports the pull request merged. Letting GitHub merge on green instead (auto-merge) is not the default.
- A merge call that returns success, or a queue that reports the pull request merged, is confirmed by one read of GitHub. Only that read makes the task landed and releases the tasks waiting on it (see Parallel hand-off).
- LINA Core never resolves a merge conflict for a worker. A pull request that a landing left unmergeable goes back once to its own worker (see Stall and failure).
- Every merge of a task's result ends as an apply record in the work ledger, whoever made it. The record cites LINA Core's own receipted merge or the observed result of a person's merge ([main-authority.md](main-authority.md)). A closed pull request is recorded with its reason.
- An apply record is not a verdict. The task's delivery is judged from evidence as in Judgment and delivery.

## Progress display

Work progress is seen through work cards and the running list built from them. The manager session is readable in the project's Details (see Manager session lifecycle); worker sessions have no session view, no "open session" action and no CLI observation command. This section fixes what a work card shows and where each value comes from; how it is drawn is defined in [surfaces.md](surfaces.md). The person sees outcomes and decisions, not issues, pull requests or checks; the working record behind every value below is in the card's Details ([surfaces.md](surfaces.md#default-view)).

- A 목표, an 일, each 할 일 handed to an agent and each background run have a live card that reports whether LINA is working on it. The card of a task or run names its requesting agent.
- A card's state is one of working, waiting, paused, ready for review, input needed, stopped, failed, done or unknown. Waiting means the task is not running and names one reason: a dependency, an area held by another task, a worker out of quota, host capacity, a limit on background runs, or CI pending. Paused means LINA has stopped delivery to the work thread (see Control). Input needed shows as waiting for the user when the user owns the open question, approval or decision. Ready for review means a result is ready for the person to open; a task whose delivery is still being checked by the manager or merged by Core shows as working. Done requires a `verified` verdict, plus an apply record when the task's result must be applied. A model's completion text, a `DONE` result word or a completed turn never shows done. For the person a card says in one line, in their words, where the work stands. A stage that only LINA handles, such as checking a result against the design, waiting for checks or merging, shows as working and ends as part of the outcome line.
- A 할 일 has two separate states: its planning state (the state categories under The to-do record) and the observed state of its work card. The card never sets the planning state, except that a done card, which requires acceptance of the delivery, completes the 할 일.
- A background run's card uses the same state words: `waiting` is waiting, `working` is working, `needs_input` is input needed, a landed run with a complete or partial result is ready for review, `failed` is failed and `cancelled` is stopped. A run never shows done, because done requires a `verified` verdict and a run's result is a claim. The running list drops a landed run once the person has opened or dismissed it ([surfaces.md](surfaces.md#running-list)).
- Each stage of a task (the worker's run, including its review and CI fixes, and for code-change work the manager session's check against the work definition and the merge or close) has a short summary built from actual results. A stage without results is unknown. Each summary says where its facts come from: the worker's report, GitHub (review, checks), the manager session's check against the work definition, or unknown. The card also carries the execution record and the acceptance-criterion references. The Details of the card show these; the default card shows one line.
- Counts include only the required children of the current generation. Optional or superseded children, and verifiers of another revision, never count toward overall success.
- The ladder of a delegated task (ready, accepted, posted, reported, verified, delivered to the user, confirmed by the user, accepted by LINA Core, merged or released) is the audit trail of the task. It opens in Details, each rung with the evidence that sets it, and it is not part of the default card.
- The developer view of a card is hidden by default and turned on in settings. It adds the worker and its version, the thread id, the attempt and the receipts to the card's Details, and changes nothing in the default card. For a background run it adds the brief, the allowlist, the limits and usage, and the run's transcript. Stages, counts and times are the same as in the default view.
- **Outcome line.** When any background work lands, the person is told in one line what changed for them, and the proof is a card. The line begins with "done" only when the done condition is met; otherwise it says partial, failed or stopped, and what is missing. It states in a clause what was verified and what was not. A failure is one line with its cause in plain words, and a row in "Waiting for me" when the person must decide. Work is never reported as an issue number, a pull request number, a branch or a check result.

### Progress line

LINA Core keeps one progress line for each 일 and each 목표 that has work in flight or something waiting on the person. The line is the person's view of where a piece of work stands, and it is the only form in which the progress of an 일 or a 목표 reaches a surface ([surfaces.md](surfaces.md#progress-at-a-glance)). It is computed from the records and the work cards. It is never written by hand and never taken from a model's estimate.

| Field | What it holds |
| --- | --- |
| Name | The title of the 일, or the sentence of the 목표 |
| Progress | A stage and a simple bar |
| Just finished | The latest outcome in a phrase, taken from the outcome line of its latest landed 할 일 or background run |
| Next | The next 할 일 or stage, in a phrase |
| Waiting on the person | Whether a row of "Waiting for me" belongs to it, and how many |

- The stage of an 일 is the stage of its plan that holds its open 할 일. An 일 with no stages of its own has four: 설계 (design) while its plan is not confirmed, 만드는 중 (building) while any of its 할 일 is open, 확인 (checking) when every 할 일 has completed and the done condition is being checked, and 끝 (done) when the done condition is met. The bar is the share of its 할 일 that are completed, each counted equally.
- The progress of a 목표 is the share of its 일 that are done, and its stage is that of its least advanced running 일.
- A value that cannot be observed is unknown and is shown as unknown, never as zero, as the rule of Plans says. A line says done only when the done condition is met.
- A 목표 has a line when one of its 일 runs or something waits on the person. An 일 that belongs to a 목표 is a line below it, and an 일 that stands alone is a line of its own. A finished item leaves the lines once the person has seen it.
- A line holds no issue number, pull request, check, log or id. The tasks and runs behind a line are the running list (in progress, waiting, landed) limited to that 일 or 목표, and the state words are the same in both. They open on request, from the Details of the line ([surfaces.md](surfaces.md#default-view)).
- The digest that an external channel shows is made from the same lines ([integrations.md](integrations.md#channels)).

### Running list

The running list is the person's view of the flow: a short list of what is in progress, waiting and landed, not a stream of notices. LINA Core owns it, and it is shown on Home and in the sidebar ([surfaces.md](surfaces.md)); the TUI shows it as a list and a count in its status line. It lists the tasks and background runs one by one; the progress lines summarize the same work for each 일 and 목표 (see Progress line).

- A row is one task or one background run. It has one of three states. In progress: a worker (or a background sub-agent) is working on it, or its pull request is being operated on. Waiting: it is not running, with one visible reason: a dependency, an area held by another task, a worker out of quota, host capacity, CI pending, a question or decision for the person, or a pause. Landed: the task has landed (see Terms), or the background run has ended with its outcome.
- The states map onto the work card states: working is in progress; waiting, paused and input needed are waiting; done is landed. A stalled or failed task shows its state word until it is dispatched again (see Stall and failure).
- A row is one line in the person's words about the outcome, not about the pull request. Rows that need the person come first, then the rest in the order they started. Opening a row opens the task's card.
- A running count, such as "2 running, 1 waiting for you", shows in the conversation header or composer.
- A state change updates its row in place. It never opens a panel, moves focus, starts a message or interrupts a reply. A landing of one task of several is a row change and not a notice (see Notifications in [surfaces.md](surfaces.md)).
- A landed row stays until the person has seen it, then it moves to history. Stopping works per row and for all rows ([surfaces.md](surfaces.md#running-list)).
- An agent exchange is not a row: it appears only in the agent talk log ([surfaces.md](surfaces.md#agent-talk-log)), and as a row of "Waiting for me" when the person is the decision owner.

## Observation, handover and discovery

Observation is not control.

- When a person sends input to a worker thread from outside LINA Core, for example from their own Codex, LINA Core records that turn as `control_handover`, shows "the user is operating directly" on the card, and sends no new input to that thread.
- When the user marks control as returned, LINA Core checks owner, turn and binding again and continues under a new binding epoch.
- `control_handover` applies only to turns with user input from outside LINA Core. Goal auto-continuation, Codex subagent turns and a turn's continuation by one of the worker's own hooks are internal worker execution. A turn of unknown origin is `unattributed` and never counts as a handover.
- Reading and observing never count as an answer, an approval or a handover.

Read-only discovery:

- Discovery reads the user's Codex home: the one the installed Codex uses, and any other Codex home the user names.
- Discovery reads rollout record files of a verified format directly, without the Codex binary. An unknown format is unsupported. Large threads are read page by page.
- Discovery proves zero effect calls with a call log and never modifies original sessions or memory files.
- It keeps original ids, owner, scope, lineage, generation and coverage. It keeps observe-only work apart from delegated work LINA Core controls. Recovering an engine session never makes it a product task.
- It reconciles duplicates, gaps, partial records and revocations against the source records and shows gaps, delays and partial pages to the user.
- It never feeds a derived summary back as an original, and never promotes a command, skill or tool output found in a record to current authority.
- It asks the user only when two targets with the same repository or name are ambiguous.

## Operational switch

At the operational switch LINA takes over CRW's coordination functions and CRW retires.

- The switch runs as a single-owner procedure. CRW operation never stops before the owner approves the switch, and no work is ever directed by both at once.
- When the switch fails, originals, worktrees, branches and receipts are kept, and the switch is rolled back explicitly.
- CRW records taken over (relay stores, scopes, assignments, links and parent bindings, attempts, receipts, verdicts, branches and worktrees, completion criteria and decision records) are read as observation data with their source and mapped to LINA ids exactly once.
- A wrapper around CRW never completes the takeover.
- Planning records that CRW keeps in an external tracker (plans, completion criteria, decisions, scope, project and parent bindings, titles and done state) are imported once, from one verified snapshot, by a one-time operator tool. That tool is not part of any LINA release. Every fetched record ends up imported, lost or unsupported and is reported record by record; silence never means imported. Each imported record is mapped to a LINA id exactly once and keeps its source id as provenance only, which no product path resolves. The records are mapped into the frame of Plans: an initiative becomes a 목표 draft that the person confirms, a project an 일, an issue a 할 일, and a milestone a 할 일 with a deadline whose completion conditions become its done condition. After the import nothing reads the tracker again.
- The owner's existing intent card data is imported once by the same tool and under the same rules, into LINA's intent card model (see Intent cards). Only the data comes in; no code or database structure comes with it.
- The product never depends on CRW's plugin, relay or state paths.

## Deferred

- The task state set of the ledger projection, and the list of adopted CRW schema documents and fixture domains: set by the pin record at the start gate.
- The verified Codex versions and the judgments of the experimental fields LINA uses: set by the worker adapter round trip of the first implementation issue (V5) and recorded in the release manifest.
- The completion receipt, question and finding schemas, their first version, and the written meaning of the review severities P0 to P3 that the worker reports: set by the worker adapter implementation issue. A receipt maps onto the pinned CRW shape by adding fields only.
- What each worker profile (direct, single and complementary) means, and the values it records: set during work engine implementation acceptance.
- The heartbeat interval H, the concurrency limits per repository, per worker and per area class, the manager's busy wait limit before parallel fallback, the stall bounds per size class, the period of the reconcile pass, the soft size budget, the defaults for background runs (concurrency and the stall check-in) and the budget caps: set by measurement during work engine implementation acceptance and tuned from LINA's own merged pull requests.
- Retention periods for closed workspaces: set during work engine implementation acceptance.
- Whether Codex subagents inside a worker see the `lina` tools, and that a declined approval leaves the action unexecuted: confirmed by QA in the worker adapter implementation issue.
- Remote-device worker hosting through Node and the worker's own pull-request path from the device: verified during Node implementation acceptance; unsupported until then.
- The adapters for Claude Code, Antigravity CLI and Devin: designed against the adapter boundary when each is added. Devin is reached through its session API with the person's own credential.
- The wire format of delivery layers, work events and progress data: added to [host-protocol.md](host-protocol.md) during work engine implementation acceptance.
- The Korean words of the frame and of the date fields, and the exposure of the four-level priority: set by a card sort with non-developers and by use.
- The thresholds of the to-do record (surviving briefings before a question, the rate at which rejected automatic additions fall back to candidates), Korean date parsing coverage (lunar dates, month ends, business days), the commit granularity and inbox form of the records repository, and its behavior at thousands of files: set by measurement during work engine implementation acceptance.
- Retention periods for the directories and transcripts of finished background runs: set during work engine implementation acceptance, with those of closed workspaces.
- The rule table for choosing a worker, and the quota signal each worker gives: provisional, set by measurement.
- The completion signal of each worker other than Codex, including whether Devin offers an outbound session event: confirmed by the adapter of that worker when it is built.
- The stage rule and the bar of the progress line, how a 목표 summarizes its 일, and the order of the lines: starting values set in Progress line, tuned by use.
