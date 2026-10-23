# Cognition and LIFE

This contract covers how LINA thinks beyond a single reply, for every agent the person creates. That means the Moirai engine and its three modules (Lachesis, Atropos and Klotho), the Jev fast judge, and Klotho's goal discovery, project planning and project supervision, including the QA product map. It also covers how agents write and suggest skills, and persona growth. Product cognition features are on by default and never block basic conversation; only experimental mechanisms enter use by measurement (see Adoption rule). The contract also defines LIFE and holds LINA's backlog list. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md).

## Scope

This contract covers:

- the adoption rule for cognition features
- the Moirai engine: its modules, the request path, background jobs and the agent or main each one runs for, the one-way flow between modules, and how the modules use each agent's persona
- Jev: what it decides, its handoff rules, and its judgment material
- Klotho: goal discovery, planning, progress and prediction, project supervision, the QA product map and the news feed
- the skills an agent writes itself, and skill and plugin suggestions from repeated patterns
- persona growth: its boundaries and the default way a growth step is adopted
- LIFE: the background world engine and the LIFE feed
- the backlog list: LIFE design, multi-person operation, group conversation, object names, and a remote relay with a login of LINA's own

This contract does not cover:

- agents, presets, what agents share and the speaker rule: [product-families.md](product-families.md#agents)
- memory canon, personal and common knowledge, recall, corrections and deletions, persona layers and request assembly, emotion, and the skill system: [conversation-and-memory.md](conversation-and-memory.md)
- the work and knowledge records (목표 (goal), 일 (project), 할 일 (to-do), 계속 챙기는 것 (area), 메모 (memo)) and the to-do record, intent card data and the work engine: [work-and-delegation.md](work-and-delegation.md)
- materials and their derived data: [materials-and-knowledge.md](materials-and-knowledge.md)
- intent card, plan, feed and LIFE screens: [surfaces.md](surfaces.md)
- non-chat adapters and the packaging of background services: [runtime.md](runtime.md)
- plugins and the GitHub plugin: [integrations.md](integrations.md)
- background load on a shared machine: [non-competition.md](non-competition.md)

## Adoption rule

The base product is basic conversation. It is LINA Core with Atropos' minimum turn preparation and Lachesis' minimum memory, and it keeps working whatever else is missing.

Product cognition features are on by default: Atropos' context selection, Lachesis' memory and recall (including the person model with its maintenance pass, and the person brief), Jev, each Klotho function and skill suggestions. Each one:

- never blocks basic conversation. If it is missing or failing, conversation continues and only that feature is unsupported.
- can be turned off on its own.
- runs its evaluation as a regression check where quality matters (recall, routing judgments, persona-likeness for each preset), and a drop is reported.

Persona-likeness is measured against each preset's evaluation set. An agent whose persona has no evaluation set, such as one created from a description, is reported as unsupported on that check, and its conversation continues.

Experimental mechanisms are different. A learned mechanism that replaces or augments a working baseline, such as a circuit model derived from a connectome, a world model, reinforcement learning or a growth mechanism, enters use only when a measurement shows that it does better than the baseline it would replace. Swapping one component for another that does the same job, such as one judge for another, is decided the same way.

- A measurement runs the same inputs with the mechanism and without it.
- Before it runs, the measurement names its metric and its evaluation set.
- A mechanism's own judgment never decides whether that mechanism is better.
- The measurement and its result are recorded with the mechanism.
- A mechanism that isn't measured better stays off.

## Moirai engine

Moirai is the cognition engine inside LINA Core. It handles everything an agent knows and works from: memory, materials, persona, context and plans. It is part of LINA Core, not a plugin. One Moirai engine serves every agent. Its three modules are split by time:

| Module | Time | Does | Runs in |
| --- | --- | --- | --- |
| Lachesis | Past: what has built up | The memory engine (facts and preferences, recall, corrections and deletions), the person model and its maintenance pass, the summary trees, the import of a Hermes install and the promotion of imported history, the person brief, the material index and derived materials, growth candidates, prepared judgment material | Background |
| Atropos | Present: what to do now | Context assembly and judgment for each turn: the intent of this request, a time-capped recall query on the current input, which memories and materials to include, compression and budget, tracking commitments, task state and the state of background runs. Jev lives here. | Request path |
| Klotho | Future: what will come | Planning, next-action candidates, predictions with verification candidates, replanning, goal discovery, project supervision, skill suggestions, the news feed | Background |

### Canon

- The three modules read and write the same canon inside LINA Core, each for a different purpose. Writer rules for memory and persona are in [conversation-and-memory.md](conversation-and-memory.md). Writer rules for materials are in [materials-and-knowledge.md](materials-and-knowledge.md).
- The canon has a part for the whole main and a part for each agent. The main's part holds common knowledge, materials, the work and knowledge records, intent cards and the prediction ledger. Each agent's part holds its personal knowledge, its persona revisions, its emotional state, its conversations, and its judgment and growth records.
- Each module keeps its own derived state, such as indexes, prepared material and candidate lists. That state is internal to the module and can be rebuilt. It is never canon.
- The work ledger belongs to the main and is outside the modules' canon. Its writer rule is in [work-and-delegation.md](work-and-delegation.md).

### Request path and background

- Atropos is the only module in the request path, which runs from the moment LINA Core receives the person's input until it sends the model request. Atropos must be light and fast. It works for the agent in the conversation.
- For each turn, Atropos assembles the ContextPacket from that agent's persona, its personal knowledge and common knowledge. Its time cap, its minimum packet and the way it enters the request are defined in [conversation-and-memory.md](conversation-and-memory.md), together with the turn latency points.
- Lachesis and Klotho run in `moirai-worker`, a background service that runs whether or not a conversation is active. One `moirai-worker` serves every agent. The service is optional. Without it, only background memory and planning are unsupported. Its packaging is in [runtime.md](runtime.md).
- Inside `moirai-worker`, each function of Lachesis and Klotho runs as its own background job. Each job has its own schedule, budget, on/off switch and adoption record. Budgets count against one total for the main. A job that fails, runs long or exceeds its budget stops only itself and never delays another job or the request path.
- A job that reads one agent's personal records (its conversation ledger and its personal knowledge), such as Lachesis' memory extraction from that agent's conversations, runs as that agent's job. Each item it writes becomes that agent's personal knowledge or common knowledge under the layer rule in [conversation-and-memory.md](conversation-and-memory.md#knowledge-layers).
- A job for the whole main, such as Klotho's goal discovery, project supervision, predictions and the news feed, reads only common knowledge, plans and the work ledger. It runs once for the main, and its output is common. The agent in a conversation speaks the suggestions that come from it.
- Background runs of LINA's sub-agents are not Moirai jobs. A conversation starts them on demand, the work engine runs them inside LINA Core, and they do not run in `moirai-worker` ([work-and-delegation.md](work-and-delegation.md#background-runs)).
- Data flows one way. Lachesis and Klotho publish results, and Atropos reads them. Atropos never waits on a background module during a request. Its own query on the current input reads the memory index within its time cap ([conversation-and-memory.md](conversation-and-memory.md)).

### Memory jobs

Lachesis' memory work runs as these jobs in `moirai-worker`, each with its own schedule, budget, switch and adoption record as above. None of them runs in a turn.

- **Extraction.** Reads one agent's conversations and writes items for that agent under the layer rule of [conversation-and-memory.md](conversation-and-memory.md#knowledge-layers). It creates an item whose subject is the person only from events whose speaker kind is `person`, and reads consecutive messages of the person within a short window as one utterance.
- **Messenger extraction.** A job for the whole main. It reads the person's own messenger records that arrived after each source's learning watermark, in bounded batches, grouping a burst in one chat as one utterance, and writes items under the layer rule with no source agent ([conversation-and-memory.md](conversation-and-memory.md#messenger-records)). Other people's messages enter its request as bounded, untrusted context. They are evidence only for items about their own sender, through a usable identity link, once the identity gate passes ([conversation-and-memory.md](conversation-and-memory.md#sender-identity)). History before the watermark is read only for an import the person started with learning on. No new eligible records means no model call.
- **Person brief and summary preparation.** The brief, and the compaction summaries and summary-tree nodes that compaction will use, are prepared as [conversation-and-memory.md](conversation-and-memory.md) defines.
- **Maintenance pass.** One pass over the items replaces separate ad hoc review jobs. It runs once for the main over common items and once for each agent over that agent's personal items. It finds items past their `valid until` or superseded, duplicate clusters inside one subject, items whose only evidence is automation, and instruction-like text in the standing profile. It applies only what the person has pre-authorized, as a recorded choice the person can see and edit, and turns everything else into proposals that expire unanswered. It reports in a line or two and stays silent when nothing changed.
- **Import and promotion.** The import of a Hermes install is a job the person starts. It is the one job that writes the ledgers of agents; it reads no personal record of an agent except to check a legacy reference, and it makes no model call. Promotion proposals are the job of each mapped agent: they read that agent's imported conversations, and the person confirms them in grouped batches. Promoted items take their layer by the usual rule.

The first Go memory-job implementation keeps preparation and transactional write
rules in `internal/core/memory`, using the existing worker's Aux/Apply/Report
path. Extraction is bounded to one agent's canonical person events, groups a
short burst without counting replay copies, and commits its items with its task
watermark. Empty eligible input makes no extraction model call. Prepared tasks
are checked again before model dispatch and before publication. An obsolete
extraction task is consumed without a model call or item writes; its replacement
is rebuilt from the prior source cursor against current canon so still-eligible
events are not skipped.

Installed Core owns the extraction schedule and registers each live agent on
the existing worker socket. Its source cursor is distinct from the worker task
sequence: accepted memory and source progress commit together; an ineligible
prefix advances without an auxiliary task. Failed or interrupted calls keep
their original reservation and charge, and a later scan prepares a fresh task
from the unchanged source cursor. The worker remains optional; without it,
queued extraction cannot produce memory.

The maintenance choice is a durable, revision-checked record for the person and
holder. Its default allows no action. The pass may clear standing flags, merge
exact duplicates or mark exclusively automated evidence only when that action
is allowed by the current choice. Other findings become expiring proposals;
unchanged findings do not renew them or repeat a notice. Exact merges retain
all evidence and prior revisions, and never combine different subjects or
scopes. Similar wording is a proposal, since overlap alone cannot distinguish a
reinforcement from a contradiction. Expired and superseded statements remain
available as history.

Briefs retain per-sentence citations and a generation plus revocation epoch.
Only the latest task can publish, and changed evidence prevents both dispatch
and publication. An obsolete brief task is consumed without publishing or making
a model call, so it cannot block the newer task behind its watermark. Reads take a ready result for the same agent, person and topic,
with bounded turn age, and never wait on background work. Empty results back off
on the configured cadence. The concrete callback and schema composition,
initial thresholds and executable acceptance scenario are specified in
[go-core.md C-21](go-core.md#records-and-memory).

### Persona in the modules

Each agent's persona is a layer that all three modules use:

- Atropos puts the persona snapshot of the agent in the conversation into the context of every reply.
- Lachesis turns that agent's accumulated records into growth candidates for that agent.
- Klotho reads the person's preferences and boundaries held in common knowledge, and the rules of the work area, as constraints when it plans or predicts.

Persona storage, revisions and injection are defined in [conversation-and-memory.md](conversation-and-memory.md).

## Jev

Jev is an optional fast judge that only Atropos uses. Atropos uses it only for closed choices that rules can't settle:

- the intent of this utterance and the response mode
- which item the person's input is about, when the rules of [What a message is about](conversation-and-memory.md#what-a-message-is-about) leave it open
- the ranking of context candidates
- where a request goes when the routing rules of [work-and-delegation.md](work-and-delegation.md) leave it open: an answer in the conversation, a task for a worker, a background run for research or analysis, work for a device's Node, or a question to the person
- which installed worker takes a task, when the choice rules of [work-and-delegation.md](work-and-delegation.md#choosing-the-worker) leave it open, among the workers whose adapters LINA supports and that have quota and capacity left

Jev never chooses the chat model or its effort. A model change discards the prompt cache of the conversation, so the model stays the person's setting ([runtime.md](runtime.md)). When no judge is available (Jev is off or has no key, and no local judge is running), rules plus the main model provide the same function.

Atropos asks all of a turn's open closed choices in one judge call, so a turn pays the judge's latency once. When a judge takes one question per call, the judge adapter splits the call and the turn's latency budget covers all the parts. Each choice that gets no answer within the budget follows the handoff rules on its own; the choices that were answered stand.

Atropos reaches Jev through the judge adapter. Every judge answers in one typed shape: a closed question with its candidates goes in, and a choice with a probability for each candidate comes out. The adapter speaks to a hosted judge API, Jev by default, or to a local judge model that runs as a separate pinned process on the person's device, like the other external components ([runtime.md](runtime.md)). A local judge is trained on the person's own labeled judgments and keeps the judged text on the device. Atropos uses one judge at a time. A judge, a threshold or a change to the judgment material enters use only when it does better than the current one on the evaluation set below.

Handoff rules:

- Code first fixes the candidates, the permissions and the canonical revision. The candidates always include "not applicable" and "hold".
- These conditions send the choice to the main model: an answer outside the candidates, low confidence, a stale revision, or an error. If the main model can't decide either, the agent asks the person one question.
- A Jev result is never permission to act.

Judgment material:

- A Jev call is the job of the agent in the conversation. Its material comes only from what that agent knows: its personal knowledge and common knowledge.
- In the background, Lachesis prepares, for each agent, past judgments with provenance, their actual outcomes, and the person's preferences and refusals.
- At request time, Atropos checks this utterance's candidates, state, permissions and revocations, and does the final assembly.
- Judgment records and outcome records are canon of the agent whose conversation made the judgment. Prepared material is derived data that can be rebuilt, and it follows the revocation epoch rules ([conversation-and-memory.md](conversation-and-memory.md)). An outcome nobody knows is recorded as unknown.
- Jev records link to Klotho's prediction ledger by id only.
- When the person corrects a routing choice, or a request or task is moved to another destination, the corrected destination is recorded as that judgment's label.
- Lachesis puts confusable pairs side by side in the material: a judgment that went wrong next to the closest judgment that went right, so the difference that decides the destination is visible. The same pairs are training data for a local judge.
- An evaluation set of labeled judgments is held out. It never enters judgment material or training, and every judge, threshold and material change is measured on it for accuracy and latency.
- Each judge call records its latency. A call that runs past the latency budget counts as no answer and follows the handoff rules.

The Core input-binding path accepts an optional non-chat judge adapter. Atropos
supplies revision-bound candidate identities plus new, none, not applicable and
hold; it accepts only a complete probability distribution with a unique winner
above its threshold. It records latency, validates the revision again before
saving a choice, and hands stale, missing, invalid or uncertain answers to the
main model. A generic new choice still needs the main model to choose the kind,
parent and duplicate explanation. No configured adapter means no judge call.

Jev is called through its own non-chat adapter, separate from the model path ([runtime.md](runtime.md)).

## Klotho: project planning

Goal discovery, planning, prediction and project supervision form one project planning function:

1. goal discovery
2. planning, where the person confirms each plan
3. progress tracking: drift, completion time and next-action candidates
4. the prediction ledger
5. risk and replanning
6. design and quality upkeep: tidying intent cards, the QA product map, and research

In this contract "project" means an 일 and the 목표 above it. Every step uses the built-in work and knowledge records and the intent cards ([work-and-delegation.md](work-and-delegation.md)). No step has a store of its own. Each step, like supervision, the news feed and skill suggestions, is a separate background job with its own schedule and budget. Every step is a job for the whole main (see Request path and background).

Klotho never executes. It sends requests for code work, other work, research and the creation of a skill or plugin that widens effects (see Skill suggestions) to the work engine, and the work engine decides whether and how they run. The exceptions are project supervision, which updates its own outputs directly (the tidy-up of intent cards and the QA product map), and the pruning and merging of stale skills, which are versioned skill writes that one action undoes (see Skill suggestions). Even then, Klotho never touches the person's intent text ([work-and-delegation.md](work-and-delegation.md)).

### Goal discovery

- In the background, Klotho reads common knowledge and the records. It turns goals and concerns that keep coming up into 목표 drafts, each stated as one sentence of the outcome in the person's words, with a done condition when one can be stated.
- Discovery stops at the draft. A 목표 draft has no 일 or 할 일 list.
- The agent in a conversation with the person presents the draft. When the person accepts a 목표 draft, it becomes a 목표. A 목표 isn't a plan yet.
- Klotho also produces the drafts that the agent in the conversation proposes when a 메모 or a 할 일 grows into something LINA should track: a 할 일, an 일 or a 목표. The promotion rules are in [work-and-delegation.md](work-and-delegation.md).

### Planning

- Decomposing a 목표 into 일, and an 일 into stages and 할 일, has two entry points: an accepted 목표, and a large piece of work that the person hands over directly as an 일.
- A plan Klotho drafts is shown to the person as a `plan_view` result and becomes a plan only at the plan confirmation point defined in [work-and-delegation.md](work-and-delegation.md).
- Splitting a confirmed item of code work into hand-off tasks, and ordering the tasks, is the work engine's method ([work-and-delegation.md](work-and-delegation.md#splitting-and-parallel-hand-off)). It is not a second plan confirmation.
- Where a card's criteria and the observed state differ, Klotho may draft 할 일 to close the gap. Those drafts go through the same confirmation point.

The planning implementation queues an immutable snapshot of an accepted goal or
project through Core's `draft_plan` tool. `plan_index` discovers accepted roots
and preview revisions; `read_plan` explicitly reads a complete preview. Moirai
requests the auxiliary, tool-free Klotho model call through Core and submits its
validated proposal through the job API. Core accepts only that call's retained
output and commits the complete `plan_view` revision with the job watermark.
This work produces no project or to-do records and starts no execution. A queued
replacement that becomes obsolete because its preview changed or was confirmed
ends with a durable rejection in the same transaction as the job watermark. It
leaves the preview and records unchanged and releases the next queued request.
The originating conversation receives one retained outcome line, including when
startup recovers a rejection committed before publication.

A completed model response that is not a valid plan (including invalid JSON,
cyclic dependencies or a dependency targeting a project instead of a to-do)
also ends with a retained rejection before any preview is published. Dependency
targets must be to-dos in the complete draft; they may appear later in the entry
order or belong to another project under the accepted goal. Core caches that
outcome and settles the completed call's usage; Moirai submits it through the
same authenticated proposal path. The rejection and watermark commit together,
and the conversation receives one retained outcome line. Restart and duplicate
application do not repeat the model call or the notice. Later queued drafts can
proceed. A provider call whose completion is unknown retains its pending
reservation and is never automatically repeated.

The client `planning` capability provides the person-only acceptance and
confirmation actions. Acceptance saves the goal or directly handed-over project
and its done condition. Confirmation names the current preview id and revision;
Core also checks that the accepted root has not changed. It creates the entire
set of project/to-do records in one saved version, with their done conditions,
parent ids, order, stages and dependencies. A replacement discards every previous draft
entry, and a stale, foreign or already-confirmed replacement is refused. After
confirmation the same card remains readable with no confirm action. Repeated
confirmation returns the original receipt, including after restart. An uncertain
publication is reconciled by reading its exact records, never by dispatching the
write again. Work-engine hand-off records require no second plan confirmation.


### Progress, prediction and replanning

- Klotho tracks progress, drift between the plan and the actual state, and expected completion time. It also proposes next-action candidates.
- The expected outcomes of next actions and the completion-time predictions of supervision go into one prediction ledger, kept in the main's canon. Risk and replanning both read that ledger.
- Klotho proposes a verification candidate for each prediction. A prediction whose outcome isn't observed stays unknown.

### Project supervision

Klotho periodically reviews repositories, plans and cards. Supervision covers:

- scanning repositories and tracking drift between the state of a code 할 일 and the actual state of its linked pull request and issue, through the GitHub plugin ([integrations.md](integrations.md))
- predicting progress and completion time
- research that helps design or learning, requested from the work engine
- refreshing the QA product map
- tidying intent cards
- judging a repository's issues and pull requests through the GitHub plugin and reporting that judgment ([integrations.md](integrations.md))

Every supervision output is a proposal or a report. None of them is approval to execute.

A proposal or report reaches the person as a row in "Waiting for me" when the person must decide, and otherwise as one outcome line. The issues and pull requests it judged stay in Details ([surfaces.md](surfaces.md#default-view)).

### QA product map

The QA product map is a project supervision function.

- For each feature of a supervised product, the map records the path to reach the feature, how to operate it, and what counts as observed success.
- Klotho keeps the map as a feature map and a UI tree, and it refreshes both periodically.
- Klotho compares the map with the code and with the running app. Running the app is work that the work engine performs, and Klotho updates the map from the results.
- Klotho reports QA gaps: features with no QA path, and paths that don't match the code or the app.
- The map lives with the built-in plan data and the intent cards. It has no store of its own.

### News feed

- In the background, Klotho collects and selects news that matches the person's interests held in common knowledge, through the plugins that are on, such as a web search plugin ([integrations.md](integrations.md)). The LINA app only shows the result ([surfaces.md](surfaces.md)).
- The news feed and skill suggestions are separate outputs.
- Interest discovery and ideas are kept separate from the LIFE feed.

## Skill suggestions

- Klotho collects requests, procedures and workflows that the person repeats often. It builds nothing itself. Repetition in one agent's conversations is collected by that agent's job, and repetition in the work ledger by a job for the main (see Request path and background).
- After a non-trivial task, a correction, or a repeated procedure (one the agent noticed, or one that a Klotho candidate shows), the agent in the conversation writes a new skill or patches an existing one: the procedure, its pitfalls, and the person's preferences for that kind of work. It patches the skill it used instead of adding a near-duplicate.
- A write that changes only a skill's procedure or instruction text, and adds no tool, connection or permission, is reversible, so it is adopted at once with no approval step. LINA Core writes it as a new version of the skill in the version space ([filesystem.md](filesystem.md#version-space)), the person is told in one line afterwards what was learned, and one action undoes it by restoring the previous version. A write the person undid isn't made again. These writes belong to skill suggestions: when that feature is off, no agent writes a skill on its own.
- Durable facts about the person go to memory canon, and procedures go to skills, never the reverse. A correction is applied to every copy of what it corrects, including the text of a skill ([conversation-and-memory.md](conversation-and-memory.md#correction-retraction-and-deletion)).
- Skills are loaded by index: the catalog index holds each skill's name and a one-line trigger, and a skill's body is loaded only when a turn needs it ([conversation-and-memory.md](conversation-and-memory.md#request-layout)). In the background, Klotho prunes stale skills and merges near-duplicates among the skills that the person or an agent created, as a job for the main. A prune or a merge is a skill write like any other: versioned, with the same one-line notice and one-action undo. Built-in skills ship with a release and are not touched.
- The maintenance job judges retirement or replacement from the skill contents, not from age or non-use alone. It retains a destination's useful instructions when merging near-duplicates and archives the sources. One undo restores the entire merge as new revisions; later edits to any member prevent a stale undo. The main remembers undone operations across restart, including a repeated merge with a different destination or wording. Its durable switch also gates automatic conversation skill writes, while undo remains available. A failed or interrupted reserved pass is not replayed; its budget charge remains and later changed snapshots can run after the normal interval. Recovered notices stay in their original conversation and history position. A missing or trashed recipient does not block notices for surviving agents; restoring the recipient allows delivery to its original conversation. The installed job, initial schedule and budget, validation and notice tools are specified in [go-core.md C-23](go-core.md#records-and-memory).
- Approval stays only where effects widen. A skill or plugin that adds executable code, a tool connection or a new permission surface is never written or adopted by the bullets above.
- Once enough evidence builds up, the agent in the conversation suggests such a skill or plugin in one short message that follows the reply shape ([conversation-and-memory.md](conversation-and-memory.md#reply-shape)). The evidence is in the suggestion's Details: when the repetition happened, how many times, and in what form.
- No agent presses a suggestion the person declined or put off.
- Repetition is only a candidate signal. A repeated request or tool sequence proves neither a procedure nor consent to automate it.
- A candidate records the distinct sessions and dates, the shared purpose, inputs and outputs, the ids of the evidence events, and any counterexamples.
- LINA builds a skill or plugin that adds executable code, a tool connection or a new permission surface only once the person understands what it does, when it is used, what permissions it has, and how to undo it. The understanding check runs in this order: show a summary (a `summary` result), take questions and adjust the scope, run a reversible trial where possible, then get explicit acceptance. A button click alone doesn't count as understanding. This check covers skills and plugins that LINA proposes and builds; adding an existing plugin follows [integrations.md](integrations.md).
- A skill holds a procedure, instructions and completion conditions. A plugin is needed when the result requires new executable code, a tool connection or a new permission surface. A plugin that widens permissions gets a separate review.
- Creating a skill or plugin that widens effects is work for the work engine. Every new skill, whoever writes it, follows the meta-skill in [conversation-and-memory.md](conversation-and-memory.md). A skill or plugin, however it came to be, is available to every agent.

## Growth

This contract fixes the boundaries of persona growth and the default way a growth step is adopted:

- Growth is per agent. Each agent grows from its own records, and the persona it starts from is its baseline: its preset's layers for an agent created from a preset, and the layers written from the person's description for an agent created from a description.
- Growth adoption is the only way an agent's disposition changes. It is also the only way a growth candidate becomes part of that agent's persona.
- Lachesis produces growth candidates from that agent's records. A candidate is not growth.
- Growth writes only the growth snapshot layer, the layer that belongs to the agent ([conversation-and-memory.md](conversation-and-memory.md#layers-and-revisions)). The other layers change as that contract defines.
- A growth step is a reversible text write, so by default it is adopted at once with no approval step. It is written as a new revision of that agent's persona through the Moirai engine's write path, and the person is told in one line afterwards what was learned. One action undoes it: undo writes a new revision that restores the previous text, so the history stays append-only, and a step the person undid isn't proposed again. Which revisions count as a growth effect is defined in [conversation-and-memory.md](conversation-and-memory.md).
- Growth mechanisms follow the adoption rule.

The growth write accepts a candidate id, the expected persona revision, the complete next growth snapshot and a one-line learning notice. Core binds the agent and person outside the candidate; neither another persona layer nor an agent selector is accepted in the worker's proposal. A deterministic Moirai job adopts the precomputed candidate without a model call or approval. Its revision, learning notice, job receipt and watermark commit together. A failed write publishes no notice. Replaying the same candidate publishes no second revision or notice. Preparation already acknowledged for a turn keeps its revision; the next turn uses the adopted snapshot.

Each notice carries an undo handle identifying its growth revision. One undo action appends a revision containing the previous growth text while retaining the current core, default voice, user voice and agent state. Later growth steps must be undone first, even if a later step has the same text; unrelated persona edits do not prevent undo. Repeating an undo does not mint another revision and returns the current agent revision, including after later persona edits; the original undo revision remains in the historical record. The store remembers undone candidate ids and SHA-256 snapshot hashes per agent, including across restart. This suppression applies to new growth candidates; an explicit undo always restores its saved prior snapshot, including the baseline after undoing a step that cleared it. A job receiving an undone candidate consumes it silently, with no new notice or persona write. Agent expiry removes these records with the agent's other persona records.

Installed Core owns one private worker socket and registers deterministic growth jobs bound to installed agents. It advertises growth even when no agent remains; a replacement created through management can adopt, read notices and undo in that same boot. Client operations and worker requests resolve the current installed agent through the conversation router, retaining separate jobs, tools and notices per agent. Its client `growth` capability uses the existing `agent_request`/`agent_result` kinds for precomputed candidate adoption, committed notices and single-action undo. Candidate and undo mutations require the bound owner and an eligible decider; the client cannot select an unknown/deleted agent or another person. Adoption queues the candidate and commits its revision/receipt together through the worker's Apply path, so a rejected candidate leaves no poisoned queue row. Notice and undo operations use the agent-bound `growth_notices` and `undo_growth` tools in Core's authorized registry.

The snapshot limit remains 262144 Unicode characters, subject to the existing 1 MiB client frame limit. Growth job input and output allow up to 6 MiB of encoded JSON to accommodate metadata and escaping after client decoding; ordinary jobs retain their 256 KiB cap. The private worker request envelope is bounded at 8 MiB. Direct ingress, queued input and worker proposals enforce the same per-job payload bound.

After commit, Core reports adoption success even if notice delivery fails; a notice read, subsequent adoption, worker receipt replay or restart can retry the durable outbox. Core projects the one-line learning notice into the conversation with a stable ledger and stream id. Restart fills a gap between adoption and notice delivery without duplicating the line. Growth applies to future turn preparations only, including when adopted while a reply is still running. The `e2e/m2/persona-growth.json` scenario has both real-worker service coverage and an installed-Core path covering active/next-turn model instructions, notice delivery, undo, restart and suppression. The `e2e/m2/persona-growth-followups.json` scenario additionally checks near-limit ASCII, Unicode and escaped snapshots, empty-agent startup, replacement growth controls and repeated undo after later adoption. Candidate generation belongs to Lachesis; acceptance uses fixture text.

## LIFE

LIFE is two things: a background world engine, and a feed inside the LINA app. It is not a separate product. LIFE is unrelated to the life preset, which manages the person's own life ([conversation-and-memory.md](conversation-and-memory.md#preset-methodologies)).

- The world engine keeps a world, its time, its events and the agents' daily activities going in the background. It belongs to the LINA Core family, runs on the main and is Go ([runtime.md](runtime.md)). Whether each agent has a world of its own or the agents live in one world together is set by the LIFE design item in the Backlog.
- The LIFE feed shows an agent's activities, posts, reactions, media and conversation experiences, much like a social photo feed. It isn't connected to Instagram or any other real social network. Conversation experiences in the world are world events. They are not agent exchanges and never appear in the agent talk log ([conversation-and-memory.md](conversation-and-memory.md#agent-exchanges)).
- LIFE has no separate app, installer or login. LIFE has no persona of its own; the agent in LIFE is the agent whose life it shows.
- LINA APP is a thin client. It shows the LIFE feed and never runs the world engine ([surfaces.md](surfaces.md)).
- The world engine never writes an agent's persona, conversations or memory canon. LIFE events reach an agent's memory only through LINA Core ([product-families.md](product-families.md)).
- The LIFE feed is its own stream. It doesn't mix with the news feed or the today feed ([surfaces.md](surfaces.md)).
- World activity is background load, and non-competition measurements include it ([non-competition.md](non-competition.md)).
- LIFE is the lowest-priority area. Beyond this definition and these boundaries, the design of LIFE is a backlog item.

## Backlog

The following items are out of scope until each one has a design of its own.

| Item | Covers |
| --- | --- |
| LIFE design | The world model, whether agents each have a world or live in one world together, the agents' activities in it, feed content, the connection to conversation and memory, and the packaging of the world engine |
| Multi-person operation | The person axis: admitting people other than the owner ([product-families.md](product-families.md)), whether each person has agents of their own, more than one world, and relationships between people, including what people share and who may decide for whom, and how people other than the owner receive LINA's releases ([releases.md](../policy/releases.md)). Agents are a separate axis, and this item leaves them as they are. |
| Group conversation | Several agents talking with the person in one conversation. In a conversation today, one agent speaks. Each conversation ledger event names the agent that spoke, and each conversation lists the agents taking part in it ([conversation-and-memory.md](conversation-and-memory.md#conversation-ledger)); group conversation is built on this model. An agent exchange has no person, and an external channel binds one agent to a chat, so neither is a group conversation. |
| Object names | Product names for 목표, 일, 할 일, 계속 챙기는 것, 자료 (resource), 보관 (archive), 메모 and the materials manager. The Korean words in the contracts are the default; a card sort with non-developers confirms them. Until then, documents use these words and describe objects by role and permission. |
| Remote relay and own login | Reaching LINA Core from outside the tailnet through a relay, and a login of LINA's own. Remote access through Tailscale is defined in [runtime.md](runtime.md). The owner's ChatGPT sign-in ([runtime.md](runtime.md#lina-login)) identifies the owner to LINA Core once and is not this login. |

## Deferred

- The size of the growth snapshot layer, and the evidence a growth candidate needs before it is adopted: set during implementation acceptance of growth.
- Jev's confidence threshold, its latency budget, the size of its judgment material, how often that material is refreshed, and the size of the held-out evaluation set: measured during implementation acceptance of Jev.
- Which other hosted judges the adapter supports, such as a model provider's decision API: decided by measurement against Jev on the evaluation set.
- Whether a local judge is offered, which model it starts from and when it is retrained: decided when enough labeled judgments exist to measure it against Jev on the evaluation set.
- Thresholds and frequency of skill suggestions: measured during implementation acceptance of skill suggestions.
- The metric and evaluation set for each regression check and each experimental mechanism, including each preset's persona-likeness set: set by its implementation before it runs.
- The periods for repository scans, card tidy-up and QA product map refresh: set during implementation acceptance of project supervision.
- The schedule and budget of each memory job: set during implementation acceptance of memory.
