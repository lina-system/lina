# Work and delegation

This contract fixes how LINA Core plans, delegates, supervises and accepts work. It covers the work engine and its ledger, the CRW contract schemas and fixtures the engine is built from, plans and intent cards, parent and child roles, how every agent shares the work engine, workers (the coding tools the user installed, driven through worker adapters), the LINA work harness those workers use, how LINA Core judges stage transitions and deliveries, how work handles effects with unknown outcomes, how results reach GitHub and get merged, and what the progress display shows. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). Finishing this document does not mean any part of it is implemented or accepted. Runtime proof belongs to the implementation issues that consume this contract.

## Scope

This contract covers:

- the work engine: queue, work threads, ledger, routing, capacity, budget and result acceptance
- agents and work: one queue, work area and set of workers for every agent, and the requesting agent on every task
- the CRW contract schemas and fixtures as fixed input, and the start gate they set
- goals, projects, tasks, milestones, to-dos and memos, and the intent-card data model
- supervisor, parent and child roles, delivery and verdicts
- workers: the coding tools the user installed, the adapter boundary, the Codex adapter, compatibility, readiness, assignment, control and approvals
- the LINA work harness (`lina-work`) and stage judgment
- effects and unknown outcomes inside work
- GitHub writes and merges done for work
- the progress display data, observation, handover and discovery of work
- the operational switch from CRW

This contract does not cover, and links instead to:

- product families, canon, writers, agents and the role rule itself: [product-families.md](product-families.md)
- grants, epochs, receipts, observed results, authorization and the approval policy: [main-authority.md](main-authority.md)
- the envelope, delivery rules, effect states, idempotency keys, the `node` connection and the supported-combination table: [host-protocol.md](host-protocol.md)
- the language of LINA's components, installed tools, conversation tools, the effect ledger and the sandbox, the model path, usage accounting, packaging and the update order: [runtime.md](runtime.md)
- plugins and the GitHub plugin (identity, field-level canon, notifications, reconciliation): [integrations.md](integrations.md)
- how cards, plans and work cards are drawn: [surfaces.md](surfaces.md)
- Klotho's drafting, project supervision and the QA product map: [cognition-and-life.md](cognition-and-life.md)
- conversation threads, agents' conversations and the memory entry of verified results: [conversation-and-memory.md](conversation-and-memory.md)
- materials, their projections and passage anchors: [materials-and-knowledge.md](materials-and-knowledge.md)
- the state root, the work area's place in it, and where versioned text lives: [filesystem.md](filesystem.md)

## Terms

**Task.** The unit of work in a plan and in the work engine. A task is the size of one pull request. It references acceptance criteria, and it owns its procedure, its pull-request scope, its execution record and its verification evidence. It records its task owner, the person it is done for, and its requesting agent, the agent whose conversation asked for it, or none when the person started it directly ([main-authority.md](main-authority.md)).

**Work thread.** The execution of one task by one executor: a worker thread, or a command sequence on a Node. A work thread is a different object from a conversation thread.

**Attempt.** One run of a work thread. An attempt records the worker and its version, its thread and its turns, so that every card links to work through task, attempt, thread and turn.

**Execution generation.** A numbered round of a child's work on one task. A `needs_changes` verdict opens the next execution generation on the same child.

**Binding epoch.** The generation of LINA Core's binding to a worker thread. A new binding epoch starts when control returns after a handover and when a remote thread is bound again.

**Protected ref.** A ref in the parent clone that a worker must never move: `dev`, `main` and the branches of other tasks.

**Task packet.** The first input a child receives. It uses the harness task format (TASK, SCOPE, MUST DO, MUST NOT, PROOF, RETURN FORMAT) and adds the LINA assignment id, the receipt requirement, a restore block, and references (id and revision) to the materials and memory projections the task may read.

**PABCD.** The LINA work harness's stage loop for code-change work. Its stages are Plan (a plan for the change, inside the task's scope), Audit (an independent review of that plan before anything is built), Build (the change), Check (the verification commands the plan names run and pass) and Done (the work phase closes). An optional Interview stage before Plan settles unclear scope; its questions go through the LINA question tool. A stage advances only through an explicit transition that carries its evidence. The harness never advances a stage by itself, and every transition waits for LINA Core's verdict (see Stage judgment and delivery).

**Work phase.** One pass through the PABCD loop, from Plan to Done, on one unit of a task. A task runs one or more work phases, and Done closes the current one.

**Goalplan.** The harness's durable plan for a child work thread: the objective, its work phases and their steps, the success criteria with the evidence each one expects, checkpoints, and decisions with their reasons. Completion needs every criterion met. A goalplan binds to one child work thread.

