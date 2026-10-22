# Hermes patterns and typical operating setups

This document is an informative note. For each habit that LINA's agents should keep, it records how Hermes Agent and a typical multi-agent operating setup built on it handle the habit, what worked, what hurt, and where the habit lands in LINA under the thirteen design contracts in this directory. The decisions themselves are in [go-core.md](go-core.md) and [tui.md](tui.md), and the contracts rule over both. [go-roadmap.md](go-roadmap.md) orders the work. Where this note and those documents differ, they win. It describes public software and general practice by role. It contains no personal data, and a number in it is a starting value to be measured, never a measurement. Finishing this note does not mean any pattern is implemented or accepted.

## 1. Conventions

- `C-nn` names a decision in go-core.md and `T-nn` a decision in tui.md. Contracts are cited as links.
- "(proposed)" marks a LINA figure that is a starting value, to be set by measurement. A figure attributed to Hermes Agent or another project is a default in its public documentation, never a measurement of LINA.
- Agents are data inside the one LINA. A coding tool is a worker. A sub-agent is a background helper of LINA Core.
- Public sources: Hermes Agent (https://github.com/NousResearch/hermes-agent), Honcho (https://github.com/plastic-labs/honcho), Harness2 (https://github.com/google-research/harness2), the Model Context Protocol (https://modelcontextprotocol.io) and the Agent Client Protocol (https://agentclientprotocol.com).
- "A typical setup" means several Hermes profiles run side by side, one role each: system, scheduling and coordination, development lead, people and wiki, daily life, creative, investment judgement, law and tax.
- Go sketches show seams only, and `ctx` stands for `context.Context`.

## 2. Patterns P1 to P16

Each pattern has the same parts: the habit, how Hermes and typical setups handle it, what worked, what hurt, the LINA form and how it differs. P16 ends with what is open.

### P1. Agent personas and authority

**Habit.** Each agent has a role, a voice and limits on what it may do.

**Hermes and typical setups.** In Hermes Agent a profile is a whole home directory with its own role file, memory, skills and database. A typical setup runs the eight roles above, each with one domain and a named hand-off to its neighbours. Some limits belong to the role: an investment role that only advises, a legal role that only drafts.

**Worked.** A domain split with a named hand-off. Reversible work is done and reported, while credentials, exposure, deletion and spending are asked.

**Hurt.** Authority lives in style text. The hand-off section repeats in every role file, so adding an agent means editing all of them. Role files are long and ride every turn. Hermes lets approvals be switched off, so a limit holds only where a wrapper enforces it.

**LINA form.** The first run creates eight starter agents: system, LINA, dev, knowledge, life, creative, investing and legal, and opens the first conversation with LINA. Dev owns coding work: issues, workers, pull requests and merge decisions. Each agent is persona layers, an emotional state and personal knowledge, kept as data in canon and the version space (C-24, [product-families.md](product-families.md#agents)). Each preset carries its methodology ([conversation-and-memory.md](conversation-and-memory.md#preset-methodologies)). Private persona sources use one directory per preset; the public repository keeps only role descriptions and placeholders. Core holds the routing table, so no persona repeats a hand-off section. Persona text grants nothing. Authority is the main's: grants, effect classes and the approval policy ([main-authority.md](main-authority.md#approval)). A standing grant names agents only when the person says so, and one model policy with two roles serves every agent.

**Differs.** Investing remains advise-only and legal remains draft-only through Core's [preset effect limits](main-authority.md#preset-effect-limits). A prohibited order or binding act is refused before approval, including when relayed through another agent or tool. Empty or edited persona layers cannot remove the restriction. This is effect policy, not a persona instruction or a separate permission system per plugin.

### P2. Agents ask each other

**Habit.** Agents hand work to each other, and a busy recipient queues the request instead of losing it.

**Hermes and typical setups.** Hermes has several mechanisms: an event queue behind an active turn, a bot-chat mailbox with ticket states, an agent-to-agent platform with a ping-pong cap, and a task board with claims. Typical setups wrap the message in an envelope that names the sender and run the recipient as a fresh process, with a file queue for retries.

**Worked.** Queueing instead of failing. A visible sender. A time limit that ends the retry. One report to the person from the agent that finished.

**Hurt.** Sending is a blocking agent turn. Busy is detected by matching strings. There is no idempotency key, so a timed-out call that did run is sent again. A presence lock on the recipient can refuse a delivery that would have been fine.

**LINA form.** An agent exchange is a background conversation of two agents with a durable mailbox. A request is a typed hand-off: subject, what is wanted, what not to touch, done condition, reply deadline, and a decision with a recommendation when one is wanted. Delivery is at least once and deduplicated by delivery id. An acknowledgement ends the exchange without a model turn. The round limit is 4 (proposed). The person sees one log line per exchange, and the `needs the person` flag is set only when the person owns the decision (C-25, [conversation-and-memory.md](conversation-and-memory.md#agent-exchanges)).

**Differs.** An exchange never interrupts the person and never travels through a channel. A request carries none of the person's authority ([main-authority.md](main-authority.md#authorization)).

### P3. One gateway for many bots

**Habit.** One process serves every bot, each with its own channel, and a failure stays in its channel.

**Hermes and typical setups.** Adapters are keyed by profile and platform, with health kept per adapter. The route is resolved once at ingress into a frozen identity, and an unresolved identity is dropped. A claim table refuses a second adapter on one bot credential. Telegram updates are claimed before dispatch, and approval buttons resolve before they render.

**Worked.** Independent adapter health. Fail-closed routing. Buttons that resolve first and render second.

**Hurt.** Isolation is ambient: context variables stand in for the profile and lint rules hold them in place. The profile name sits inside session keys. The Telegram library advances its poll offset before handlers finish. An offline remote machine once delayed every adapter's startup.

**LINA form.** The channel gateway runs inside Core, one adapter per platform behind the contract boundary. A binding names a bot, a chat and an agent, and carries a scope. Pairing is by a one-time code, and one poller or Gateway session runs per token on the main. The cursor is stored with the inbox row in one transaction (C-27, [integrations.md](integrations.md#channels)). READY waits for no bot.

**Differs.** Telegram and Discord are both in, and an agent's bot is its own face. A message from anyone but the paired owner is evidence and never an instruction.

### P4. Idle-only restarts

**Habit.** The shared process restarts only when no agent is mid-work, and a restart is verified before it is reported.

**Hermes and typical setups.** A restart request sets a draining flag and waits for awaitable work. Exit code 75 means a planned restart and 78 a fatal configuration error. A second mechanism proves idleness in two phases, and an unreadable ledger counts as busy. The update flow writes a durable "restart owed" record before its first irreversible step.

**Worked.** The exit-code contract. The tri-state proof. The owed record. Verifying before claiming.

**Hurt.** Busy was counted in separate ledgers for chat turns, scheduled runs, API runs and delegation, and each omission shipped as a bug. In typical setups idle is defined as chat silence, which ignores effects in flight.

**LINA form.** Idle is decided by effects and is tri-state: Idle, Busy or Unknown, and Unknown counts as busy. Every external call takes a lease, so a call without one does not compile. A restart request is a durable record outside canon. `when_idle` is the default, `drain` is explicit, and a timeout never forces a restart (C-05, C-06). This is the Drain step of [runtime.md](runtime.md#updates).

```go
type Registry interface { lease.Leaser; Idle(ctx) IdleReport; Edges() <-chan struct{} }
type Fence interface { Prepare(ctx) (Token, IdleReport); Commit(Token) error; Cancel(Token) }
```

**Differs.** A restart mints an epoch and blocks every Node, so a planned restart must never turn an effect into an unknown outcome. Background runs resume once from their checkpoints and do not block the restart.

### P5. Capped fact memory

**Habit.** A short store of lasting facts rides every turn, while procedures and task progress live elsewhere.

**Hermes and typical setups.** Two capped text files, one for notes and one for a user profile, are edited by one memory tool. The prompt block is a snapshot taken at load. A routing rule sends facts to memory, procedures to skills and progress to history, and asks for declarative entries because imperative notes get re-read as commands.

**Worked.** The routing rule, repeated in the tool schema and the prompts. Atomic batch edits checked against the final state.

**Hurt.** Free text needs heavy substring addressing to be safe for a model to edit. Entries have no ids, so ambiguity is a permanent error class. The snapshot hides writes until a boundary, and long-lived sessions rarely reach one.

**LINA form.** Memory items have ids, revisions, a holder, a subject, evidence and validity, in eight kinds ([conversation-and-memory.md](conversation-and-memory.md#memory-items)). There are no storage caps. Only the rendered standing profile is capped. Write checks run at commit and a failed check refuses with its reason. The routing rule is kept: facts to memory, procedures to skills. Recall enters the ContextPacket each turn (C-20).

**Differs.** There is no frozen snapshot, so a retraction applies at the next preparation. Ids replace substring addressing.

### P6. User modelling

**Habit.** A model of the person is built from real conversation and offered to every agent.

**Hermes and typical setups.** With Honcho, participants are peers: the person and one AI peer per agent. The server derives atomic, dated conclusions with their sources, keeps a short profile card, stores messages for search, and answers questions on demand. Extraction keeps facts from the target peer's own messages.

**Worked.** The extraction rule. Reinforcement instead of growth. Card discipline.

**Hurt.** Raw turns leave the device. Conclusions are kept per observer, so one fact can be stored once for each observer. Text written by machines, such as scheduled deliveries or verification turns, can reach the person's model unless it is excluded. The card is unversioned.

**LINA form.** One store with several views: the standing profile, conclusions with sources, the searchable ledger, and `recall_person` at depth 0 or 1 ([conversation-and-memory.md](conversation-and-memory.md#person-model), C-21). `speaker_kind` is set by the entry path, and a person-subject item rests only on person events. The same fact learned by two agents is one common item with two pieces of evidence. A maintenance pass runs at write and in the background.

**Differs.** Nothing leaves the install. The card is a generated view and cannot drift from a second copy.

### P7. Lossless context

**Habit.** Old context is summarized, raw text stays recoverable, and large outputs live outside the window.

**Hermes and typical setups.** Hermes' own compressor writes one iterative structured summary and prunes old tool output. A lossless-context plugin keeps every message and builds a summary DAG over it, with exact expansion from any node and an atomic, coverage-checked publication.

**Worked.** Losslessness as the organizing rule. Atomic publication. Exact-span citations.

**Hurt.** The host and the plugin each kept a transcript. Compaction blocked the turn and broke the prompt cache. Korean search degraded to a scan. Secrets stayed in the raw store.

**LINA form.** The ledger is the only transcript. Compaction starts at 90 percent of the window and falls back to a continuation turn. A summary tree with proved coverage follows each summary back to its events, and `expand` returns the exact text. A failed build publishes nothing, and a retraction rebuilds the node and its ancestors (C-17, C-18, [conversation-and-memory.md](conversation-and-memory.md#summary-tree)). Korean search uses trigram plus a two-syllable fallback (C-22).

**Differs.** A summary is a handoff for reference and never canon. Imported summaries are read-only history.

### P8. Skills learned while working

**Habit.** A procedure learned in work is saved as a skill and loaded when it applies.

**Hermes and typical setups.** A skill is a folder with a description. A one-line index sits in the prompt and the body loads on demand. A background review and a curator mark stale skills and archive them, and edits are logged with rollback.

**Worked.** Progressive disclosure. Archive instead of delete. Explicit capture beside the automatic loop.

**Hurt.** Review cost scales with context size. Skills grow into runbooks with dated incident notes. Sharing between agents is by hand copy.

**LINA form.** After non-trivial work, a correction or a repeated procedure, the agent writes or patches a skill. A write that changes only instruction text is adopted at once as a new version, with a one-line notice and one-action undo. Executable code, a tool connection or a new permission goes through explicit acceptance (C-23, [conversation-and-memory.md](conversation-and-memory.md#skills), [cognition-and-life.md](cognition-and-life.md#skill-suggestions)). A background job prunes and merges stale skills.

**Differs.** There is no trial gate for the common case, and the catalog is common to every agent.

### P9. Events over polling

**Habit.** A completion event wakes the party that owns the work, because a stall after a missed relay costs more than polling saves.

**Hermes and typical setups.** Background-process completions are drained only while a session is idle. A cheap deterministic probe lets a model run only when the source changed. Event files carry deterministic ids, a cursor moves after hand-off, and a reconcile pass re-emits what was missed.

**Worked.** Deterministic ids. A cursor moved after hand-off. A level-triggered reconcile behind edge-triggered events. Silence as a result.

**Hurt.** Wakes were at-most-once, so a crashed worker was never retried. Ids without a generation collapse legitimate repeats. A scheduled agent has no chat context.

**LINA form.** A completion event is a row keyed by task, attempt and head SHA. Chat input and events share one reaction path. A reconcile sweep runs every 5 minutes (proposed) and at start, with no model call. Notices reach the conversation at turn boundaries (C-58, C-50, C-26, [work-and-delegation.md](work-and-delegation.md#event-driven-flow)).

**Differs.** The event commits with the state change. A model is never the dispatcher.

### P10. Delegated coding as a method

**Habit.** Coding goes to external coding tools, the development lead checks the result, and one review fixes only the top severities.

**Hermes and typical setups.** The practice is a brief first, then one issue, one agent, one worktree and one branch, and the lead never trusts the agent's own report. A heavier relay-and-review harness was tried around workers and set aside for plain delegation, on cost and effort.

**Worked.** Verification done by the coordinator. A merge bound to the head that was read. Plain delegation over a heavy harness.

**Hurt.** A review-once rule that lived in a marker file no script checked. Agents run with approvals off.

**LINA form.** There is no harness. The development role is a method: write the work definition, choose the worker by situation and quota, and operate the pull request. Read GitHub review and CI, do one design-conformance review, send P0 and P1 findings back once, then merge pinned to the head read or close (C-57, C-58, [work-and-delegation.md](work-and-delegation.md#development-method)).

**Differs.** Core runs no verification pipeline and gives a worker no review turn. A worker keeps the sandbox and login that the person set up.

### P11. Confirmation for sending, paying, publishing, deleting

**Habit.** Some acts always wait for the person.

**Hermes and typical setups.** Hermes has pattern-based gates, hard floors under every mode, timeout-denies and grouping by scope. Its own policy names the operating system as the only real boundary.

**Worked.** Consent wording. Timeout denies. Grouping. Buttons that resolve before they render.

**Hurt.** Pattern gates are incomplete by construction. A typed approval in the agent's own channel deadlocked. Approval cards lived in memory and expired on restart.

**LINA form.** Three tiers. The outward tier asks every time. First use asks once with always allow, deny or block. A standing grant is revocable and is suspended when a tool definition changes. A channel offers once and deny. Buttons carry an opaque id, and the presser must be the paired owner. Text never answers. A request pending at restart is stopped (C-12, C-13, [main-authority.md](main-authority.md#plugin-tools)).

**Differs.** There is no approval by command pattern and no yolo mode.

### P12. Secret hygiene

**Habit.** Secrets never appear in output, a command line or a log.

**Hermes and typical setups.** Redaction is on by default and its switch is frozen so generated code cannot disable it. An exact-value scrub follows known key shapes. One environment builder drops provider credentials from child processes.

**Worked.** One egress chokepoint. One environment builder.

**Hurt.** Redaction is a heuristic denylist, and export bundles still leak names and paths.

**LINA form.** A secret is masked before it is persisted, with a flag so nothing downstream can restore the value. Flagged spans are excluded from indexes, summaries, ContextPackets and exports. The secret store has two reveal sites, and rotation is refused inside a turn. Diagnosis events have no string field (C-15, C-33).

**Differs.** Events are clean by construction, not scrubbed afterwards.

### P13. Untrusted text is data

**Habit.** Pages, logs, documents and other agents' output never instruct.

**Hermes and typical setups.** Retrieved content is wrapped in a fixed preamble with its delimiters defanged, and there is no fast path for text that already looks wrapped.

**Worked.** No forgeable fast path.

**Hurt.** Coverage goes by tool name, so output that merely passed through a shell is not framed. The steering marker is an in-band trust channel.

**LINA form.** Speaker kind comes from the entry path. External evidence enters in the user role, marked untrusted. A summarizer receives its input in an enveloped data block. A background run's output is scanned and is external evidence (C-15, C-50).

**Differs.** Trust belongs to the entry path, not to a tool name.

### P14. Short reports that separate verified from guessed

**Habit.** Reports are short, say what was checked, and never mix fact and guess in one sentence.

**Hermes and typical setups.** Role files say that "tried" is not "done" and that unknown is a legal answer. One role keeps a status ladder from proposed to verified.

**Worked.** Honest status words.

**Hurt.** All of it is prose, and nothing checks it.

**LINA form.** The outcome line says done only when the done condition is met, and says what was verified and what was not. Stage summaries carry a source label: worker report, GitHub, Core's review, or unknown. Reply budgets are enforced while a reply streams (C-51, C-30, [conversation-and-memory.md](conversation-and-memory.md#reply-shape)).

**Differs.** The rule is a checked structure, not a tone.

### P15. Work across several machines

**Habit.** Work runs on several devices as one.

**Hermes and typical setups.** The pattern is batch-mode remote commands with a map from tool name to command and a refusal of unmapped names. The rule is that no remote machine gates startup.

**Worked.** Refusal of unmapped names. Startup independence.

**Hurt.** Reachability was the only precondition: no grant, epoch, expiry or reconcile.

**LINA form.** A Node holds grants and receipts and follows the state machine of the main ([main-authority.md](main-authority.md#node-states)). Device identity comes from Tailscale. READY waits for no Node. A seat lease hands one GUI session to one task ([non-competition.md](non-competition.md#seat-lease)). A worker on a device is the Node's child, and its pull request is read from GitHub (C-35).

**Differs.** Grants replace reachability, and placement is data.

### P16. Shared file rules

**Habit.** Originals stay where the person keeps them. There is one system of record per kind of information. Duplicates and sync-conflict copies are reported, never deleted.

**Hermes and typical setups.** The rules sit in a shared file that agents follow as prose.

**LINA form.** Connected sources are read in place with no copy. Asset identity separates id, revision, hash and path. An edit names the revision it read, and a stale write is refused with `stale_revision`. Deletion first takes a recoverable form ([filesystem.md](filesystem.md#connected-sources), [materials-and-knowledge.md](materials-and-knowledge.md#editing-a-persons-files-in-place)).

**Open.** None. The earlier question of one editor at a time is answered by the revision check.

## 3. New patterns N1 to N11

These habits come from LINA's own product direction, not from Hermes. Each gives the habit, the public practice behind it and the LINA form.

- **N1. An agent that never makes the person wait.** Current agent products keep chat responsive with background tasks, sub-agents and notices between turns. LINA: a small turn budget decides only where work runs. Heavy work goes to a worker or a background run, and a partial result is shown only when labelled (C-49, C-50, [runtime.md](runtime.md#turn-budget)).
- **N2. Typed results and a preview panel.** Canvas and artifact products show structured output beside the chat, and protocols use closed unions with a text baseline. LINA: a closed list of result types, data-only bodies, a summary before any long document, and an explicit Save (C-52, [host-protocol.md](host-protocol.md#typed-results)).
- **N3. Short replies enforced at generation time.** A prose rule alone is a weak control. LINA checks a numeric budget while the reply streams, cuts at the last sentence, turns the overflow into a card, and tunes the budget from the person's reactions (C-51).
- **N4. What needs me, and progress as lines.** Task-board and inbox products show running, waiting and done. LINA: one waiting list ranked by blocking, irreversibility and due date, and one progress line per running 일 and 목표 (C-55, [surfaces.md](surfaces.md#waiting-for-me)).
- **N5. Parallel coding hand-off.** Published practice: PR-sized tasks, disjoint write areas, one worktree per agent, completion events. LINA: write areas as leases, event-driven release of waiting tasks, concurrency bounded by quota and host, and a rebase request once (C-57, C-58).
- **N6. A signed catalog and tool-definition hashes.** MCP clients show install cards, and a server can change a tool definition under the user (a "rug pull"). LINA: a signed static catalog, a pinned registration order, and a hash per tool definition that suspends a standing grant when it changes (C-34, [integrations.md](integrations.md#plugins)).
- **N7. Subscription-aware accounts.** Plans differ in what they include and in how they expose quota. LINA: quota rows with unknown as a value, automatic setup that uses the plan first, and routing that moves only new requests (C-47, C-48, [runtime.md](runtime.md#connected-accounts-and-automatic-setup)).
- **N8. Records as files in Git, one record with two views.** PARA separates what ends from what continues, and list and tracker tools differ in their fields. LINA: Markdown records with front matter under PARA folders, written only by Core, with an index that can be rebuilt. Non-developers never see the flow fields (C-53, C-54, [filesystem.md](filesystem.md#records-repository)).
- **N9. Harness improvement as a development-time loop.** Harness2 improves an agent's own prompt, skills and sub-agent definitions from recorded tasks. LINA: it is a developer's tool and not a product feature. The product offers only `lina replay` and episode export, sandboxed and effect-free. Adopted edits ship as ordinary files (C-59, [runtime.md](runtime.md#replay-command)).
- **N10. Autonomy as data.** Standing grants, a source label on every status line, absolute timestamps in the person's zone, and declared versus observed facts kept apart. LINA: grants and policy are records the person can see and edit (C-12, C-13, C-17).
- **N11. Archive first, interpret second.** Importing another memory system without loss: store every source row first, key it by a legacy reference, keep history read-only, and promote with the person in the loop (C-28, [conversation-and-memory.md](conversation-and-memory.md#importing-a-hermes-install)).

## 4. What LINA drops

| Dropped | LINA instead |
| --- | --- |
| Ambient process-global scope held in place by lint rules | Explicit values per lane and one writer (C-16) |
| Several processes writing one database | One writer goroutine, a flock and a halt marker (C-03, C-16) |
| A regex shell gate as the main protection | OS sandbox plus tiers (C-12, C-14) |
| A plugin surface with middleware that can route around the gate | Tool servers behind one registry and one Dispatch (C-34) |
| Prompt-only rules: authority in style text, review-once as a marker file | Tiers, grants and database counters (C-12, C-58) |
| Reflection-driven loop state | Typed phase functions over one state struct (C-08) |
| File queues and timer-driven drains for hand-off | Inbox rows and idle edges (C-25, C-09) |
| Approval by chat text | Id-bound approvals from the person's own client connection (C-13) |
| Free-text logs parsed by regular expression | Closed-schema events with no string field (C-33) |
| Session-rotation compaction | Append-only ledger and a summary tree (C-17, C-18) |
| One hosted memory provider | The in-process person model (C-21) |
| A heavy delegation harness | The development method (C-57) |
| An updater for a live mutable tree | A static binary replaced after a digest check (C-38) |
| Hot membership by polling a directory | Control verbs and typed configuration (C-07) |

## 5. Lessons from earlier LINA attempts and CRW

| Attempt | Lesson | Lands in |
| --- | --- | --- |
| A UI-free runtime kernel with a closed event protocol | Own the loop and keep one session record. Make durability real. Queue when busy. | C-08, C-09 |
| An earlier web chat with context and memory layers | One context authority. Normalize Korean text for search. Stored text is data. | C-17, C-18, C-22 |
| A persona-pack lifecycle | Personas churn, so keep them as data with revisions. | C-24 |
| A messenger bridge | Store the cursor with the inbox row. Pair by a one-time code, not first come. | C-27 |
| A gated growth experiment | Without independent evidence any threshold promoted a mood or an injected suggestion. | C-23, [cognition-and-life.md](cognition-and-life.md#adoption-rule) |
| A file-queue delivery flow | Events are hints and state is truth. Put the generation in the id. | C-58 |
| CRW, a relay and workflow runtime | Keep the mechanisms: receipts over claims, an epoch fence as the first statement, unknown as a value, a halt marker. Refuse the scale and the multi-layer critique loop. Plain delegation won. | C-03, C-04, C-12, C-57 |

## 6. Not settled here

Nothing in this note was measured on LINA. Whether each proposed figure holds is decided by the implementation issue that uses it. Hermes parts not described: its browser stack, voice and platform adapters other than Telegram.
