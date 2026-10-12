# Cognition and LIFE

This contract covers how LINA thinks beyond a single reply, for every agent the person creates. That means the Moirai engine and its three modules (Lachesis, Atropos and Klotho), the Jev fast judge, and Klotho's goal discovery, project planning and project supervision, including the QA product map. It also covers skill suggestions and the boundaries of persona growth. Product cognition features are on by default and never block basic conversation; only experimental mechanisms enter use by measurement (see Adoption rule). The contract also defines LIFE and holds LINA's backlog list. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md).

## Scope

This contract covers:

- the adoption rule for cognition features
- the Moirai engine: its modules, the request path, background jobs and the agent or main each one runs for, the one-way flow between modules, and how the modules use each agent's persona
- Jev: what it decides, its handoff rules, and its judgment material
- Klotho: goal discovery, planning, progress and prediction, project supervision, the QA product map and the news feed
- skill and plugin suggestions from repeated patterns
- the boundaries of persona growth
- LIFE: the background world engine and the LIFE feed
- the backlog list: LIFE design, multi-person operation, group conversation, object names, and a remote relay with a login of LINA's own

This contract does not cover:

- agents, presets, what agents share and the speaker rule: [product-families.md](product-families.md#agents)
- memory canon, personal and common knowledge, recall, corrections and deletions, persona layers and request assembly, emotion, and the skill system: [conversation-and-memory.md](conversation-and-memory.md)
- the plan data model (goals, projects, tasks, milestones), intent card data and the work engine: [work-and-delegation.md](work-and-delegation.md)
- materials and their derived data: [materials-and-knowledge.md](materials-and-knowledge.md)
- intent card, plan, feed and LIFE screens: [surfaces.md](surfaces.md)
- non-chat adapters and the packaging of background services: [runtime.md](runtime.md)
- plugins and the GitHub plugin: [integrations.md](integrations.md)
- background load on a shared machine: [non-competition.md](non-competition.md)

## Adoption rule

The base product is basic conversation. It is LINA Core with Atropos' minimum turn preparation and Lachesis' minimum memory, and it keeps working whatever else is missing.

Product cognition features are on by default: Atropos' context selection, Lachesis' memory and recall (including the person brief), Jev, each Klotho function and skill suggestions. Each one:

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
| Lachesis | Past: what has built up | The memory engine (facts and preferences, recall, corrections and deletions), the person brief, the material index and derived materials, growth candidates, prepared judgment material | Background |
| Atropos | Present: what to do now | Context assembly and judgment for each turn: the intent of this request, a time-capped recall query on the current input, which memories and materials to include, compression and budget, tracking commitments and task state. Jev lives here. | Request path |
| Klotho | Future: what will come | Planning, next-action candidates, predictions with verification candidates, replanning, goal discovery, project supervision, skill suggestions, the news feed | Background |

### Canon

- The three modules read and write the same canon inside LINA Core, each for a different purpose. Writer rules for memory and persona are in [conversation-and-memory.md](conversation-and-memory.md). Writer rules for materials are in [materials-and-knowledge.md](materials-and-knowledge.md).
- The canon has a part for the whole main and a part for each agent. The main's part holds common knowledge, materials, plans and intent cards, and the prediction ledger. Each agent's part holds its personal knowledge, its persona revisions, its emotional state, its conversations, and its judgment and growth records.
- Each module keeps its own derived state, such as indexes, prepared material and candidate lists. That state is internal to the module and can be rebuilt. It is never canon.
- The work ledger belongs to the main and is outside the modules' canon. Its writer rule is in [work-and-delegation.md](work-and-delegation.md).

### Request path and background

- Atropos is the only module in the request path, which runs from the moment LINA Core receives the person's input until it sends the model request. Atropos must be light and fast. It works for the agent in the conversation.
- For each turn, Atropos assembles the ContextPacket from that agent's persona, its personal knowledge and common knowledge. Its time cap, its minimum packet and the way it enters the request are defined in [conversation-and-memory.md](conversation-and-memory.md), together with the turn latency points.
- Lachesis and Klotho run in `moirai-worker`, a background service that runs whether or not a conversation is active. One `moirai-worker` serves every agent. The service is optional. Without it, only background memory and planning are unsupported. Its packaging is in [runtime.md](runtime.md).
- Inside `moirai-worker`, each function of Lachesis and Klotho runs as its own background job. Each job has its own schedule, budget, on/off switch and adoption record. Budgets count against one total for the main. A job that fails, runs long or exceeds its budget stops only itself and never delays another job or the request path.
- A job that reads one agent's personal records (its conversation ledger and its personal knowledge), such as Lachesis' memory extraction from that agent's conversations, runs as that agent's job. Each item it writes becomes that agent's personal knowledge or common knowledge under the layer rule in [conversation-and-memory.md](conversation-and-memory.md#knowledge-layers).
- A job for the whole main, such as Klotho's goal discovery, project supervision, predictions and the news feed, reads only common knowledge, plans and the work ledger. It runs once for the main, and its output is common. The agent in a conversation speaks the suggestions that come from it.
- Data flows one way. Lachesis and Klotho publish results, and Atropos reads them. Atropos never waits on a background module during a request. Its own query on the current input reads the memory index within its time cap ([conversation-and-memory.md](conversation-and-memory.md)).

### Persona in the modules

Each agent's persona is a layer that all three modules use:

- Atropos puts the persona snapshot of the agent in the conversation into the context of every reply.
- Lachesis turns that agent's accumulated records into growth candidates for that agent.
- Klotho reads the person's preferences and boundaries held in common knowledge, and the rules of the work area, as constraints when it plans or predicts.

Persona storage, revisions and injection are defined in [conversation-and-memory.md](conversation-and-memory.md).

## Jev

Jev is an optional fast judge that only Atropos uses. Atropos uses it only for closed choices that rules can't settle:

- the intent of this utterance and the response mode
- the ranking of context candidates
- where a request goes when the routing rules of [work-and-delegation.md](work-and-delegation.md) leave it open: an answer in the conversation, a task for the work engine, research for a worker thread, work for a device's Node, or a question to the person
- which installed worker takes a task, among the workers whose adapters LINA supports

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

Jev is called through its own non-chat adapter, separate from the model path ([runtime.md](runtime.md)).

## Klotho: project planning

Goal discovery, planning, prediction and project supervision form one project planning function:

1. goal discovery
2. planning, where the person confirms each plan
3. progress tracking: drift, completion time and next-action candidates
4. the prediction ledger
5. risk and replanning
6. design and quality upkeep: tidying intent cards, the QA product map, and research

Every step uses the built-in plan data and the intent cards ([work-and-delegation.md](work-and-delegation.md)). No step has a store of its own. Each step, like supervision, the news feed and skill suggestions, is a separate background job with its own schedule and budget. Every step is a job for the whole main (see Request path and background).

Klotho never executes. It sends requests for code work, other work, research and skill creation to the work engine, and the work engine decides whether and how they run. The one exception is project supervision, which updates its own outputs directly: the tidy-up of intent cards and the QA product map. Even then, Klotho never touches the person's intent text ([work-and-delegation.md](work-and-delegation.md)).

### Goal discovery

- In the background, Klotho reads common knowledge and plans. It turns goals and concerns that keep coming up into initiative drafts, each stated as one sentence of the outcome.
- Discovery stops at the draft. An initiative draft has no task list.
- The agent in a conversation with the person presents the draft. When the person accepts an initiative draft, it becomes a goal. A goal isn't a plan yet.
- Klotho also produces the goal drafts that the agent in the conversation proposes when a memo or a to-do grows into something LINA should track. The promotion rules are in [work-and-delegation.md](work-and-delegation.md).

### Planning

- Goal decomposition has two entry points: an accepted initiative, and a large piece of work that the person hands over directly.
- A plan Klotho drafts becomes a plan only at the plan confirmation point defined in [work-and-delegation.md](work-and-delegation.md).
- Where a card's criteria and the observed state differ, Klotho may draft tasks to close the gap. Those drafts go through the same confirmation point.

### Progress, prediction and replanning

- Klotho tracks progress, drift between the plan and the actual state, and expected completion time. It also proposes next-action candidates.
- The expected outcomes of next actions and the completion-time predictions of supervision go into one prediction ledger, kept in the main's canon. Risk and replanning both read that ledger.
- Klotho proposes a verification candidate for each prediction. A prediction whose outcome isn't observed stays unknown.

### Project supervision

Klotho periodically reviews repositories, plans and cards. Supervision covers:

- scanning repositories and tracking drift between issue state and actual state, through the GitHub plugin ([integrations.md](integrations.md))
- predicting progress and completion time
- research that helps design or learning, requested from the work engine
- refreshing the QA product map
- tidying intent cards
- judging a repository's issues and pull requests through the GitHub plugin and reporting that judgment ([integrations.md](integrations.md))

Every supervision output is a proposal or a report. None of them is approval to execute.

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

- Klotho collects requests, procedures and workflows that the person repeats often. It doesn't build anything right away. Repetition in one agent's conversations is collected by that agent's job, and repetition in the work ledger by a job for the main (see Request path and background).
- Once enough evidence builds up, the agent in the conversation suggests a skill or a plugin. The suggestion shows the evidence: when the repetition happened, how many times, and in what form.
- No agent presses a suggestion the person declined or put off.
- Repetition is only a candidate signal. A repeated request or tool sequence proves neither a procedure nor consent to automate it.
- A candidate records the distinct sessions and dates, the shared purpose, inputs and outputs, the ids of the evidence events, and any counterexamples.
- LINA builds a skill or plugin only once the person understands what it does, when it is used, what permissions it has, and how to undo it. The understanding check runs in this order: show a summary, take questions and adjust the scope, run a reversible trial where possible, then get explicit acceptance. A button click alone doesn't count as understanding. This check covers skills and plugins that LINA proposes and builds; adding an existing plugin follows [integrations.md](integrations.md).
- A skill holds a procedure, instructions and completion conditions. A plugin is needed when the result requires new executable code, a tool connection or a new permission surface. A plugin that widens permissions gets a separate review.
- Creating the skill or plugin is work for the work engine. A skill follows the meta-skill in [conversation-and-memory.md](conversation-and-memory.md). A built skill or plugin is available to every agent.

## Growth

The design of persona growth is decided in implementation. This contract fixes only its boundaries:

- Growth is per agent. Each agent grows from its own records, and the persona it starts from is its baseline: its preset's layers for an agent created from a preset, and the layers written from the person's description for an agent created from a description.
- Growth adoption is the only way an agent's disposition changes. It is also the only way a growth candidate becomes part of that agent's persona.
- Lachesis produces growth candidates from that agent's records. A candidate is not growth.
- An adopted growth step is written as a new revision of that agent's persona through the Moirai engine's write path. Which revisions count as a growth effect is defined in [conversation-and-memory.md](conversation-and-memory.md).
- Growth mechanisms follow the adoption rule.

## LIFE

LIFE is two things: a background world engine, and a feed inside the LINA app. It is not a separate product.

- The world engine keeps a world, its time, its events and the agents' daily activities going in the background. It belongs to the LINA Core family, runs on the main and is Go ([runtime.md](runtime.md)). Whether each agent has a world of its own or the agents live in one world together is set by the LIFE design item in the Backlog.
- The LIFE feed shows an agent's activities, posts, reactions, media and conversation experiences, much like a social photo feed. It isn't connected to Instagram or any other real social network.
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
| Group conversation | Several agents talking with the person in one conversation. In a conversation today, one agent speaks. Each conversation ledger event names the agent that spoke, and each conversation lists the agents taking part in it ([conversation-and-memory.md](conversation-and-memory.md#conversation-ledger)); group conversation is built on this model. |
| Object names | Product names for goal, initiative, project, issue and the materials manager. Until then, documents use working names and describe objects by role and permission. |
| Remote relay and own login | Reaching LINA Core from outside the tailnet through a relay, and a login of LINA's own. Remote access through Tailscale is defined in [runtime.md](runtime.md). |

## Deferred

- The design of persona growth, including the steps of growth adoption: decided by the first growth implementation.
- Jev's confidence threshold, its latency budget, the size of its judgment material, how often that material is refreshed, and the size of the held-out evaluation set: measured during implementation acceptance of Jev.
- Which other hosted judges the adapter supports, such as a model provider's decision API: decided by measurement against Jev on the evaluation set.
- Whether a local judge is offered, which model it starts from and when it is retrained: decided when enough labeled judgments exist to measure it against Jev on the evaluation set.
- Thresholds and frequency of skill suggestions: measured during implementation acceptance of skill suggestions.
- The metric and evaluation set for each regression check and each experimental mechanism, including each preset's persona-likeness set: set by its implementation before it runs.
- The periods for repository scans, card tidy-up and QA product map refresh: set during implementation acceptance of project supervision.