**Stop hook.** The harness hook that runs when a managed turn of the worker is about to end. While a work phase is in progress, it blocks the end and names the next step, so the same turn continues; it never moves a stage. It lets the turn end when the work stalls or the context is nearly full. It also detects a managed turn that is ending without a declared result or receipt.

**Stop-block limit.** How many times the Stop hook may block within one turn. Past the limit the turn ends, and the state of the work phase stands as it is.

**Subagent evidence gate.** The check that runs when a write-scoped subagent called inside the PABCD loop stops: the subagent must leave an evidence receipt for what it changed. The gate blocks the subagent's stop a limited number of times per subagent and turn, then releases it with the verdict unresolved. A work phase cannot complete while any verdict is unresolved.

**Stage receipt** and **completion receipt.** Receipts in the sense of [main-authority.md](main-authority.md), issued by a worker thread under its task's grant. A stage receipt states that a harness stage or work phase ended. A completion receipt states that a deliverable is ready for review, or that an execution ended without one. A receipt is necessary for a judgment and never sufficient.

**Verdict.** LINA Core's recorded judgment on a stage receipt or on a delivery.

**Apply record.** LINA Core's ledger record that a task's result was merged or otherwise applied, with the evidence that shows it, the agent that requested the change and, when the result was applied through an apply request, the person who accepted it.

## Work engine

The work engine is part of LINA Core. It owns:

- the work queue and one work thread per delegated task
- the work ledger, append-only, and the task state projected from it
- capacity reservation, permissions and budget for work
- result acceptance
- the supervisor and parent roles
- the passage of questions, progress and results between conversations and work threads

The work ledger is canon, and the work engine is its only writer. Moirai reads it by task id and never writes it. Codex thread records are never canon. Recovery starts from the work ledger, receipts and artifacts. The projection is rebuildable from the ledger at any time.

Rules:

- **One coordinator.** LINA Core coordinates. LINA Core and a worker never manage the same sub-execution twice. Harness stages and Codex subagents are the worker's internal execution; LINA Core does not own their scheduling, retries or cancellation. LINA Core judges stage transitions and deliveries.
- **No parent advisor thread.** Judgment belongs to LINA Core and comes from the ledger and evidence only. No model thread judges on LINA Core's behalf.
- **Kind of work.** Conversation and work are split by the kind of work, not by tools. Work that needs a branch or worktree, outlasts a tool deadline, or needs parallel execution or a stage loop goes to the work engine. Checking a file, a short command or a document edit happens in the conversation with the conversation tools of [runtime.md](runtime.md).
- **Routing.** Coding work and worker work such as planning go to a worker thread. Research and organizing go to a worker thread as well; their result is a cited brief whose citations carry passage anchors ([materials-and-knowledge.md](materials-and-knowledge.md)), and a brief that is accepted becomes a `verified` result. Device work goes to the Node on that device. Worker and Node routes use the same grants, ids, effects and receipts. When these rules leave the destination or the worker open, Atropos may ask Jev to choose among the candidates that code fixes; the choice is a routing judgment, never permission to act ([cognition-and-life.md](cognition-and-life.md)).
- **Agents.** Every agent uses the same work queue, work area and workers. The work ledger records the requesting agent of every task, and the task's questions and results return to the conversation that asked, which belongs to that agent. When the requesting agent is deleted, its tasks keep running; their questions and results go to the person's home cards, and the person can hand them to another agent's conversation ([product-families.md](product-families.md)).
- **Workspaces.** Only file-producing work gets a worktree and a branch. Work with no file output runs in a non-git work directory in the work area and completes by its artifacts and receipts. A file-producing work thread has exactly one draft branch, and its result returns as an apply request. Finished and abandoned workspaces are cleaned up automatically after retention.
- **Draft and apply.** Only delegated results go through a draft and an apply request. A document or goal edited through its own conversation thread is saved at once as a saved version.
- **Profiles.** Each worker profile (direct, single, complementary) records its model, reasoning effort, tools, budget, session persistence, completion-verification capability and unsupported scope. A profile names a model or an effort only when it sets one, and then only for the threads it starts; it never changes the worker's provider settings (see Assignment). Choosing a profile is a proposal. The work engine decides whether the work runs.
- **Liveness.** The heartbeat interval H comes from worker events and LINA Core polling. A work thread is stale after max(3H, 90 s). A worker lease lasts at most 5 minutes.
- **Budget.** LINA Core reserves and settles budget for every worker thread, including subagents inside the worker. At the cap the work engine sends `turn/interrupt` under the budget policy. Codex's own goal token budget is never used. Usage accounting is in [runtime.md](runtime.md).
- **Optional configuration.** An installed worker and the LINA work harness are needed only for delegation. When either is missing or fails readiness, only delegation is unsupported. Conversation is never blocked by work, and the conversation and other tasks continue while work runs or waits.
- **Execution owner.** The work engine runs the code, work and skill creation that Klotho requests; Klotho's own limits are in [cognition-and-life.md](cognition-and-life.md).
- **Memory.** Only a result with a `verified` verdict may enter LINA memory, under the writing rules of [conversation-and-memory.md](conversation-and-memory.md). A verified work result is common knowledge, known to every agent, unless the person says otherwise.

### Work area

The work area is `work/` under the state root ([filesystem.md](filesystem.md)). Every agent's tasks share it. Its layout:

| Relative path inside the work area | Holds | Writer |
| --- | --- | --- |
| `clones/<repository-id>/` | The parent clone of each repository that delegated work uses | LINA Core |
| `tasks/<task-id>/` | The task's worktree, or its non-git work directory | LINA Core creates it; the task's worker writes inside it under its grant |

- The work area is not part of the backup generation. Everything needed to recover work is canon and LINA materials: the work ledger, receipts and artifacts.
- When a task's result is ready for review, LINA Core keeps a copy as LINA materials of the task: a Git bundle of the draft branch, or the output files of a non-Git task. A restore that lacks the work area applies the draft from that copy, so an unapplied result survives a lost disk.
- A worker writes only inside its own task directory and the git directories it needs to commit. It never writes a protected ref in a parent clone.
- Task directories are removed only under the retention rules of this contract.

## Contract input from CRW

CRW ([codex-relay-workflow](https://github.com/thisisjun786/codex-relay-workflow)) is the operator tool that coordinates Codex work until the operational switch. The work engine is LINA Core's management session, built anew in Go for LINA's system: it keeps how CRW works (the work ledger and its states, relay delivery and acknowledgement, and the supervisor, parent and child roles), with CRW's contract schemas (`contract/schema/`) and fixtures (`contract/fixtures/`) as fixed input. CRW code is never copied, vendored, translated or linked. The schemas and fixtures state the behavior that matters; CRW's own code and host paths stay with CRW.

### Start gate

- The start gate for the work engine is a pinned CRW schema and fixture version.
- The pin record names the CRW commit, the content hash of every adopted schema document and fixture file, and the adopted fixture domains. It lives in the LINA repository next to the work engine and is listed in the release manifest.
- Work that needs the pinned schemas starts only after the pin: the work ledger and delivery records, delegation, orchestration, worker dispatch from the product, and the takeover of CRW records.
- The pinned schemas fix only the records of the work ledger. The harness round trip and the worker adapter round trip do not depend on the pin. They are acceptance items of the first implementation issue and run first.
- Conversation, memory, persona, TUI, LINA APP screens, materials, cognition outside the work engine, LIFE and the OS base never wait for the pin.

### Use

- The stage receipt and the completion receipt that a worker submits are defined by the schema of the LINA work harness contract, at the harness contract version (see LINA work harness). The harness round trip uses them before the pin.
- The pinned schema documents for relationships, completion receipts, delivery attempts, acknowledgements and verification verdicts fix the shape and allowed values of the matching ledger records. LINA adds fields only for LINA ids and evidence and never redefines a pinned field.
- A ledger record of a receipt maps the harness receipt onto the pinned shape by adding fields only, so a receipt is never defined twice.
- The harness contract extends these records with PABCD stage receipts, work phases and decision waits.
- Single delegation uses these records from its first implementation, so delegation records are never built twice.
- LINA runs every adopted fixture against the work engine with its own runner. Expected values come from the fixture, never from LINA's observed output. A failing fixture is fixed in LINA, never in the fixture.
- A fixture that exercises a CRW-only surface (its CLI, sockets, MCP bridge or plugin hooks) is not adopted.
- Moving the pin is one change that updates the pin record, the release manifest and every affected record shape together.

## Plans

Planning is built into LINA Core. Plans, bindings and summaries live in LINA Core's plan store, and clients read and change them only through LINA Core ([host-protocol.md](host-protocol.md)). No external planning service is ever a dependency ([product-families.md](product-families.md)). The object names below are working names; their product names are a backlog item in [cognition-and-life.md](cognition-and-life.md).

| Object | Meaning | What LINA tracks |
| --- | --- | --- |
| Memo | A captured thought with no place and no category. | Nothing. A memo surfaces only when searched or referenced. |
| To-do | A small item of work. | Done or not done. A to-do is never forced into a hierarchy or an execution. |
| Goal | An outcome LINA is responsible for following, stated in one sentence. | Progress, blockage and completion. |
| Project | A body of work under a goal. The parent role in delegation. | Its tasks, milestones and dates. |
| Task | A pull-request-sized unit of work (see Terms). The child role in delegation. | Its work through the work engine. |
| Milestone | An ordered checkpoint inside a project with completion conditions, an optional date and an assignee. | Its completion conditions. |

Rules:

- What separates a memo, a to-do and a goal is whether LINA is responsible for tracking it, not its name. The user is never asked to classify.
- A goal is one sentence: the outcome it reaches, in the person's own words. That sentence is the goal's name. The first page, the projects and the milestones explain and deliver it; they never replace it with a list of topics.
- Promotion from memo to to-do to goal is proposed by LINA and confirmed once by the user. Silence is not consent.
- A plan is the confirmed structure of projects, tasks and milestones under a goal. A plan drafted by LINA becomes a plan at exactly one point: the draft is saved and the user confirms it. A plan the user writes directly on the plan screen is a plan without further confirmation.
- Goal decomposition, and where it starts, is Klotho's planning function ([cognition-and-life.md](cognition-and-life.md)).
- Structural fields of plan objects (title, description, completion conditions, order, dates, assignment, relations) are canon in the plan store in SQLite. Free text attached to plan objects is versioned text: every edit creates a saved version that can be compared and reverted. Where versioned text lives is defined in [filesystem.md](filesystem.md).
- The latest dates are owned by project and milestone fields. Real precedence is owned by blocked-by relations. A task never copies dates or precedence into its own text.
- Product acceptance criteria are owned by card criterion items. A task references criterion ids with their revision.
- Where a goal stands is computed from the actual results of its tasks. When results are missing or stale, it is unknown.

Development mode:

- Connecting a GitHub repository to a goal, or to part of one, puts that part in development mode. In that part a task has one branch and one pull request, and applying its draft merges the pull request. The GitHub plugin and field ownership in a connected part are defined in [integrations.md](integrations.md). Parts without a repository stay document-centered, and both can live in one goal.
- Document projects are local by default. Connecting a remote repository is optional.

## Intent cards

Intent cards are a common feature of LINA APP. Their data is owned by LINA Core; LINA APP shows the cards and takes input. The card tree states what must be true. Goals, projects and tasks state when and how it gets delivered. The two are axes of one graph:

- A goal cuts across cards. It names the cards it covers and never includes their descendants automatically.
- A task hangs on one or more cards.

The plan has four layers. How they are shown is defined in [surfaces.md](surfaces.md).

| Layer | Object | Writers | Rule |
| --- | --- | --- | --- |
| 0 First page | One page per goal: why, what it becomes when done, where it stands, what waits for the user, recent changes (what, why, who) | LINA and the user | Co-edited. LINA rewrites it when the work layer has new results. Every edit is a saved version, appears in recent changes and can be reverted. "What it becomes" is drafted by LINA and confirmed by the user. |
| 1 Map | The card tree | LINA organizes | "Take a look" requests and Klotho's periodic tidy write the organization directly. |
| 2 Page | One card: a summary at the top and the user's intent field | LINA writes the summary; the user writes the intent | LINA and automatic tidying never overwrite the user's intent text. |
| 3 Work | Tasks with criterion references, execution record and evidence | LINA | Derived from the work engine. |

Card rules:

- The intent field saves automatically as the user types. Each save is a saved version in the card's history.
- A "take a look" request asks LINA to read the saved intent and carry it into the card's summary, the map, criteria drafts and task drafts. Drafts enter the plan only through the plan confirmation point.
- Each criterion item has a fixed id that survives moves and rewording, and a revision that advances on every edit.
- Every change that LINA or tidying makes to a card appears in the card's history.
- A card's execution state is one of none, planned, in progress, delivered or verified, derived from observation of its tasks. When observation is missing or stale the state is unknown, never none or verified.
- From gaps between cards and work, Klotho drafts tasks only. Plan confirmation stays at the one confirmation point.

## Supervisor, parent and child

| Role | Held by | Owns | Never |
| --- | --- | --- | --- |
| Supervisor (optional) | LINA Core | Oversight across several projects | Changes permissions, scope, branches, worktrees or report recipients by being attached or detached |
| Parent | LINA Core, one per project | The ledger and coordination: dependencies, assignment, delivery, verdicts and merge order | Hands judgment to a model thread |
| Child | One work thread per task | Its own goal and harness loop | Merges, judges its own transitions, or inherits the parent's full permissions |

Rules:

- A parent works without a supervisor.
- A child has an explicit role and scope. A request outside its scope is blocked.
- Roles come only from LINA Core's link records. Titles, folders and conversation links never create a role.
- A dependent task is released only after its predecessor has a `verified` verdict.
- A work phase waiting for a decision holds only itself. Independent work phases and tasks continue.

## Workers

Workers are coding tools the user installed. LINA drives each worker through one worker adapter. Codex is the first worker, driven through its app-server protocol over stdio; Claude Code and Antigravity CLI are added later as further adapters on the same boundary.

A worker runs with the user's own installation and configuration: its version, login and model provider, MCP servers, plugins and integrations such as `gh`, trust settings, repository instructions such as `AGENTS.md` and skills, sandbox and approval mode. LINA Core and the worker run as the user's own OS user ([runtime.md](runtime.md)). LINA does not ship, pin or configure the worker, does not override its model provider, and does not isolate it from the user's setup. The one thing LINA adds to the worker is its work harness plugin (see LINA work harness). LINA plugins never reach a worker; a worker uses the integrations connected in the worker itself ([integrations.md](integrations.md)). Workers serve every agent.

LINA gives a worker three things: per-thread instructions (the task packet), LINA's own work tools, and the LINA work harness. LINA Core judges stage transitions and deliveries from receipts and independent checks, checks protected refs, and reads the user's Codex rollout files directly for discovery (see Observation, handover and discovery).

### Adapter boundary

Every worker adapter offers the work engine the same operations:

- start a task: open a thread in the task's worktree or work directory, with the task packet and LINA's work tools
- continue it: input to an idle thread, or steering of an active turn
- interrupt it
- stream progress: turn and item events, and usage when the worker reports it
- route the worker's questions and approval requests to the originating conversation, and return the answers
- carry calls of LINA's work tools, receipts included, to LINA Core
- report the worker's name and version

### Compatibility

- The adapter records the installed worker's version with every attempt.
- LINA lists the worker versions each adapter has verified in the release manifest ([runtime.md](runtime.md)). When the installed version is not on the list, LINA tells the user that the version is unverified, with the version it found and the verified ones, and still delegates.
- LINA refuses delegation to the worker only when the adapter can't connect to it or the protocol handshake fails (for Codex, `initialize`), and names the cause.
- LINA never installs, updates or pins the worker.

### Codex adapter

- The adapter is a stdio JSON-RPC client written over Go types generated from the JSON Schema that `codex app-server generate-json-schema --experimental` produces. The types are generated for each verified Codex version and committed with the list of verified versions, as generated upstream code under the vendoring rules of [dependencies.md](../policy/dependencies.md): never edited by hand, and regenerated on sync. With an unverified version, the adapter uses the types of the newest verified version.
- The app-server is experimental and is not a production-supported interface. The list of verified versions and a round trip on each listed version absorb that risk. A new experimental field is used only after LINA records a judgment for it.
- Closed sets of variants are switched on exhaustively, and the `exhaustive` analyzer in `foundation` fails a switch that misses a variant.
- Wire values come from the generated types, never from documentation examples.
- The session opens with `initialize` carrying `capabilities.experimentalApi: true`, then `initialized`.
- Methods used: `thread/start`, `thread/resume`, `thread/read`, `thread/list`, `thread/loaded/list`, `thread/archive` (explicit close only), `turn/start`, `turn/steer` (`expectedTurnId` required), `turn/interrupt`, `hooks/list`, `skills/list`.
- Server requests handled: `item/tool/call`, `item/commandExecution/requestApproval`, `item/fileChange/requestApproval`, `item/permissions/requestApproval`, `item/tool/requestUserInput`, `mcpServer/elicitation/request`.
- Notifications consumed: `turn/*`, `item/*`, `thread/tokenUsage/updated`, `serverRequest/resolved`, `error`, `hook/*`.
- The adapter calls no other method. Apart from the LINA work harness plugin (see LINA work harness), LINA never changes the user's Codex settings or plugins.
- The same JSON-RPC shape never implies the same capability. `turn/completed` is never evidence of a product effect.
- LINA Core starts the installed Codex as `codex app-server --listen stdio://`, a stdio child with LINA Core's environment and the user's own Codex home. On a remote device the registered Node starts it. When its standard input closes, the child exits normally. The only client is LINA Core, or on a remote device that device's registered Node.

### Readiness

Delegation to a worker is ready when:

1. the installed worker is found; an unverified version is reported and passes (see Compatibility)
2. the adapter's session opens (for Codex, `initialize`)
3. for code-change work, the LINA work harness is installed in the worker at the revision the release manifest records; LINA installs or updates it first when it is missing or at another revision (see LINA work harness)

When a check fails, delegation is reported as unsupported with its cause. Conversation readiness is separate and can pass while worker readiness fails.

### Assignment

1. For file-producing work, LINA Core creates the worktree and branch and records the parent clone's protected refs in the ledger.
2. The adapter starts a thread with the worktree or work directory as its working directory, LINA's work tools and the task packet. For Codex this is `thread/start` with `cwd` and `dynamicTools`, and the task packet goes out in the first turn. Model and effort come from the worker profile when it names them, and otherwise from the worker's own configuration.
3. LINA Core compares the returned working directory, model and effort with the request. A mismatch stops the assignment.
4. LINA Core records the worker and its version, the thread, and the instruction sources and skills the worker reports (for Codex, `instructionSources` and `skills/list`).

Code-change work uses the harness's PABCD loop. Work without a pull request, such as audits and research, uses the path that requires no source change and completes by its artifacts. The assignment judgment looks separately at the instruction received, the settings requested, the settings returned, and the child harness's work-phase state.

### Control

- An idle thread receives `turn/start`. An active thread receives `turn/steer` with `expectedTurnId`.
- Right before any input, LINA Core checks the thread, the turn, the process generation and the binding epoch again. A thread id alone never controls anything.
- An open question or approval blocks ordinary input to that thread.
- A worker thread on a remote host is not controlled until that host's thread, generation and epoch are bound again.
- `turn/interrupt` is sent only with intent to cancel. The allowed reasons are a user request, the budget cap policy, and a Node policy cancel (an observed disconnect from the main, grant expiry or revocation). Any other automatic interrupt goes to the user as a decision.
- A cancel is final only when `turn/completed` reports `interrupted`.
- Pause stops LINA's delivery to the work thread. It does not stop execution. Paused work never resumes automatically.
- Close keeps files, branches, worktrees and receipts and never deletes the thread. `thread/archive` is sent only on an explicit close.

### Approvals

- When the worker asks for an approval (for Codex: a command, a file change or permissions), the adapter routes the request to the originating conversation, and the person's answer goes back to the worker. On a remote device the Node relays both.
- The worker's own sandbox and approval mode decide what it asks. LINA imposes no sandbox or approval policy of its own on a worker.
- Arguments of the worker's tools are never rewritten (`updatedInput` is never used).
- A linked worktree shares the common `.git`, so the protected-ref check is what keeps a worker's result to its own branch (see GitHub writes and merges).

### LINA tools for workers

- LINA's work tools reach a worker through its adapter: for Codex, `thread/start.dynamicTools` in the namespace `lina_work`. LINA Core identifies the caller by thread, turn and call ids.
- The tools are: submit a stage receipt and request a transition judgment; submit a completion receipt; ask a question; send a coordination request (to LINA Core only, never to another worker); read LINA materials and memory projections. GitHub work and other integrations need no LINA tool; the worker uses its own setup.
- LINA Core declares the tools and handles `item/tool/call`. The harness defines the receipt and question schemas.
- Only the worker thread itself submits receipts.
- A projection read returns only the scope the task allows, carries a revision, and never returns a revoked item. A write happens only on an explicit request and passes the grant check.

### Remote devices

- On a remote device, the registered Node starts and restarts the device's installed worker as its own stdio child. Events and server requests (approvals, questions, dynamic tool calls) reach LINA Core over the `node` connection ([host-protocol.md](host-protocol.md)), and answers return the same way. LINA Core never connects to a remote app-server.
- For coding work, the Node creates the worktree and branch in the device clone, records the protected refs and reports them. To return commits, the Node sends the branch as a git bundle with the head SHA named in the receipt. LINA Core fetches it into the main clone, runs the protected-ref check, merges, and pushes when needed.
- Remote coding work is unsupported until the remote transport is verified and listed in the supported-combination table.

## LINA work harness

The LINA work harness (`lina-work`) is what workers use to do coding work for LINA. LINA builds it in the LINA repository as its own rebuild of the coding-work harness of [CXC (codexclaw)](https://github.com/lidge-jun/codexclaw): the stage loop, its gates and its receipts work as they do in CXC. No CXC code is copied, vendored or translated line by line. The parts of CXC that are not the coding-work harness, such as messenger integrations like Telegram, recall and the GUI, are not brought in. The harness also carries the worker side of LINA's coordination protocol: receiving an assignment, submitting receipts and asking questions.

- The harness is Go, built as static binaries per platform, and ships in LINA releases as the Codex plugin `lina-work@lina`.
- LINA installs the plugin into the user's Codex the first time the user delegates work to Codex, and tells the user once, in that conversation, that it did. After that LINA keeps the plugin at the revision of its own release and updates it before a delegation that finds another revision.
- Installing and updating this plugin is the only change LINA makes to the user's Codex configuration. Settings, other plugins, MCP servers, the model provider, sandbox and approval mode stay as the user set them.
- LINA Core never works through the harness. It dispatches and judges.
- The release manifest records the harness revision, content hash, hook declaration and contract version (receipt and report-word schemas) ([runtime.md](runtime.md)). The adapter reads the installed harness version (see Readiness).

Behavior:

- The loop starts only on an explicit assignment from LINA Core. Everyday wording never starts it.
- A goalplan binds only to a child work thread. The harness has an action that unbinds a goalplan bound to the wrong thread.
- Work without a pull request completes by its artifacts with no source delta and runs in non-git directories.
- Questions go through the LINA question tool. There is no free-pass path that crosses a transition without evidence.
- The subagent evidence gate applies only to subagents called inside the PABCD loop.
- The Stop-block limit counts per turn.
- The harness never edits the worker's settings.
- A background completion notice is never a delivery acknowledgement.
- Input from LINA Core is never treated as a human command.
- Rule formats are identified by the harness contract version and content hash.
- One harness Stop hook handles stage continuation and the completion check, and detects a managed turn that ended without a declared result or receipt.

## Stage judgment and delivery

A worker finishing a stage and LINA Core accepting it are completions on different layers. The harness checks form; LINA Core checks facts.

Stage transitions:

- Before every transition the harness checks form (required fields, file existence) and emits a stage receipt at every PABCD transition and every work-phase completion. It moves to the next stage only after LINA Core's judgment.
- LINA Core checks facts outside the worker: it re-runs the named commands and reads the git head, the diff and the artifacts itself. A receipt file the worker wrote never passes a transition on its own.
- LINA Core answers each transition request with a verdict recorded in the ledger: the transition is accepted, or it is refused with the failed check named.

Delivery:

1. LINA Core registers the assignment before the child's first output.
2. For each execution generation the child submits a completion receipt for the actual output path. It is held until the end of the turn is observed.
3. LINA Core records the receipt in the ledger and acknowledges it.
4. LINA Core judges `verified` or `needs_changes` from the receipt and from artifact and check evidence: the current head, required checks, review thread state, the artifacts, and the protected-ref check.
5. Requested changes go to the same child as a new execution generation. An idle thread receives them with `turn/start`.

Every message LINA Core sends to a work thread, and every result a work thread returns, passes through separate delivery layers: `transport_accepted`, `received`, `agreed`, `applied`, `verified`. Each layer is recorded with its own evidence, and no layer implies the next. Delivery layers are not effect states ([host-protocol.md](host-protocol.md)).

Harness report words are structured receipt fields bound to the harness contract version:

| Report word | Task state | Notes |
| --- | --- | --- |
| `DONE`, `NOOP` | ready for review | `DONE` is never `verified`. |
| `BLOCKED`, `UNSAFE`, `NEEDS_HUMAN` | input needed | The original word is kept and shown. |
| `BUDGET_EXHAUSTED` | stopped or failed | Decided by the budget policy. |

### Messages and questions

- A message has a logical message id, a payload, a recipient, an attempt key and an engine receipt, each recorded separately. A message is claimed before it is delivered.
- Messages to and from work threads follow the resend rules of [host-protocol.md](host-protocol.md): the same logical id and content, and only after a rejection before delivery is proven.
- Delivery, rendering and acknowledgement are recorded separately. A child result that arrives late is recorded as a candidate against its stale revision.
- When no recipient is active or the task is paused, LINA Core keeps the task's inbox and operational notices.
- Questions and results return to the conversation that made the request and to its current reply thread. The user never relays messages. A follow-up instruction for a task reaches only that task.
- Follow-ups, approvals, cancels and handovers are bound to their target task or thread id and never become execution authority for another thread.
- A worker asks through the LINA question tool. When `item/tool/requestUserInput` or `mcpServer/elicitation/request` arrives, LINA Core routes it as a question to the originating conversation.
- A question whose decision owner is the user (`decision`) is answered only by the user. No model thread answers it. Every answer is recorded with the person who gave it.
- A question is shared between tasks only when decision owner, permissions, budget, goal and dependency revision are all identical. A task waiting for a decision never blocks unrelated tasks.

## Effects and unknown outcomes

Effect states and idempotency keys are defined in [host-protocol.md](host-protocol.md), the effect ledger in [runtime.md](runtime.md), and epochs, reconciliation and the task owner's disposition of irreversible effects in [main-authority.md](main-authority.md). Work applies them as follows:

- When LINA Core restarts, running worker turns are cut. Their effects are unknown. After the restart LINA Core reconciles with `thread/resume` and a `thread/read` snapshot.
- On reconnect, LINA Core reconciles with `thread/loaded/list` and `thread/read`. When a pending server request does not arrive again, the attempt is marked unknown and needing attention, and the originating conversation is told.
- When a Node restarts or disconnects, the effects of its running turn are unknown and are reconciled. On a Node policy cancel the Node sends `turn/interrupt` and reports STOPPED or UNKNOWN.
- A GitHub write or merge with an unknown outcome is settled by reading GitHub's state ([integrations.md](integrations.md)). It is never repeated automatically.
- A turn completion, a model's final text, an exit code and a policy echo are never evidence of an effect or of authority.

## GitHub writes and merges

The role rule is in [product-families.md](product-families.md): LINA Core or a person may merge, fix and open pull requests, and no role is exclusive. Pre-merge safety and a result record are mandatory for every merge. The GitHub plugin, the GitHub-side merge preconditions and the recording of merges by people are defined in [integrations.md](integrations.md). This section fixes the parts that belong to work.

- A worker does its GitHub work (push, pull request, review reply) with its own GitHub setup, such as the `gh` login, SSH or a GitHub MCP server in the user's Codex. LINA does not issue, inject or restrict a GitHub credential.
- The GitHub writes a worker makes are reported in its receipt, and LINA Core records them.
- At assignment LINA Core records the parent clone's protected refs in the ledger. When a receipt arrives, and again before a merge, LINA Core compares the receipt's head SHA and the protected refs. On any mismatch it does not merge and tells the originating conversation.
- LINA Core serializes merges into a shared base branch through merge turn slots, whichever agent requested the task. It takes the slot, pins the head, runs the protected-ref check and the preconditions of [integrations.md](integrations.md), merges, and then releases the slot.
- Every merge of a task's result ends as an apply record in the work ledger, whoever made it. The record cites LINA Core's own receipted merge or the observed result of a person's merge ([main-authority.md](main-authority.md)).
- An apply record is not a verdict. The task's delivery is judged from evidence as in Stage judgment and delivery.

## Progress display

Work progress is seen only through work cards. The product has no session view, no "open session" action and no CLI observation command. This section fixes what a work card shows and where each value comes from; how it is drawn is defined in [surfaces.md](surfaces.md).

- Goals, projects and tasks each have a live card that reports whether LINA is working on it. A task's card names its requesting agent.
- A card's state is one of working, paused, ready for review, input needed, stopped, failed, done or unknown. Paused means LINA has stopped delivery to the work thread (see Control). Input needed shows as waiting for the user when the user owns the open question, approval or decision. Done requires a `verified` verdict, plus an apply record when the task's result must be applied. A model's completion text, a harness `DONE` or a completed turn never shows done.
- Each stage has a short summary built from actual results. A stage without results is unknown. The card also carries the execution record and the acceptance-criterion references.
- Counts include only the required children of the current generation. Optional or superseded children, and verifiers of another revision, never count toward overall success.
- The user-visible ladder of a delegated task is: ready, accepted, posted, reported, verified, delivered to the user, confirmed by the user, accepted by LINA Core, merged or released. Each rung is shown only with the evidence that sets it.
- The developer view of a card is hidden by default and turned on in settings. It adds the worker and its version, the thread id, the attempt and the receipts. Stages, counts and times are the same as in the default view.

## Observation, handover and discovery

Observation is not control.

- When a person sends input to a worker thread from outside LINA Core, for example from their own Codex, LINA Core records that turn as `control_handover`, shows "the user is operating directly" on the card, and sends no new input to that thread.
- When the user marks control as returned, LINA Core checks owner, turn and binding again and continues under a new binding epoch.
- `control_handover` applies only to turns with user input from outside LINA Core. Goal auto-continuation, Codex subagent turns and a turn's continuation through the Stop hook are internal worker execution. A turn of unknown origin is `unattributed` and never counts as a handover.
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
- Planning records that CRW keeps in an external tracker (plans, completion criteria, decisions, scope, project and parent bindings, titles and done state) are imported once, from one verified snapshot, by a one-time operator tool. That tool is not part of any LINA release. Every fetched record ends up imported, lost or unsupported and is reported record by record; silence never means imported. Each imported record is mapped to a LINA id exactly once and keeps its source id as provenance only, which no product path resolves. After the import nothing reads the tracker again.
- The owner's existing intent card data is imported once by the same tool and under the same rules, into LINA's intent card model (see Intent cards). Only the data comes in; no code or database structure comes with it.
- The product never depends on CRW's plugin, relay or state paths.

## Deferred

- The task state set of the ledger projection, and the list of adopted CRW schema documents and fixture domains: set by the pin record at the start gate.
- The verified Codex versions and the judgments of the experimental fields LINA uses: set by the worker adapter round trip of the first implementation issue (V5) and recorded in the release manifest.
- How LINA installs the harness plugin in the user's Codex at the first delegation, keeps it updated and tells the person: set by the implementation issue that ships delegation. The harness round trip (V4) installs it with an install command.
- What each worker profile (direct, single and complementary) means, and the values it records: set during work engine implementation acceptance.
- The heartbeat interval H, execution slot limits and budget caps: set by measurement during work engine implementation acceptance.
- Retention periods for closed workspaces: set during work engine implementation acceptance.
- Whether Codex subagents inside a worker see the `lina_work` tools, and that a declined approval leaves the action unexecuted: confirmed by QA in the worker adapter implementation issue.
- Remote-device worker hosting through Node and commit return by git bundle: verified during Node implementation acceptance; unsupported until then.
- The adapters for Claude Code and Antigravity CLI: designed against the adapter boundary when each is added.
- The wire format of delivery layers, work events and progress data: added to [host-protocol.md](host-protocol.md) during work engine implementation acceptance.
