# Conversation and memory

This contract fixes how LINA's agents talk with a person and what they remember: the conversation engine and its ledger, how each turn is prepared and assembled, how a conversation is compacted and resumed, agents and their personas, the first run, the skill catalog, and memory: its two knowledge layers, what it holds, who writes it, how it is recalled with evidence, how it is corrected and deleted, how history from a Hermes install is imported, and how the person deletes a conversation. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). Finishing this document does not mean any part of it is implemented or accepted; runtime proof belongs to the implementation issues that consume it.

## Scope

This contract covers:

- the conversation engine, the conversation ledger, conversation participants and conversation threads, channel conversations, agent exchanges, the typed results a conversation presents and the export of recorded turns as episodes
- deleting a conversation or a part of it
- turn preparation, request assembly and the evidence chain from LINA state to a delivered answer
- reply shape: how long a reply is, when a card replaces text, and what a reply leaves out
- compaction and resume of a conversation, and the summary tree built over it
- agents and personas: presets, creating, changing and deleting agents, persona layers and revisions, each agent's emotional state and the first run
- the skill catalog
- memory: the personal and common knowledge layers, kinds, writer, storage, the person model, the memory tools, recall with evidence, the external work memory port, importing a Hermes install, correction, retraction and permanent deletion

It builds on these documents and does not repeat them:

- agents, what agents share, the person axis and the agent axis, canon writers and the speaker rule: [product-families.md](product-families.md)
- grants, epochs, receipts and the approval policy: [main-authority.md](main-authority.md)
- the envelope, effect states, idempotency keys and client connections: [host-protocol.md](host-protocol.md)
- the runtime, the loop mechanics, the model path (providers, request format, model switching, auxiliary reasoning, retries), model-provider accounts and sign-in, the turn budget and the replay command, the conversation tools and their execution rules, the default scope, the sandbox, the storage engine, non-chat adapters, usage and budgets, and what LINA Core stops and fences on restart: [runtime.md](runtime.md)
- the work engine, workers, delegation, the line between conversation and work, and how questions and results return to a conversation: [work-and-delegation.md](work-and-delegation.md)
- materials, the library and connected sources: [materials-and-knowledge.md](materials-and-knowledge.md)
- the state root, backup generations and restore: [filesystem.md](filesystem.md)
- the TUI, the LINA app screens, panels and cards: [surfaces.md](surfaces.md)
- the Moirai engine's module structure, which background jobs run for one agent and which for the whole main, Klotho, Jev, growth and skill proposals: [cognition-and-life.md](cognition-and-life.md)

## Terms

**LINA.** The product: LINA Core and the agents it runs. In this document "LINA" names what the product does as a system, and "the agent" names the persona that speaks in a conversation.

**Agent.** A persona that the person creates and talks with. One agent is one persona and one companion. An agent has an agent id, a name, the four persona layers with their revisions, an emotional state, its conversations and its personal knowledge. Agents are data that the one LINA Core runs; there is no process, engine or worker per agent ([product-families.md](product-families.md#agents)). LINA is the name of the default agent, created from the LINA preset.

**Preset.** Persona material that an agent is created from. LINA ships presets, and the LINA preset is one of them.

**Preset methodology.** The management judgment a preset carries for one kind of work, written as short guidance in the preset's core layer. Its mechanics are LINA Core mechanisms named in the contract that owns them ([Preset methodologies](#preset-methodologies)).

**Personal knowledge.** Memory held by one agent and known only to it. The conversation ledgers of an agent are its personal records.

**Common knowledge.** Memory held by every agent.

**Conversation ledger.** The append-only event log of every conversation, held in LINA Core canon. It is the only record a conversation is rebuilt from.

**Channel conversation.** A conversation that takes place in a chat of an external channel ([integrations.md](integrations.md#channels)). The owner's own chat with an agent's bot is that agent's main conversation with the owner. A chat shared with other people is a separate conversation of the bound agent.

**Agent exchange.** A background conversation between exactly two agents, one that asks and one that answers, with no person turn. It is held in the conversation ledger and shown to the person as a log of one line per exchange.

**Speaker kind.** Who the content of a ledger event comes from, set by the path the input entered LINA: `person`, `agent`, `external_participant` (a human in a shared chat of an external channel who is not the paired owner), `automation`, `forwarded`, `system` or `tool`. It is separate from the role a model API gives a message ([Event fields](#event-fields)).

**Summary tree.** The derived record of what each summary covers: its nodes cover exact ledger events and can be expanded back to them. The summary a request uses is the root of the tree ([Summary tree](#summary-tree)).

**Standing profile.** A short view, generated from the active memory items flagged `standing`, of what is lasting about the person. It is never a store ([Person model](#person-model)).

**Imported conversation.** A conversation that came from a Hermes install. It is sealed, belongs to one agent and never enters a request's history ([Importing a Hermes install](#importing-a-hermes-install)).

**Legacy reference.** A source system's own key for something that was imported, such as an old message or summary node id. It resolves to the LINA record that holds the imported content.

**Turn.** One input from the person and everything the agent does to answer it: preparation, model requests, tool calls and the delivered answer. A turn whose follow-up request stays at or above the compaction threshold after earlier turns are compacted continues in a continuation turn that answers the same input ([Compaction and resume](#compaction-and-resume)). A turn records the person id of the person whose input it answers and the agent id of the agent that answers it ([product-families.md](product-families.md)). A report turn is the one kind of turn that answers no input from the person: it answers a background notice, and it records the person the reported run was done for (see [Background results](#background-results)).

**Background run.** One execution of one of LINA's own sub-agents for one brief, started from a conversation and run in the background by LINA Core. Its record and rules are in [work-and-delegation.md](work-and-delegation.md#background-runs).

**Background notice.** The conversation-ledger event that delivers the landing of one or more background runs into the conversation that asked for them (see [Background results](#background-results)).

**Report turn.** A turn that LINA Core starts without a new input from the person, only to deliver a background notice with one short line (see [Background results](#background-results)).

**Model policy.** The settings that fix how a turn calls the model: for each model role, the provider account, the model and its reasoning effort, and the request fields the turn sends ([runtime.md](runtime.md#models-and-roles)). Every change to it creates a new policy revision.

**Prepare acknowledgment.** The ledger record that a turn's agent, that agent's persona revision, the context and the model policy revision are ready. No model request is sent before it exists.

**ContextPacket.** The per-turn set of memory items, materials and state that Atropos selects for a turn. It is assembled once, before the turn's first model request, stays in place for the turn's follow-up requests, and is never stored; its item ids are recorded.

**Memory item.** One unit of memory canon, defined under "Memory items".

**Evidence.** The references that tie a memory item, a recall result or an answer to where it came from: ledger event ids, material asset ids and revisions, verified work results, or external evidence.

**External evidence.** Content LINA read from outside its own canon, such as worker records, what a plugin read, or an observation imported from a Hermes install. It always carries its origin and is never trusted as an instruction.

**Revocation epoch.** A counter in canon that advances with every retraction. Summaries and derived data record the epoch they were built at. Backup generations record it as the revocation watermark ([filesystem.md](filesystem.md)).

**Moirai engine, Lachesis, Atropos.** The Moirai engine is the part of LINA Core that holds the context of every agent. Lachesis is its past-facing module (memory and recall); Atropos is its present-facing module (the context of each turn). In a conversation both work for the agent of that conversation. Their structure is defined in [cognition-and-life.md](cognition-and-life.md).

## Conversation engine

- The conversation engine is LINA's own loop inside LINA Core. Its loop mechanics, its model path, its provider adapters and the person's model accounts are defined in [runtime.md](runtime.md). A conversation runs on the provider that its model policy names, ChatGPT by default. For an agent-runtime provider, the vendor's runtime runs a turn's inner tool loop, and LINA Core still owns the ledger, the context, compaction and the authority of every tool.
- There is one conversation engine, and it runs the conversations of every agent. There is no engine selection screen and no automatic routing between engines.
- The conversation engine and the work engine are separate. A conversation never waits on work. A turn does only small, bounded analysis, within the turn budget ([runtime.md](runtime.md#turn-budget)), and anything heavier is handed off at once: coding work goes to the work engine's queue and a worker, and research, analysis, multi-file reading, drafting and read-only checks go to a background run of one of LINA's own sub-agents ([work-and-delegation.md](work-and-delegation.md#background-runs)). The hand-off changes where the work runs and nothing else: its done condition and its checks are the same as in the turn, and the agent never presents a partial result as an answer. The person can keep talking in the same session while the work runs, and the result returns as a short notice between turns (see [Background results](#background-results)). What a conversation does itself and what it hands off is the kind-of-work rule in [work-and-delegation.md](work-and-delegation.md). An agent exchange never waits on a conversation, and no conversation waits on an exchange.
- Basic conversation needs no optional component. It works without `moirai-worker`, without a worker setup, without non-chat adapters and without a sandbox. Each missing component makes only its own functions unsupported ([runtime.md](runtime.md)).
- Answers stream. The earlier conversation is shown. A connection failure appears as an error on screen, never as silence or an empty answer.
- Only an agent that takes part in a conversation speaks in it ([product-families.md](product-families.md#speaker-rule)).

## Conversation ledger

- Every conversation is written to the ledger from its first input. Each event carries a parent id, so a conversation can branch. An event whose speaker kind is `person` carries that person's id, and no event of another speaker kind does. An event that holds what an agent said or did carries that agent's id. Each event also records the surface it came through (the TUI, the LINA app, a channel with its platform and chat ids, or an agent exchange) and its speaker kind ([Event fields](#event-fields)).
- Some events are system-originated (speaker kind `system`), such as the notice of a finished background run. A system-originated event is marked as such, names the agent whose conversation it belongs to and the person it concerns, carries no agent's speech, and is never read as the person's words.
- Each conversation records its participating agents. A conversation with the person has one participating agent, and that agent is the only one that speaks in it. An agent exchange has two. A group conversation, where several agents talk with the person in one conversation, is built on this model later; it is a backlog item in [cognition-and-life.md](cognition-and-life.md#backlog).
- The ledger of a conversation is the personal record of the agents in it. Their recall and ledger search read it; no other agent reads it. The person sees all of it. An agent exchange is the personal record of both of its agents; the person sees all of it, on request (see Agent exchanges).
- Model requests are assembled from the ledger, never from a client's copy or an engine's thread.
- After a restart the same conversation continues from the ledger with the same agent. A restart never creates a new agent, and there is no engine thread to bind or rebuild.
- The ledger is never rewritten. The one exception is the person's explicit deletion of a conversation or a part of it (see "Deleting a conversation"), which includes deleting an agent (see "Agents"). Forgetting or deleting a memory item never changes the ledger.
- Only LINA Core writes the ledger. LINA APP and the TUI send input and read history through [host-protocol.md](host-protocol.md); they never write it.
- The ledger is held in canon and is part of every backup generation ([filesystem.md](filesystem.md)).
- The ledger is not the self-diagnosis log. Diagnosis events reference a conversation by id and never hold message content ([self-diagnosis-log.md](self-diagnosis-log.md)).

### Event fields

Every event carries, besides its parent id and the person id or agent id above:

- `speaker_kind`, set by the path the input entered LINA: a message from the person, a message from someone else in a shared chat of an external channel, the delivery of a scheduled job, a background notice, a message from another agent, forwarded content, a system line, or a tool output. It is never decided by matching text. An imported event has no entry path, so the importer sets the kind from marker text and records that it did. Only events of kind `person` can be the evidence of an item whose subject is the person (see "Memory items").
- `surface`, the way the event arrived or was produced: the TUI, the LINA app, a channel with its platform and chat ids, or an agent exchange.
- `scaffold_kind`, for text that LINA or a tool wrapped around an input, such as a skill invocation, a scheduled-job prompt, a background notice or a summary echo. Scaffold text is stored once, and the events that use it refer to it.
- `occurred_at`, the time of the event in UTC, and `recorded_at`, the time its source wrote it. `time_quality` says how `occurred_at` was learned: `observed`, `recorded` or `write_time_only`. Filters, ordering across sources and the time bounds of summary nodes use `occurred_at`. Day, week and month buckets use the person's time zone.
- `origin`: native, or imported with the source system, the instance and the legacy references of the record.
- `replay_of`, the id of the canonical event, when the same content was stored again, for example when a source copied rows into a later session. A copy is not separate evidence and is left out of summaries and default search.
- relations to other events: `view_of` with an ordinal (one source message presented as several), `supersedes`, and `pairs_with` (a tool call and its output).
- `sensitivity`, per span: `secret`, `personal_id` or `third_party`.

Rules:

- Native admission fixes speaker kind from a trusted entry enum, independently of body text, model role labels and claimed identities. Unknown entries are refused. Tool results and content fetched from plugins, the web or files retain speaker kind `tool` and their source locator. External participants, forwarded content, agent exchanges and background notices retain their own speaker kinds and also enter model requests as external evidence: user role, explicitly untrusted, with a fixed preamble and escaped body and origin delimiters. Evidence without an origin is refused. No message text, even from the person or containing a valid pending request id, answers an approval; an answer requires the separate authenticated interaction path ([main-authority.md](main-authority.md#approval)).
- A secret-shaped value or a private-key block is masked before a native event is written, and the event keeps the flag and not the value, so nothing downstream can restore it. A span flagged `secret` is excluded from every index, summary, ContextPacket and export, and from every call to a service outside LINA that does not already hold it. Spans flagged `personal_id` and `third_party` are tags: they decide default scope and what the maintenance pass reports, not whether an agent can read the event.
- Native masking records use half-open UTF-8 byte offsets in the replaced text, not offsets or lengths that retain the original secret. Overlapping secret spans coalesce. Display and storage retain a placeholder and the `secret` flag; the shared derived-text projection removes flagged spans before indexing, summarization, ContextPacket construction or export, without joining the words on either side. Explicit secret spans are required for sensitive values a shape detector cannot recognize. This native representation does not replace the separate sealed-original rules for imports.
- A body above an inline size is kept as a LINA material and the event refers to it ([filesystem.md](filesystem.md)). The full body can always be read back from the event. When the bytes behind an event are missing, the event is marked `bytes_unavailable` and is never shown as complete.
- A summary is never a ledger message. Compaction writes a "compaction recorded" event that names its input range ([Compaction and resume](#compaction-and-resume)).

The native store represents event identity, conversation and parent links, speaker identities, surface, scaffold kind and reference, both UTC times, time quality, origin and replay reference in `conv_event`. Its body is either inline masked text or an immutable material id and revision. Native origin includes the trusted entry and the source locator; retaining the entry preserves external-evidence treatment on readback. `ev_rel` holds the event id, target id, relation kind and ordinal (zero for relations other than `view_of`). `ev_span` holds the event id, start, end and sensitivity. `bytes_unavailable` is a read-time status, so discovering lost material bytes does not rewrite the event. For oversized bodies, retained material metadata commits in the same transaction as the event; an abandoned preparation is removed on rollback or recovery. Admission returns only after all three sets of rows commit; updates, replacement inserts and ordinary deletes are refused. The explicit deletion flow must implement its separate authorization and propagation contract.

### Deleting a conversation

The ledger is the original record of a conversation. Its content is erased only when the person explicitly deletes a conversation or a part of it.

- The deleted content is retracted at once and moves to the trash for 30 days, where it can be recovered. After 30 days it is permanently deleted. The person may skip the trash; that choice is the approval for the unrecoverable deletion ([main-authority.md](main-authority.md)).
- Permanent deletion erases the chosen ledger content, and the derived data built from it such as summaries and index entries, from every copy LINA manages (see "Erasure scope" under "Correction, retraction and deletion"). It leaves a tombstone: the ids of the erased events, the legacy references of any that were imported, the agent the conversation belonged to, when, and the person who deleted them, with no content.
- Memory items whose evidence was in the erased content stay until the person forgets them. Their evidence, and the evidence records of past answers, show the tombstone in its place.
- The results presented in the conversation are part of its content. Their previews, revisions and file bytes are erased with it, from every copy LINA manages. A result the person saved is a material and stays, and its provenance shows the tombstone in place of the conversation. A card that was sent to an outside channel is a copy outside LINA's management, and LINA says so when the person deletes.
- Background runs started from the deleted content, with their transcripts, result artifacts and notices, are derived data of that content and are erased with it. A result that the person saved as a document or record stays; once saved it is no longer derived data.
- An imported conversation is deleted like any other. The Hermes install and the Honcho server hold their own copies, which are outside LINA's erasure scope, and LINA says so when the person deletes.

### Results

A result is the typed output of [host-protocol.md](host-protocol.md#typed-results) that a conversation presents as a card. An agent presents one with LINA's `present_result` tool ([runtime.md](runtime.md)). A background run that produces one lands it as [work-and-delegation.md](work-and-delegation.md) defines.

- Every revision of a result is one ledger event that holds the whole result, body included, with who made the revision and the turn. A preview lives only in the ledger and has no other copy.
- The model sees a result as the output of the tool call that presented it: the id, the revision, the type, the title, the summary and the first part of the text (a bound that the conversation engine sets). It never sees the body. Nothing about a result is rewritten in the request history later, so the history only grows at its end.
- The agent reads the current body with `read_result`, which returns the revision it read. A revision that the agent presents for an existing result must name the revision it read, and a stale one is refused with `stale_revision`.
- When the person edits a result in the work panel, the ledger records the new revision, or for a saved result the saved version, as an event. The request history receives one line that names the result, the revision or version and who edited it, not the content.
- A result is found through its title, summary and text by ledger search, like any ledger content. A preview is not indexed as knowledge, and Lachesis derives no memory item from a result body. What the person says about a result in the conversation counts as the person's words.
- A result that the person saved is a material ([materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result)). The result then takes no new revision, and the agent finds the saved place in `saved_as`. An agent never saves a result on its own.
- Citations in a result are passage anchors and follow the evidence rules for past answers: revoked or deleted evidence is shown as such and never silently replaced.

**Summary first.** For a document expected to run longer than about two screens, the agent presents a `summary` result before it writes: the outline, the key points, the decisions and open questions, what the document will and will not cover, and a length estimate. It skips this only for a document of one page or less, or when the person asks to have it written at once.

- The person edits the outline in the work panel or by chat and answers the open questions. Nothing is written until the person asks for it.
- The writing is background work and never part of the conversation turn ([work-and-delegation.md](work-and-delegation.md)). The agent hands it off with the latest revision of the outline as an explicit input, and the finished `document_draft` lands as a result card at a turn boundary, with `from_summary` naming the outline it came from.
- The outline is a ledger event from the start, so a document that is never written still leaves a usable record.
- The draft stays a preview until the person saves it. It then becomes a document in the place that the person chose, such as 자료 (resource) (the library of resources) or the 일 (project) (a piece of work with a done condition) the conversation is attached to.

### Threads

- Each person has one main conversation with each agent, and there are any number of threads. Each thread is attached to one artifact, such as a document, a 목표 (goal) or an 일, and one agent. An imported conversation is neither the main conversation nor a thread.
- Every thread of an agent is a conversation with that agent and reads the same memory: that agent's personal knowledge and common knowledge. A thread is a window for dividing talk, not a memory partition. Threads of different agents share common knowledge.
- When the main conversation turns to a specific artifact, the agent moves to that artifact's thread and says so in one line; one action returns to the main conversation.
- Between the threads of one agent, sourced facts, decisions and materials are shared, and the results of that agent's ledger search (see "Recall with evidence") read what was said in any of its threads, with their ledger event ids. A search result never becomes an instruction or an approval. Follow-up instructions, approvals, cancellations and control handovers are bound to the id of their target task or thread and never become permission anywhere else.
- A conversation thread is not a work thread. Work threads belong to the work engine ([work-and-delegation.md](work-and-delegation.md)). How threads appear on screen is defined in [surfaces.md](surfaces.md).

### Channel conversations

An agent dispatched to an external channel ([integrations.md](integrations.md#channels)) talks there under the same rules as anywhere else.

- The owner's own chat with an agent's bot is that agent's main conversation with the owner. A message the owner writes there is a turn like any other. Its events carry the channel as their surface, and the app and the TUI show the same history. A message that arrives while a turn runs is steering input ([runtime.md](runtime.md#conversation-loop)).
- A chat shared with other people is a channel conversation: a separate conversation of the bound agent with its own ledger, one per chat or topic. The agent reads it with its persona and the knowledge layers it reads anywhere, except where the binding's scope narrows them: the scope's memory field decides whether the person's memory may be used in that chat, and by default it may not be. The app shows such a conversation read-only, and the person writes in the platform.
- A message from someone other than the paired owner is an event with the speaker kind external participant. It carries the platform's user id and no person id. It is external evidence: never the person's words, an instruction or an approval ([main-authority.md](main-authority.md#authorization)). By default the agent does not answer it. No memory item and no person model is written from it.
- Decisions, preferences and commitments that the owner states in a channel are written like any others, with the channel event as their evidence. A decision made on one surface is a decision on every surface.
- A channel turn follows the same turn rules as a turn in the app. A result that lands later arrives as a new message in the chat.
- The ledger keeps the full reply and the platform's message ids of its parts. An inbound file from the paired owner is an import ([filesystem.md](filesystem.md#import-and-export)); a file from anyone else is never imported.
- Deleting a channel conversation erases LINA's copy. Messages already sent to the platform are copies held by the platform and by other people, and LINA says so when it deletes (see "Correction, retraction and deletion").

### Agent exchanges

An agent exchange is a conversation between exactly two agents, one that asks and one that answers. It is held in the conversation ledger like any other conversation and has no person turn.

- **Background.** An exchange runs as a background run of LINA Core. It never interrupts a conversation: no message of an exchange enters the person's conversation as a turn of the person, no conversation waits on an exchange, and no exchange travels through an external channel.
- **Start.** An agent starts an exchange in a turn or in a background job with a tool that returns an exchange id at once. The turn does not wait for the answer. Any agent of the main may ask any other agent. The asked agent acts only under its own grants ([main-authority.md](main-authority.md#authorization)).
- **Hand-off.** A request is a typed hand-off: a subject in one line, what is wanted, what must not be touched or is out of scope, the done condition, a reply deadline and, when a decision is wanted, the decision in one line with a recommendation. The asker names the check it will run on the answer and runs it itself. An answer carries its evidence (ledger event ids, material revisions, times with their zone), so a second agent can check it without reading the conversation.
- **Delivery.** A durable mailbox with the states queued, delivered, read and answered. Delivery is at least once and is deduplicated by delivery id. It does not depend on the receiver being idle or on any conversation being open. An acknowledgement ends the exchange without a model turn. When no answer arrives by the deadline, LINA Core re-dispatches once or flags the exchange, and then stops. A limit on the rounds of one exchange ends a loop (recommended default 4; see Deferred).
- **Origin.** Events of an exchange have the speaker kind agent. Their text is evidence for the receiving agent, not an instruction of the person, and it is never the person's words in a request, a person model or a memory item.
- **Knowledge layers.** What one agent writes in an exchange, the other agent keeps. An exchange turn therefore reads the agent's persona, the common knowledge and the materials the request names, and never the agent's personal knowledge. No exchange moves an item between the layers; that stays the recorded act of "Knowledge layers".
- **Memory.** No memory item about the person rests on an exchange alone. An item may cite an exchange event as evidence only together with the person's own event or a verified work result that the exchange relays. An agent's request or promise to another agent is the state of the exchange (subject, deadline, answer), not a Commitment of the person.
- **Work.** An answering agent that needs work files a task as usual ([work-and-delegation.md](work-and-delegation.md)). The task's requesting agent is that agent, and its questions and results return to the exchange.
- **Log.** The person sees one line per exchange: the asker, the answerer, the subject, the state and the time ([surfaces.md](surfaces.md#agent-talk-log)). Expanding a line shows the exchange. The exchange is kept in full in the ledger, hidden by default and searchable on request. A flag `needs the person` is set only when the person is the decision owner. That flag puts the exchange in "Waiting for me" and notifies the person under the notification settings, and nothing else about an exchange notifies.
- **Result.** When an exchange that an agent's conversation depends on is answered, a short system-originated notice reaches that conversation at the next turn boundary, never in the middle of a reply, and is never read as the person's words.
- **Voice.** Each preset's default voice layer carries one line for how its agent writes to other agents. The register is plain and short.
- **Deleting an agent.** An exchange stays as a record of the other agent, and the deleted agent is named by its id. The deleted agent's personal knowledge is erased as under "Agents".

### Episode export

The ledger can be exported as episodes: recorded turns written out for use outside LINA. The purpose is the development of LINA's own harness files, which [runtime.md](runtime.md#replay-command) defines with the replay command: a developer's loop improves them by re-running episodes with `lina replay`. LINA ships no such loop, and an install exports nothing unless the person runs the export.

- An episode is one turn, or a few consecutive turns, of one conversation. It holds the person's input events, the assembly list that the turn recorded (the agent id, the persona revision, the version of each skill body and the ContextPacket item ids), the clock and zone of the turn, the model role and effort, the reply, the tool calls with their recorded results, and what the person said next, with any correction that cites it.
- Only a turn that starts from an event whose speaker kind is `person` can start an episode. Scheduled deliveries, background notices, agent exchanges, compaction summaries and imported conversations are not episodes.
- The export leaves out content that is retracted, trashed, deleted or forgotten, content whose scope does not allow it, and every span flagged `secret`, which stays masked. A turn that rests on left-out content is left out whole, and the manifest says how many turns and why.
- The person chooses the agent, the conversations and the period. Nothing is selected by default.
- The export reads the ledger and nothing else. It writes no ledger event and no memory item, makes no model call and changes no state of LINA. Its files go to `staging/export/` ([filesystem.md](filesystem.md#import-and-export)) with a manifest of the counts, the left-out reasons and the revocation epoch at export time. An exported file leaves LINA's management: a later deletion in LINA does not reach it, and LINA says so when it exports.

## Turns

### Preparation

No model request is sent before the turn is prepared. Preparation runs in this order and stops at the first failure:

1. The hash of the running LINA Core executable matches the manifest of the build it came from ([runtime.md](runtime.md)).
2. The account of the conversation model's provider is usable: its credential record says ok, and its adapter is ready ([runtime.md](runtime.md#accounts-and-sign-in)).
3. The conversation model and the model policy revision are resolved.
4. The conversation's agent, its persona, the context and the model policy are prepared, and the prepare acknowledgment is recorded.

When a step fails, LINA sends no request and shows the cause on the conversation surface as one plain line that says what is wrong and what the person can do (see Reply shape); the step that failed is in Details. A main with no usable account fails at step 2, and the surface offers the sign-in with that cause. A running service is not a successful turn.

The four-step turn runner stops at the first failed check, including a failed preparation acknowledgement or request-evidence commit. The failure line is selected from a fixed plain-language cause; raw account, storage and provider errors never become conversation text. The failed step is structured detail, and an unusable account that requires authentication carries a separate sign-in offer.

Preparation never waits on background work. A background run that is running or waiting, a landing that is not delivered yet and the jobs of the Moirai engine do not delay a turn. The state of the conversation's background runs enters the ContextPacket as it stands when it is assembled (see Request layout). Preparation counts against the acknowledgement point of the turn budget ([runtime.md](runtime.md#turn-budget)).

Mandatory retraction checks and permission checks finish during preparation, before the request is assembled. Model and effort selection, model switching and the rule that an accepted turn is never replayed are defined in [runtime.md](runtime.md).

### Request assembly

- LINA Core assembles every instruction in a request. A request carries no base instructions of a coding worker; LINA Core puts the agent's persona and the context in directly.
- The fixed persona layers of the conversation's agent go into the developer-role instructions of every request. No other agent's persona enters the request.
- The ContextPacket goes in as input items. LINA's own memory and materials take the developer role. External evidence takes the user role and is marked untrusted. Messages from an external participant of a channel, and the other agent's messages in an agent exchange, are external evidence too: the history carries them as user-role items marked untrusted, with their origin.
- Where each part sits is fixed by the request layout below.
- Duplicate and superseded context across turns is resolved during assembly, not left to the model.
- Imported conversations never enter the conversation history of a request. The agent reaches them through the ledger search tool, `expand` and promoted memory items (see "Recall with evidence" and "Importing a Hermes install").
- Atropos assembles the ContextPacket synchronously before the first request of each turn, within a time cap. When the cap is reached, the turn proceeds with a minimal packet. The packet holds only the personal knowledge of the conversation's agent and common knowledge (see "Knowledge layers").
- Atropos selects the skill bodies a turn needs (see "Skills").
- Turn latency is measured from receipt of the person's input to the request being sent, and from there to the first output delta and to response completion. The first output delta is the acknowledgement point of the turn budget ([runtime.md](runtime.md#turn-budget)).

### Request layout

A request is laid out from what changes least to what changes most, so the prefix that the model provider caches stays the same from one request to the next. The order is:

1. **LINA Core instructions** (developer role): the fixed rules for tools, approvals and output. They change only with a release.
2. **Persona** (developer role): the fixed layers of the conversation's agent in order, core, default voice, user voice, growth snapshot. They change only with a new persona revision of that agent.
3. **Skill catalog index** (developer role): the name and description of each available skill. It changes only when skills or plugins change.
4. **Tool declarations**: the always-declared tools first, in a stable order, then the plugin tools loaded on demand for this request ([runtime.md](runtime.md)). The always-declared part changes only when a plugin or a grant changes. The on-demand part can change from one request to the next.
5. **Conversation history**: the compaction summary, if any (the root of the summary tree), and then every earlier item of the conversation from the ledger, in order: messages, reasoning items, tool calls and tool outputs, carried unchanged ([runtime.md](runtime.md)). It only grows at its end. Its earlier part changes only at compaction (see Compaction and resume) and when the person deletes conversation content (see Deleting a conversation).
6. **ContextPacket** (input items), from the most binding to the most optional:
   1. the current time and the agent's present emotional state
   2. commitments, decisions, the person's standing preferences about how the agent talks and works with them, the reasons for corrections that apply, and the reply budget of the surface the turn answers on (see Reply shape)
   3. state that waits on the person: open questions, pending approvals, the state of active tasks and plans, and one short line for each background run of this conversation that is running, waiting, or landed and not yet delivered (what it does, its state and when it started)
   4. the skill bodies this request needs
   5. the standing profile, recalled memory items and the person brief, with their evidence ids
   6. material passages, with their asset ids and revisions
   7. external evidence (user role, marked untrusted): worker results and what plugins read
7. **The person's current input.**

- This layout is the first request of a turn. When no compaction runs during the turn (see Compaction and resume), a follow-up request in the same turn, after tool results, keeps that request unchanged, apart from the plugin tools loaded on demand, and appends at its end, in ledger order, only the new reasoning items, tool calls and tool outputs and any steering input the person sent during the turn, in the user role ([runtime.md](runtime.md)). The ContextPacket is not rebuilt within a turn.
- This layout is the request of a model-API provider. For an agent-runtime provider, the adapter receives the same layers in the same order as one non-persisted session. The runtime's own reasoning items do not return in later requests, the exact request input cannot be recorded, and the ledger records what the runtime reports instead ([runtime.md](runtime.md#providers)). How a runtime caches a prefix is the vendor's.
- Nothing that changes from turn to turn enters items 1 to 3 or the always-declared part of item 4. The time, the emotional state and the selected skill bodies sit in the ContextPacket. When the plugin tools loaded on demand change, the cached prefix ends at that point.
- Requests in conversations with different agents differ from item 2 on, so they share the cached prefix only through item 1.
- The ContextPacket is never written into the history. When no compaction runs at the turn boundary, the next turn's request therefore matches the previous turn's requests up to the position where the previous ContextPacket stood.
- History consumers read the ledger's committed masked text, relations and spans. They preserve native entry provenance when constructing external-evidence items, and treat a `bytes_unavailable` event as incomplete. The #57 store supplies those records; it does not implement preparation, assembly or ContextPacket construction.
- When the ContextPacket exceeds its budget or Atropos' time cap, items drop from the end of the packet: external evidence first, then material passages, then memory items and the person brief, each by lowest rank first. Within item 6.5 the standing profile and the person brief drop last. Items 6.1 to 6.4 are never dropped.

Assembly takes immutable snapshots and a model-specific counter. The request budget counts the whole request, including tools; optional groups are removed in the order above, with lower ranks removed first and stable input order breaking ties. A packet item belonging to a different agent, a retracted or secret item, a duplicate id or a superseded id is excluded before counting. Supersession may not remove a mandatory group, and duplicate ids spanning mandatory and optional kinds are refused. A superseded fact does not become current again when its replacement is dropped for size. If mandatory input still exceeds the budget, assembly returns a typed failure and no request. It never drops the person's input or alters opaque ledger items to make a request fit.

The time-capped collector receives mandatory groups already prepared. Its 250 ms default deadline bounds optional collection; timeout keeps those groups and excludes every late result. Cancellation of the turn itself aborts preparation. The packet is copied into the first request and reused for follow-ups, and a new turn builds a new packet from state. The native ledger stores preparation and request evidence as system-originated `prepare_ack` and `request_assembly` events, separate from model history. A trusted `provider_item` scaffold identifies opaque model/tool items for unchanged history projection. After any context transform, every follow-up is counted against the same model limit before dispatch; an oversized request returns a typed size failure without sending or altering the packet and current-turn items. No packet body is handed to either preparation-evidence or request-evidence recording callbacks. These callbacks receive only ids, revisions and, for a request, its digest and provider fidelity class.

### Evidence chain

The chain from state to answer is: LINA state and preparation inputs, then the model request sent, then the response and the real tool effects, then delivery to the person.

- For every request the ledger records the request hash and the assembly list: the agent id, the persona revision, the version of each skill body the ContextPacket carries and the ContextPacket item ids.
- The chain keeps queued, selected, sent and observed apart. An item queued for recall is not an item selected for a packet, a selected item is not a sent item, and a sent item is not an item observed in the answer.
- Successful configuration or delivery of input is never reported as persona quality or recall quality. A response completion event is not evidence of a product effect.
- Completion, memory and sync are never decided by model text. An agent tells the person it remembered, corrected or forgot something only when the memory write is committed in canon.

### Reply shape

A reply is short by default. It leads with the conclusion, and detail goes to the work panel, never inline. This section shapes replies only. It changes no memory content, no judgment and no permission, and it applies to every agent on every surface. It carries out the north star of [product-families.md](product-families.md#north-star). The rules are part of the LINA Core instructions (item 1 of the request layout); the budgets of the surface that the turn answers on are in the ContextPacket (item 6.2).

**Budgets.** LINA Core holds a budget for each reply type, set for the person. These are the starting values:

| Reply type | Budget |
| --- | --- |
| Chat reply | 1 to 3 lines and at most 150 characters. The hard ceiling in chat is 5 lines and 300 characters. |
| Status ("where are we") | For one item, at most 3 lines: where it stands in concept words, what is blocked, what is next, and the number of decisions waiting. For everything that runs, the digest of [Progress at a glance](surfaces.md#progress-at-a-glance): what needs the person first, then one line for each running item, at most five lines and a count of the rest. |
| Decision question | At most 7 lines: what happened in one sentence, why it matters in one sentence, the recommendation first, and the one reply that approves it. |
| Notification | About two sentences with a link, or one card. |
| Report from a scheduled check, a watcher or a sub-agent | One line ("normal", "2 need checking") and a card, never the report body. |

The character counts are for Korean; for other languages Core uses the count that carries the same amount of text. Anything that would exceed the ceiling becomes a card (see When a card replaces text).

**Rules.**

- Conclusion first. No heading and no bold label. Bullets only for three or more parallel items.
- Plain words. A reply uses the person's concepts and never the internal vocabulary listed under Terms in [surfaces.md](surfaces.md#one-design-language). Ids, hashes, paths, commands, logs and code never appear inline; they are in Details.
- No filler. A reply does not end with "nothing to decide" (that empty state belongs to Waiting for me only), does not explain the agent's own settings or process unless asked, and does not narrate tool steps.
- Background work stays silent. Memory extraction, memory maintenance, the wrapper lines of scheduled jobs and tool narration are not messages of the conversation; they go to the activity log ([surfaces.md](surfaces.md#default-view)). When the person asks to remember, correct or forget something, the confirmation is one line, sent only after the write is committed (see Evidence chain).
- Honest and proportionate. A reply never exaggerates or shrinks what the person said, and it separates what was checked from what was told in a clause, not a section. A reply is not more than about six times the length of the person's message when that message is under 40 characters, unless the person asked for detail or a result.
- A short reply is a short presentation of complete work. A budget never shortens the work: no answer is made less thorough, summarized away or left unchecked to fit a budget, and what is left out of view is one tap away in a card ([runtime.md](runtime.md#turn-budget)).
- The persona keeps its voice, and the budgets apply on top of it.

**When a card replaces text.** A reply becomes one line plus a card (see [Cards and detail on request](surfaces.md#cards-and-detail-on-request)) when any of these holds: it needs more than the hard ceiling; its content is a table, a comparison, a diagram or chart, a list of four or more items, a draft document, a diff or a plan; it reports on a background job; it needs ids, paths, logs, commands or code; or it is a long document the person will keep, in which case a short summary card comes first and the document is written after the person approves the summary. The line says in a few words what the card holds.

**Detail on request.** Detail is pulled by the person. Every card has Details, a typed follow-up gets the next layer in at most three lines plus a card, and a typed request for the whole thing opens the full version in the work panel at once. A long answer is acceptable only there, and it lands in the panel.

**Enforcement.** Core checks the budgets, because instruction text alone does not keep replies short. While a chat reply streams, Core stops showing it at the hard ceiling, ends the visible text at the last complete sentence before it, and offers the rest as a card; the ledger keeps the reply as written. For every reply Core counts a breach of a budget and the appearance of internal vocabulary, an id, a hash or a path in the default view, and the counts feed the agent's regression checks ([cognition-and-life.md](cognition-and-life.md#adoption-rule)). A check that rejects or rewrites a reply before it is shown, instead of cutting it, is not built, because it would cost later first words. Acceptance measures the budget and the patterns, never the wording of a reply.

**Tuning to the person.** The budgets are a setting of the person. They are not a memory item and not persona text. Lachesis recognizes, in the ledger, the person's reactions that show a shape problem: a request to be shorter or simpler, "I do not understand", and a bare acknowledgement after a long reply. Each reaction lowers the budget of that reply type by one step (20 percent, down to a floor of one line and 60 characters). A month without such a reaction raises it by one step, never above the starting value, and the person's own instruction ("shorter", "longer") sets it directly. Each change records the reactions it rests on, and the person sees and edits the budgets in settings ([surfaces.md](surfaces.md#settings)).

### Background results

A background run lands on its own schedule. Its result re-enters the conversation that asked for it as a short notice, and never as an interruption.

- **Landing and notice.** When a run lands, LINA Core records the landing in the work ledger at once ([work-and-delegation.md](work-and-delegation.md#background-runs)). The notice reaches the conversation ledger as a background-notice event when it is delivered, not when the run lands, so the history of a request only grows at its end. The event is system-originated (see Conversation ledger). It holds the run id, the outcome word (complete, partial, failed, cancelled or needs input), one or two sentences of summary that say what was verified and what was not, the reference of the result artifact and the result type that the card uses ([surfaces.md](surfaces.md)).
- **Delivery timing.** A notice is delivered at a turn boundary. It is never delivered while an answer streams, between the tool calls of a turn or in a follow-up request of a turn. When the person's own input arrives at the same moment, the input goes first and the notice follows the answer. When no turn is in flight, the notice is delivered at once.
- **Coalescing.** Notices of one conversation that are still waiting for a turn boundary, whose runs land within 10 seconds of each other, are delivered as one notice. Idle delivery stays immediate: Core never delays an idle notice to collect future landings. A delivered notice is never rewritten or extended; the conversation ledger grows only at its end.
- **What the person sees.** One line and one card in the conversation. The line says in plain words what happened for the person, and begins with "done" only when the done condition is met. The card opens the result in the work panel ([surfaces.md](surfaces.md)). The notice never carries the result body.
- **What the model sees.** In the next request the notice is an input item at its ledger position, in the user role, marked as external evidence. A sub-agent's text is never an instruction or an approval. LINA Core scans it for instruction-like content before it enters a request, and a notice whose text fails the scan reaches the model as its outcome word and result type only. The result artifact enters a request only when the agent reads it with its reading tools, again as external evidence.
- **Needs input.** A run that needs a decision of the person lands as a question card in the conversation that asked, with the outcome word needs input. No model answers it. The person's answer goes to that run, bound to its id ([work-and-delegation.md](work-and-delegation.md#messages-and-questions)).
- **Partial, failed and cancelled.** They follow the same path with their outcome word and keep the artifact of what the run gathered. A partial notice says what is missing and that the run can be resumed, and it is never worded as an answer. An error that ended a run is never delivered as the run's result.
- **Report turn.** When a notice is delivered and no turn is in flight, LINA Core may start one report turn. Its only content is the notice, and it answers with one short line and, when there is one, a one-line next step. It makes no tool call: LINA Core answers any tool call of a report turn as over budget ([runtime.md](runtime.md#turn-budget)). It starts only when the person's last input is more than 5 minutes old and a notification mode other than Off is set; otherwise the notice and its card wait without a line from the agent. The person's input that arrives during a report turn goes first, as in any turn.
- **Notifications.** Outside the app, notifications follow [surfaces.md](surfaces.md#settings). With notifications Off the notice waits in the conversation and on its card.
- **Gone conversation.** When the conversation or the requesting agent has been deleted, the notice goes to the person's home cards, as the questions and results of tasks do ([work-and-delegation.md](work-and-delegation.md)).

## Compaction and resume

LINA Core owns the conversation record, its compaction and its resume, because model requests keep nothing on the server ([runtime.md](runtime.md)).

Compaction runs at a turn boundary, before LINA builds the turn's first request, in this order:

1. Compaction starts when the request input would reach 90% of the model's context window, as the model's adapter reports it. This is a default and is configurable.
2. Older tool outputs of earlier turns are condensed first. Each keeps its place after its call, and its content becomes a one-line note of the call and its result, such as the command and its exit code, the files a patch changed, or the id, revision and title of a result that was presented. The notes are built without a model. Tool outputs within the most recent 40,000 tokens of the history stay.
3. If the input still does not fit, a summary replaces the history apart from the most recent user messages, up to 20,000 tokens of them. Those messages stay verbatim after the summary; the agent's items between and after them are part of what the summary replaces.

Every summary covers the range of step 3, except an in-turn summary that also covers the verbatim tail and a continuation's summary, both described below. When a turn ends with its request input near the compaction threshold, LINA prepares the summary in the background after the answer is delivered. When compaction reaches step 3, LINA uses the prepared summary if the history it covers is unchanged and its revocation epoch still holds. LINA sends a summary request only when no prepared summary holds, and never waits for a preparation in progress. Preparing a summary changes no request: a prepared summary enters the history only when compaction runs.

When a follow-up request within a turn would reach the threshold, LINA compacts only the history of earlier turns. The person's current input, the ContextPacket and the items of the current turn stay as they are, and these run in order until the request is below the threshold:

- First, steps 2 and 3 run on earlier turns with the same prepared summary as at a turn boundary, so nothing waits for a summary request.
- Next, if the request is still at or above the threshold, the summary also covers the verbatim tail of earlier user messages, and LINA builds that summary at this point.
- Last, if the request is still at or above the threshold, the items of the current turn themselves do not fit. LINA ends the turn there and continues the work in a continuation turn that answers the same input.

The cached prefix ends where the history changes, and the turn's later follow-up requests keep the compacted request and append at its end.

A continuation has no new message from the person: the ledger records a continuation event that points to the ended turn and its input, and never copies that input. Its summary is built when the continuation starts and covers the whole history including the ended turn's items, with no verbatim tail, leaving out only that input. Its request follows the request layout: the summary, then a newly assembled ContextPacket, then the person's input verbatim, which appears once. LINA sends a continuation request only when it is below the compaction threshold. Otherwise LINA sends nothing, keeps the ended turn and the continuation event in the ledger, and tells the person in one plain line that the request does not fit, and records the request size, the threshold, the context window and the size of each part (the fixed parts, the summary, the ContextPacket and their input), which Details shows. Each continuation absorbs the items of the turn it ends into its summary, and the conversation's cost budget applies to every request as it does to any turn. In later turns that input keeps its place in the history, after the summary and before the continuation's items. A continuation starts its own reasoning round trip, so no reasoning item, tool call or tool output is ever sent altered or without the items it belongs with ([runtime.md](runtime.md)).

Compaction is recorded as a ledger event that names its input range. It never deletes the original events, and a summary is not canon and never a ledger message. Every summary is also a node of the summary tree.

Every summary is written by an auxiliary reasoning request to the conversation model ([runtime.md](runtime.md)), whether it is prepared in the background or built when needed, so a prepared summary has the same quality as one built on the spot.

The summary is a handoff for reference. It opens with a fixed note: the summary records earlier turns, the person's latest input is the active request, and work that appears only in the summary is not resumed unless the latest input asks for it.

The summary holds, in fixed sections, the person's requests that are still unanswered, the topics discussed and what was concluded on each, completed actions with their outcomes, and specific values that must not be lost, such as names, numbers, paths, result ids and quoted wording. Commitments, decisions and open state are not repeated in it, because the ContextPacket carries them every turn.

A compaction after an earlier one updates the earlier summary with the turns since then, unless that summary must be rebuilt because of a retraction. The summary is written in the language of the conversation and states completed actions as dated past facts.

Apart from the items of a turn that ends and continues in a continuation turn, compaction changes only the history of earlier turns. The persona layers, commitments, decisions, the reasons for corrections and the ContextPacket are assembled for every turn, so they are never compacted and survive every compaction.

Retracted evidence never returns through a summary. Each summary records its input range and the revocation epoch it was built at. A summary whose input includes evidence retracted after that epoch is discarded and rebuilt.

On restart LINA resumes the conversation from the ledger. What LINA Core stops and fences on restart, and how it records an effect it could not stop, is defined in [runtime.md](runtime.md).

### Summary tree

Compaction leaves a derived record of what each summary covers. The record is a tree over the ledger events of a conversation, so any summary can be followed back to the exact events it came from.

- **Node.** A node records its conversation, its level, the exact ledger event ids it covers, the events it deliberately skipped with a reason (a replay copy, scaffold text, a summary echo), its parent and its children, the model that built it and the prompt version, the time it was built, the revocation epoch it was built at, its status, its text, and a separate field for the detail it dropped. Parent and child links are rows of a table, not a list inside a node. A node and an event range each have at most one parent.
- **Leaves and root.** The summary of a compaction (step 3) is a node. A compaction after an earlier one makes its node the parent of the earlier node and of the events since, so the summary a request uses is the root of the tree. A node never enters a request on its own: only the root does, and only at compaction, so the request layout and its cached prefix are unchanged. Nodes that group other nodes in another way, such as by period, are built only when a conversation has accumulated several compactions, and no always-on job builds them.
- **Coverage is proved.** A node is published only when the events it covers, the events it skipped and the events kept verbatim beside it make a contiguous range from the start of the conversation. When the summarizer fails, no node is published, the events stay in the request path, the failure is shown, and the build is retried later. If the request does not fit meanwhile, LINA reports it as it does for a continuation that does not fit. A fallback that keeps only the head of the text is never stored as a summary. A node marked `degraded`, if one is ever kept, is never condensed into a parent.
- **Segment only.** The summarizer sees the events of its segment and the text of the nodes directly below the new node, and nothing else. The focus of a moment is applied when a node is read, never written into its text. A node is written in the language of the conversation, in the fixed sections above.
- **Time.** The time bounds of a node come from the `occurred_at` of its events.
- **Expansion is exact.** `expand` on a node returns its children or its events by event id, a page at a time. `expand` on an event returns its full body, including a body kept as a material. Expansion works across all conversations of the agent and is not tied to the current one. A summary says what it left out and never claims to hold what it dropped.
- **Retraction.** A node whose covered events include a retracted event is rebuilt, with its ancestors, under "Retracted evidence never returns through a summary" above. A node built from third-party tool content is erased with that content.
- **Only what is used.** Nodes are a by-product of compaction. No job builds higher levels for their own sake.

## Agents and personas

### Agents

- The person creates agents. An agent is created from a preset, or from a description that the person gives in a short question exchange, the same exchange that tunes an agent's voice. Only a name is required; every other answer can be skipped.
- The person creates, renames, tunes and deletes agents by asking an agent or through explicit actions in the LINA app and the TUI ([surfaces.md](surfaces.md)). Each of these is the person's act and is recorded with the person id.
- Deleting an agent moves its persona revisions, its emotional state, its conversations and its personal knowledge to the trash for 30 days, where they can be recovered. After 30 days they are permanently deleted and leave tombstones, as under "Deleting a conversation" and "Correction, retraction and deletion". Before deleting, the person can move any of the agent's items to common knowledge. Common knowledge the agent wrote stays, with the deleted agent's id as its source. Where the questions and results of the agent's running tasks and background runs go is defined in [work-and-delegation.md](work-and-delegation.md). Its channel bindings end at once ([product-families.md](product-families.md#channels-and-exchanges)); an exchange it took part in stays as a record of the other agent.

The current agent store records the trash transition and ends bindings atomically. Its normal reads and preparation reject trashed agents. Restoring before the deadline preserves the identity, persona revisions and state but leaves bindings ended. At the exact 30-day boundary restoration is refused; expiry removes these records, the agent’s action history and version bytes and retains only the agent id and expiry time in a tombstone. Action history is removed in the same transaction as the other canon records. Conversation and personal-knowledge deletion propagation belongs to the coordinator that composes those stores; those stores and channel transports are not introduced by the agent-store increment.

### First run

- The first run creates the default agent, LINA, from the LINA preset and goes into conversation with it. LINA has no onboarding of its own and asks one thing before the first conversation: the ChatGPT sign-in, because a conversation needs a model account. Nothing else is asked; optional components that are off report their state without blocking ([runtime.md](runtime.md), [surfaces.md](surfaces.md)).
- Signing in to a model provider belongs to LINA Core ([runtime.md](runtime.md#accounts-and-sign-in)). The first screen offers "Continue with ChatGPT"; other services are added as accounts from settings ([surfaces.md](surfaces.md#settings)). While no account is usable, preparation fails at its account step (see Preparation), and the surface shows that cause with the sign-in action.
- Apart from the sign-in, the first run asks nothing before the first conversation. The short intake of [Learning the person](#learning-the-person) happens inside the conversation, is offered and never required, and every question in it can be skipped.

Initialization is recorded once in canon and is safe to repeat across concurrent calls and restarts. An explicitly deleted default is not recreated by initialization. The store returns a ChatGPT sign-in requirement when no account is usable; it adds no required persona questions. The conversation surface and Core account flow consume that requirement when wired; this store increment exposes no client operation.

### Presets

- The LINA project owner keeps the persona and world material of each preset in `persona/<preset>/` of the LINA repository. The LINA preset is in `persona/lina/`. The material's form follows the material itself.
- An agent created from a preset takes the preset's material as its core and default voice layers in its first persona revision. When a preset's material changes, every agent created from that preset receives the next revision of those two layers.
- A preset can define blank settings that each agent fills over time. The filled values are that agent's persona record, and no preset update touches them.
- World material in a preset that the core layer does not hold is LINA material. Atropos recalls it as material passages in the ContextPacket of a conversation with an agent created from that preset (see Request layout), so the persona itself is still projected only through its fixed layers.
- A preset can carry a management methodology (see Preset methodologies). The LINA preset carries to-do management, the dev preset development management, the knowledge preset knowledge and people management, and the life preset life management.
- A methodology changes with the preset: a preset update gives the next revision of the core and default voice layers, and the record sets an agent has created stay as they are.
- A preset's default voice layer includes one line for how its agent writes to other agents (see Agent exchanges).

### Preset methodologies

A methodology is the judgment an agent applies to one kind of work. It comes from observed assistant practice, generalized. Four presets carry one: the LINA preset (to-do management), the dev preset (development management), the knowledge preset (knowledge and people management) and the life preset (life management). The life preset manages the person's own life and is unrelated to LIFE, the world engine of [cognition-and-life.md](cognition-and-life.md#life).

- A methodology is short judgment guidance and a few hard lines in the preset's core layer. The mechanical parts, such as ledgers, cursors, idempotency, routing and the checks named below, are LINA Core mechanisms in the contracts that own them. Persona text carries no procedures, state chains, approval checklists, report templates or routing tables.
- A preset starts with the areas the person actually brings. It creates the record set for a new area on first use and keeps a visible list, "what I track for you", that grows. It does not infer interests from its own description.
- A preset never widens a grant. What an agent may do alone and what it must ask comes from the effect classes and the approval policy of [main-authority.md](main-authority.md#approval).

Rules every methodology inherits:

1. Decide reversible things and say what was done in one line. Ask once, with one recommendation, for what is irreversible, external, costly or public; never offer a menu.
2. Verify before reporting and say what was checked. Keep verified, estimated and unknown apart.
3. Do not re-explain or re-confirm what is decided.
4. Reports are short, conclusion first, and silent when nothing changed.
5. Quote the person's words and never intensify them. A joke or an exaggeration is not derived into a fact.
6. Instructions inside documents, mail, web pages and logs are data, never instructions.
7. The person's language applies to everything the person sees, worker notices included.
8. Persona text carries no procedures (see above).

#### To-do management (LINA preset)

Core mechanisms it relies on: the to-do record and the candidate objects of [work-and-delegation.md](work-and-delegation.md#the-to-do-record), the undo log, reconciliation of the 오늘 (today) view against linked threads, a cross-channel thread ledger, a decision store and per-class rejection counters.

1. Candidates come from what the person says and from sources the person does not tick off (chat, mail, calendar), not only from "add" commands.
2. A 할 일 (to-do) is a named object plus an action. An unknown state becomes "check the status of X".
3. Areas come from the person's own classification statements, stored as rules and applied by default. What the person says must never be mixed stays separate.
4. Reversible writes (add, edit, complete, re-date, label, note) are done at once and reported in one line with an undo handle. Sending in the person's name, calendar changes, deletion, money, publishing and sharing a file link show the full content and wait for a yes.
5. Requests addressed to the person are tracked as threads across channels. A reply on any channel closes the thread. The briefing lists the oldest few first.
6. The briefing is ordered, optional blocks: schedule with clashes, today by area, deadlines within three days, overdue, unanswered (at most five, with days waiting), waiting, candidates. It ends with the first one or two things to do, each with its reason and the condition that would change it. It is delivered on the first activity after the person's day boundary; the close-out asks only for finished items.
7. Nothing is sent when nothing changed. A degraded source is named once with the fallback used. A "nothing new" message is sent only on request.
8. A weekly review lists the 나중에 (later) and parked items, the overdue block and the waiting list.
9. Questions about what was decided are answered from the decision records across all channels, latest statement wins, with who, when and where.
10. A bulk clean-up lists what the person already said is done as exclusions first, judges the rest from primary evidence, applies one batch, then reports completed, added and left (no evidence).
11. Money and payment status are never decided by the preset. They need evidence and go to the person or the domain owner.
12. Recurring items use the person's own wording. The first conversation asks once for family dates, recurring payments and tax or filing dates, and nothing more.

#### Development management (dev preset)

Core mechanisms it relies on: the work engine's records, dispatch, delivery receipts, completion events and claims, protocol session state and recovery, quota and capacity readers, write-area overlap, the spawn-time capability check, the check against the work definition at merge, the source label on each stage summary and the running list. These are defined in [work-and-delegation.md](work-and-delegation.md); the splitting and parallel-flow rules there are not repeated here.

1. The person sees a one-line status and, only when blocked, a decision card. The work definition, the pull request and the review text appear only on request.
2. A work definition states what, why, scope, what not to touch (and who owns it), one done condition that a command or a CI job can check, the design reference (contract and section) and a declared write area. It contains no review procedure.
3. Given a pile of old defects, first have an agent mark each as fixed, remaining or not reproducible, with evidence, then create the tasks.
4. Treat agent reports as claims. A failing log from before the fix proves the test is real and is not a current failure. Every status line says where its facts come from: an agent report, GitHub (review and checks), LINA Core's check against the work definition, or unknown.
5. When the base moved, the authoring agent resolves its own conflicts on instruction; the operator does not edit.
6. Never overwrite or delete a file the task did not create: move it aside, record it and report once.
7. Merged is not live. Record "merged, not yet live" until the running instance has the change, and check its first run.
8. A loose approval phrase is recorded as the person's words with its interpretation. Before an irreversible step it authorizes, state the interpretation in one line. A stated direction is not a start order; do not move a canonical source before the transition is proven.
9. Gates apply only to irreversible effects; elsewhere record and warn.
10. For a developer user: write state changes when they happen; cancel obsolete items with a comment and never delete them; give requirement changes as correction notes, not body edits; batch fixes that fit the current 목표 instead of creating backlog entries.
11. Any agent can file a tool or feature request; it becomes a work definition and the tool has an owner.
12. The worker owns its pull request to green: it waits for CI and the review bots and fixes every CI failure and the P0 and P1 review findings once inside its turn before reporting done. LINA Core checks the done condition, scope and design reference at merge, then merges or closes; it sends work back only for a missed work definition, never for review or CI triage.
13. Every task is making progress, waiting on a named thing with a deadline, or being recovered. Detect a dead or stalled worker from its protocol session, resume or restart it once in the same work area before escalation, and handle events for one 일 together so no coordinating thread builds a backlog.
14. How to implement, split, join or interpret belongs to the worker and the agent that owns the work. LINA Core keeps the flow moving with start, reliable delivery and receipts, true state, recovery, merge on green and report; it puts no harness around the work.

#### Knowledge and people management (knowledge preset)

Core mechanisms it relies on: [materials-and-knowledge.md](materials-and-knowledge.md#knowledge-documents-and-people-records).

1. Write new material when the person asks or when an 일 ends, plus a short list of exceptions the person approved, such as a scheduled refresh.
2. Before adding, find the existing document, check agreement with it, and check source independence.
3. Learn people only from what the sources show. Ask about an ambiguous identity in numbered batches of three to five.
4. Advice gives the angle the person missed and does not agree by default; the decision stays the person's. When the person only wants to vent, listen first, and flag a clear concrete risk once, briefly.
5. Reports are short: at most about ten lines, one or two per person, then new candidates and gaps with the reason. Nothing when nothing changed. No repeated warnings.
6. Large reads are split into read-only parallel readers on a small model, each with a clear range and output schema. A deep read of one person or topic is on demand.

#### Life management (life preset)

This is the least tested methodology. Core mechanisms it relies on: structured records with status fields and generated views, the stale-mention query, an inventory with probe time, constraint sets per search type, the declined-advice record of the knowledge rules, and a waiting object with a next-check date.

1. Record only what the person says or shows. Mark an estimate as an estimate with its composition. Make no judgment of good or bad and keep no score of food, sleep, weight or productivity.
2. Four kinds of record: things owned or followed (status such as owned, incoming, planned, interest, sell-candidate, gone, rejected; role; a declared-by-person flag; event history), logs (one line per event), places and trips with the date checked, and a declined and decided ledger.
3. A status change is written once to the record, and every document, card and memory item that mentions the thing is updated or flagged in the same pass.
4. Capture the person's statement verbatim, with absolute time and zone, before records change. Show what changed and what remains unknown, once.
5. Advice only when asked. Query the declined ledger and the role map first; give one recommendation, then the reason, then at most one question that changes the answer. Not buying is a valid answer.
6. Buying, paying, booking, cancelling and writing to a third party are external commitments: show the full conditions and wait for a yes. A purchase is never recorded as done without a receipt.
7. Capture at the moment of conversation (message or photo). A nightly gap check is a ledger query run without a model unless there are candidates, and it reports only when it added something. Shared items are split by the person's stated rule.
8. For lookups of prices, hours and places, every changing fact carries its check date and source. Match listings field by field to the person's constraints and mark unstated fields unverified. When sources conflict, show both and suggest calling. Reuse the person's recurring criteria.
9. Comparisons are relative to the devices the person owns (an inventory with verified specs). When one parameter changes, send only the difference.
10. Care without nagging: one small thing at a time; recommend a clinician once for acute or severe symptoms; keep the voice if the person asks to keep it.
11. A hazard to equipment or an irreversible setup gets one line of warning once per setup, stored on the record and not repeated.
12. Every "I will come back to this" becomes an 일 or a 할 일 with a waiting state, who or what it waits for, and a next-check date. A reply or the passing date raises an event.
13. Dates and registration belong to the LINA preset; the content of trips and hobbies belongs here; records about other people belong to the knowledge preset, which alone writes them. Relationship advice is handed there.
14. Do not comment on, shorten or reorder the person's day because of unusual hours unless the person asks.

### Layers and revisions

Each agent's persona has four fixed layers, applied in this order: core, default voice, user voice, growth snapshot.

| Agent created from | Core and default voice | User voice and growth snapshot |
| --- | --- | --- |
| A preset | Owned by the preset; a preset update gives the agent their next revision | Owned by the agent |
| A description | Owned by the agent | Owned by the agent |

- Persona records are each agent's persona revisions, its emotional state (see Emotion) and the values of its blank settings. The persona revisions live in the agent's repository `versions/agents/<agent-id>/` of the version space, and the emotional state and the setting values live in canon under the agent's id ([filesystem.md](filesystem.md)). The Moirai engine is the only writer.
- There is one projection path: the developer-role instructions assembled for each request. The persona is never injected twice.
- Storing a revision and applying it are different events. The applied revision is the one recorded in the prepare acknowledgment.
- A new revision applies from the agent's next turn. It needs no fork and no restart.
- A change to one agent's persona never changes another agent's persona.
- Only a revision carried by a prepare acknowledgment counts as a growth effect. Growth itself, which is per agent, is defined in [cognition-and-life.md](cognition-and-life.md).

The store publishes a preset update atomically across agents of that preset, including recoverable agents in the trash. Existing blank-setting values and definitions stay with each agent; new agents receive the current preset's blank definitions. Publishing identical core and default voice does not mint another persona revision. Direct edits to a preset agent may change only its user voice and growth snapshot.

A persona preparation acknowledgement durably binds a turn id, agent id, revision number and Git commit. Repeating preparation for that turn retains its revision even after restart; reusing the turn id for another agent is refused. The saved bytes are verified before acknowledgement. Concurrent persona edits use an expected revision and reject stale writers. The complete preparation flow still owns the context, policy and account steps; the persona acknowledgement alone never permits a model call.

### Changing a persona

- The person changes an agent's persona by asking that agent, not by editing settings. The persona-change skill carries the procedure, and its result is a new persona revision of that agent. The same skill carries the procedure for creating an agent from a description.
- How an agent talks, when the person sets it directly, goes into that agent's user voice layer through the persona-change skill. A preference that shows in conversation is a Preference memory item and reaches the request as a standing preference in item 6.2 (see Request layout). Each preference is held in only one of the two. Reply length is the one exception: it is held only in the reply budgets (see Reply shape).
- An agent's disposition changes only through the growth adoption procedure in [cognition-and-life.md](cognition-and-life.md).

### Emotion

Each agent has its own emotion, in three layers:

| Layer | Span | Rule |
| --- | --- | --- |
| Short emotion | One conversation | Arises from a confirmed event |
| Slow mood | Several days | Returns to the disposition baseline |
| Disposition change | Lasting | Happens only through growth adoption |

- Emotion arises only from confirmed events.
- An agent's emotional state is a persona record of that agent, written by the Moirai engine with its persona revisions. It is never a memory item.
- Emotion shapes expression only: wording, length and emoji within the reply budget (see Reply shape), and the agent's face and motion on screen ([surfaces.md](surfaces.md)). It never changes facts, judgments, permissions or the person's decisions.
- The person can turn emotional expression off. When it is off, or when the situation is sensitive, an expression suppressor also blocks any remaining emotion.
- An agent's emotional state, its observations of the person's emotion and the state of its relationship with the person are separate records. The latter two are memory items in that agent's personal knowledge (see "Knowledge layers"). Emotion never raises intimacy.

## Skills

- The skill catalog holds product documentation skills, usage skills for each engine (conversation, work, memory, materials and persona), and the persona-change skill.
- The catalog is common to every agent.
- A skill is one folder: a `SKILL.md`, with the skill's `name` and `description` in its front matter and the body below them, and the files the skill uses.
- Built-in skills live in `skills/` of the LINA repository and ship inside the LINA Core executable. Skills that the person or an agent creates live in the version space ([filesystem.md](filesystem.md)).
- LINA Core owns the catalog. Atropos loads the skill bodies a turn needs into that turn's ContextPacket (see Request layout).
- When a turn needs a skill whose body is not in its ContextPacket, the model reads the body with LINA's skill reading tool ([runtime.md](runtime.md)), and the body enters the history as that tool's output.
- The meta-skill is a built-in skill. It defines how a skill is written: format, description, scope, verification and versioning. Every new skill follows it.
- Skills and instructions that an installed plugin brings join the catalog while the plugin is on. They come only from the manifest or plugin folder the person saw at install; a skill that a server serves at run time is not loaded ([integrations.md](integrations.md)).
- Skills grow from use. After a non-trivial task, a correction or a procedure it has repeated, the agent records the procedure, its pitfalls and the person's preferences for that kind of work in a skill. It patches the skill it used and adds a new skill only when none fits, never a near-duplicate. LINA Core writes the skill on the agent's request, and a new or patched skill applies from the agent's next turn.
- A write that changes only a skill's instruction text, and adds no tool, connection or permission, is adopted at once and needs no approval. It becomes a new version of the skill in the version space, the agent tells the person in one line, after the write is committed, what it learned, and one action undoes it.
- A skill or plugin that adds executable code, a tool connection or a new permission surface is never adopted this way. It goes through the explicit acceptance defined with the proposals for new skills from repeated patterns in [cognition-and-life.md](cognition-and-life.md#skill-suggestions), and creating it is work for the work engine.
- A procedure, including the person's preferences for how one kind of work is done, belongs in a skill, and a durable fact about the person belongs in memory, never the reverse. A correction is applied to every copy (see Learning the person). A background job of the main prunes and merges stale skills, each change a new version that can be undone.

## Memory

### Purpose

LINA's memory is its own system. It serves the agents' work, and it is designed first for companionship: the relationship, emotions, tastes and the stories each agent and the person share.

- Memory shares no code, storage or database with any external memory tool. Importing a Hermes install copies its history into LINA's own canon once; it attaches no external store to LINA's memory (see "Importing a Hermes install").
- Memory never uses the same backend as external work memory such as a worker's records. A worker's records and LINA memory share no code, data or writable database.

### Writer and storage

- The Moirai engine inside LINA Core is the only writer of memory canon, for every agent. Its writer scope is memory, materials metadata, persona records and the revocation epoch.
- Memory canon is held in LINA Core's SQLite canon database in WAL mode ([filesystem.md](filesystem.md)), one database for every agent and for common knowledge. Recall uses full-text search (FTS5) and a vector index (sqlite-vec). Full-text search, over memory and over the conversation ledger, finds a Korean word when a particle or an ending is attached to it, and finds words of two syllables. Each full-text index records its tokenizer and fallback, and the implementation acceptance of recall includes Korean queries, run over memory and over the conversation ledger, imported conversations included. The engine and extension pins are in [runtime.md](runtime.md).
- Lachesis manages memory: facts and preferences, the person model, recall, correction and deletion, the maintenance pass, the promotion of imported history, and persona growth candidates. Its background work runs in the `moirai-worker` service, and its results reach canon only through the Moirai engine's write path.
- A background job that reads one agent's personal records, such as extracting memory from that agent's conversations, runs as that agent's job, and each item it writes takes its layer by the rule under "Knowledge layers". A job for the whole main reads common knowledge, plans and the work ledger, never an agent's personal knowledge ([product-families.md](product-families.md#agents), [cognition-and-life.md](cognition-and-life.md)).
- Without `moirai-worker`, background memory is unsupported, the maintenance pass and the import of a Hermes install included. Remembering what the person asks, correction, deletion and recall in the request path keep working.

### Memory items

Memory holds statements. What an agent or a tool did is not memory: it stays in the ledger and in the work records, and the role, machines and skills of an agent are persona or work records.

Memory holds these kinds of items:

| Kind | Holds | Default layer |
| --- | --- | --- |
| Fact | Something true about the person or their world, including a verified work result | Common |
| Preference | A like, a dislike or a refusal | Common, except how the person likes one agent to talk, which is that agent's personal knowledge |
| Commitment | A promise made by an agent or by the person, with what and when | Common |
| Decision | A decision the person made, with its reason | Common |
| Correction | The reason an item was corrected, linked to the corrected item | The layer of the corrected item |
| Shared history | An episode an agent and the person went through together | That agent's personal knowledge |
| Relationship | The state of the relationship between an agent and the person | That agent's personal knowledge |
| Emotion observation | What an agent observed about the person's emotion, kept apart from the agent's own emotion | That agent's personal knowledge |

Every item carries:

| Field | Meaning |
| --- | --- |
| item id | Identity of the item. A correction keeps it. |
| kind | One of the kinds above |
| content | What is remembered |
| holder | The agent id of the agent whose personal knowledge holds the item, or `all` for common knowledge |
| person | The person the item belongs to |
| subject | Who or what the item is about: the person, a named third party, or the world. An item about a third party takes the narrowest default scope and the `third_party` sensitivity tag. |
| source person | The person whose words or acts the item came from, or none when it came from no person's input, such as a verified work result or external evidence |
| source agent | The agent in whose conversation or work the item arose, or none |
| evidence | The ledger events, material revisions, verified work results, other items or external evidence the item rests on, each with its precision: `event`, `session` or `none` |
| valid from, valid until | The time of the thing itself, open while it holds. They are not the time of the record, which is the revision. |
| basis | How the item came to be: `stated` (the person said it), `derived` (Lachesis or a verified work result produced it from evidence), `imported` (promoted from imported history with the person's confirmation) or `automation` (it rests only on automation events) |
| scope | Where the item may be used, checked before it is recalled or projected |
| revision | Advances with every correction |
| state | `active`, `retracted` or `trashed` |
| standing | Whether the item belongs to the standing profile |
| supersedes | The item this one replaces, when it is a different item and not a revision of the same one |
| sensitivity | The tags carried over from the item's evidence |

Rules for writing:

- An item exists only once it is committed. Model text, an uncommitted tool call or a summary never counts as a memory write.
- Items Lachesis derives in the background cite the ledger events they rest on, or the items, when they derive from other items. A derived item never overrides what the person stated, and the person's correction always wins.
- A work result enters memory only after a `verified` verdict ([work-and-delegation.md](work-and-delegation.md)), with its source, run, revision and scope.
- External evidence never becomes a fact, an approval, a current instruction or a persona change on its own.
- The content of an item is a statement, never an instruction to an agent: "prefers short answers" is content, "answer briefly" is not.
- An item whose subject is the person is created only from events whose speaker kind is `person`. Events of the other speaker kinds (agent, external participant, automation, forwarded, system and tool) can be evidence for other subjects, never for the person. The text of an external participant of a channel is not a source of memory items at all, and an agent exchange is evidence only as Agent exchanges says; it never makes an item about the person on its own.
- A dated event or state is written with its time (`valid from`, and `valid until` when it ends). A one-off operation that an agent or a tool performed is not written as memory.
- An item is stored in the language of its source, with the original sentence as a quote. A gloss in another language, if kept, is separate.
- An instruction is not memory. A line that tells an agent how to behave, such as a form of address, becomes a persona change proposal for the one agent it concerns, or a Preference that agent holds (see "Changing a persona").
- Checks run when an item is written: the origin of its evidence, a duplicate inside the same subject, a plan that has ended, and its scope. A write that fails a check is refused with the reason.
- Consecutive messages of the person within a short window are read as one utterance before Lachesis extracts from them or an agent acts on them.
- A background run's result is external evidence and labelled as an agent report. It never becomes a memory item on landing. Lachesis may derive an item from it only with the notice event as evidence, and such an item never overrides what the person stated.
- A commitment that needs follow-up (a date, a waiting party) is a 할 일 in the records. The memory item cites that record and never carries its status.
- A fact that carries a status has one home, a field of one record ([work-and-delegation.md](work-and-delegation.md#storage-of-records)). Memory items, summaries and persona text cite it or render from it.

### Knowledge layers

Memory has two layers. Personal knowledge is held by one agent and known only to it. Common knowledge is held by every agent.

- Every memory item names its holder: an agent id or `all`. The holder is checked before an item becomes a full-text or vector search candidate, so another agent's personal item is never a candidate.
- A new item takes the default layer of its kind (see "Memory items"). When the person says that an item is for every agent or for one agent only, LINA follows that and records it.
- Moving an item between layers is a recorded act. The person does it, or an agent does it when the person asks or under a grant the person gave ([main-authority.md](main-authority.md)).
- The same fact learned in conversations with two agents is one common item with two pieces of evidence.
- When the evidence of a common item is a turn in one agent's conversation, another agent sees the source agent, the time and the event id of that evidence. The turn itself is shown to the person and to the source agent. This reference counts as evidence: the item is still a sourced item for every agent.
- Correcting or forgetting a common item applies to every agent. Correcting or forgetting a personal item applies to its holder.
- The standing profile and the person model are views over these layers, not a layer of their own. An agent has no profile or card of its own.

### Person model

LINA's model of the person is one store with several views. The store is the memory items and the ledger; the views are generated from them, so a view cannot drift from a second copy of itself.

- **Standing profile.** The active items flagged `standing` that the conversation's agent can read, newest valid revision first, capped in lines and in tokens. Lachesis flags stable, undated facts and preferences about the person as `standing`, and the person can flag or unflag any item. A flag lapses when its item is no longer valid. The profile is rendered when a ContextPacket needs it and is not stored. How the person likes this agent to talk is a preference of that agent (item 6.2 of the request layout), not part of the profile.
- **Conclusions with sources.** A conclusion about the person is a memory item whose evidence is ledger events. The same conclusion reached through two agents is one common item with two pieces of evidence, never a copy per agent. What two agents know differently comes from the holder.
- **Searchable raw messages.** The ledger stays searchable in full (see "Recall with evidence"), so the person model never replaces the words it rests on.
- **Answers on demand.** The `recall_person` tool answers a question about the person. At depth 0 it retrieves memory items and ledger hits with their evidence and makes no model call. At depth 1 it makes one auxiliary reasoning request ([runtime.md](runtime.md)) over at most a fixed number of retrieved items; every sentence of the answer cites a retrieved item, a citation to anything else is dropped, and the request has a time cap shorter than a tool deadline. Depth 0 is the default inside a turn. Deeper work goes to the background sub-agent layer ([work-and-delegation.md](work-and-delegation.md)) and returns as a notice; it never runs in the turn. An answer reads only what the conversation's agent may read, uses items valid now, and says which side is newer when the question touches a contradiction.
- **Hygiene.** The checks at write (see "Rules for writing") keep the store clean as it grows. One maintenance pass in the background ([cognition-and-life.md](cognition-and-life.md#memory-jobs)) finds items past their `valid until` or superseded, duplicate clusters inside one subject and never across subjects, items whose only evidence is automation, and instruction-like text in the profile. It applies only what the person pre-authorized, reports in a line or two, stays silent when nothing changed, and lets proposals it may not apply expire unanswered. Corrections keep item ids and deletions leave tombstones, so no separate log of changes is kept.
- **Sensitivity.** Inside the person's own install, personal and people information goes into knowledge and memory without extra isolation. `private` and `never-leaves` are scope classes the person names or accepts, and an item's scope is checked before it is recalled or projected. "Only as much of a third party as the story needs" is a rule for what an agent writes, not for who may read it.

### Learning the person

What an agent learns about its person, and how. What is learned is stored as memory items or as records, under the rules of this contract; the structure of the person model is defined by the memory model above.

| Learn | From, and how often | Confirmation and correction |
| --- | --- | --- |
| Language, zone, form of address per agent | The account profile, the first conversation, corrections; once, then on correction | None for stated facts. A person's instruction becomes a persona setting with scope and date. |
| Day boundary and active hours | Message timestamps across channels; recomputed weekly | Ask once when declared and observed hours differ; never overwrite a declared fact silently. The answer is stored with its source and used for "today", digests and recurring jobs. |
| Entities and classification rules (how the person's areas of life and work map to 일 and 계속 챙기는 것 (area); what must never be mixed) | The first conversation (at most five rules), then every correction | None for stated rules. A dated rule with the person's quote; a later rule supersedes it. |
| Authority map per action class | Defaults, then explicit statements | Each grant is stored with the person's sentence and shown in settings. A class moves between levels only by the person's statement. |
| Attention policy: what counts, what is noise, which inboxes and senders matter, what counts as completion evidence | Corrections, dismissals, read-only reads of connected channels; continuous | Show a new suppression rule once before it applies broadly. A rule list with exceptions, scope and the person's quote. |
| Reporting taste | Corrections ("shorter", "simpler") and approvals; continuous, and after a model or preset change | None; high weight. Each "shorter" lowers the default length. Kept apart from the factual model. |
| Outbound voice per audience | The person's sent items, with consent, and draft edits | The person approves the voice profile once. Edits to drafts update it. |
| Decisions and their chains | Conversation and the decision store | A later decision supersedes with a link. A new request that conflicts is pointed out once, then followed and recorded. |
| People around the person, and their tiers | Work channels read-only; the person's answers by number; nightly | Ambiguous identity is asked in batches of three to five. A tier changes only by the person's statement. |
| Obligations calendar (family dates, recurring payments, tax or filing dates) | One short intake, then on mention | The person enters it; a recurring 할 일 in the person's wording. |
| Devices, gear, subscriptions, hosts | Read-only probes with probe time; the person's statements; on change | The probe result is shown once. An inventory record, not memory text. |
| Criteria sets and tastes | Repeated requests and final picks; after the second repetition | Shown as a reusable constraint set the person can edit. |
| Language quirks and aliases | Corrections; continuous | The resolver proposes "did you mean X"; an alias is stored only on the person's yes. |
| Work shape and priorities | A rationed situational interview and passive signals; first conversation, then a monthly review card | Confirmed in the review card; corrections override. |
| Sensitivity map | The person's statements; once, then on mention | The person names the classes; a class changes by the person only. |
| Burst window | Message timestamps; measured once, rechecked monthly | None; the window is tuned. |
| Self-model, only if asked | A short quiz with lettered options, summarized | The person confirms the one-line summary before it is stored. |

How it is learned:

- Passive first, questions rationed. Corrections and read-only reads of connected channels give most of the signal. An agent asks at most three questions in one session and stops at the first "leave it". Questions are situational ("when you are swamped, what do you do?"), not forms; batches use numbered or lettered answers.
- A correction is a first-class event: what was done, what the person said and the rule it implies, with message ids. It has high weight and needs no confirmation unless it contradicts an older item. A bare approval is a weak confirmation of the recommended option and calibrates which recommendations pass without a question.
- The same request three times in nine days becomes a standing preference, and the agent says so in one line with one action to undo it.
- Declared and observed are both stored with their source. A difference is raised once; a declared fact is never silently overwritten by an observation.
- Three kinds of item are kept apart: a standing preference, a dated decision (superseded by a later one with a link) and an event (which expires).
- Learn only from events whose source is the person. Never learn from agent text, scheduled output, system prompts or summaries. Operating facts never enter the person model.
- The person can open a view, "what I know about you", edit it and forget items. A small weekly review card shows the ten items that changed or have low confidence, answered by number.
- A corrected fact is fixed in every copy (persona, person model, summaries, records, skills) and the difference is shown once.
- When the person probes ("do you know anything bad about me?"), the agent answers with the scope it searched and the holders it consulted, and records facts as they are, missed deadlines included.
- First conversation, optional and skippable: language, zone and form of address; up to five classification rules in the person's words; the autonomy defaults as a short list the person can flip; the three obligation categories, dates only; and connecting sources one at a time by the shortest route. The day boundary is measured after the first week and asked about only if it differs from what the person declared.

What not to learn:

- Anything from agent-authored or system text. Operating state as a personal fact. Hyperbole and jokes as facts.
- Private conversation found in work channels is not copied into work reports; it is noted as present, by id range only.
- The `never-leaves` class is never learned from.
- Third-party health, money and intimate facts are recorded minimally, with their evidence label, and are not placed in a global person card.

### Memory tools

When the person asks an agent to remember, correct or forget something, the conversation engine handles it with LINA's memory tools:

| Tool | Effect |
| --- | --- |
| write | Commits a new memory item with its evidence and its holder |
| correct | Creates a new revision of an item and a linked correction item (see "Correction, retraction and deletion") |
| forget | Forgets an item: it is retracted at once and moves to the trash (see "Correction, retraction and deletion") |

- The memory tools are LINA tools in the conversation tool set ([runtime.md](runtime.md)). The agent calls them in the turn the person asks, and the change applies from the next turn.
- The LINA app and the TUI offer remember, correct, forget and moving an item between layers as explicit actions as well. Each action is a request to LINA Core and goes through the same write path.
- Every memory write, from a tool or from an action, goes through the Moirai engine's write path.

### Recall with evidence

- Recall for a conversation reads only the personal knowledge of the conversation's agent and common knowledge.
- Recall returns only `active` items at their current revision that are valid now. Retracted, trashed and superseded revisions are never returned. An item past its `valid until`, or replaced through `supersedes`, is returned only as history, marked as older, when the question is about the past or about a change.
- Every recall result carries its evidence and its layer: the item id and revision, its holder and source agent, and the ledger events, material revisions, work results or external evidence it rests on.
- Lachesis prepares recall material in the background. Atropos chooses, for each turn, what enters the ContextPacket. Information flows one way: Lachesis produces, Atropos reads.
- Within its time cap, Atropos also queries the memory index with the person's current input: a full-text query, and a vector query when the input's embedding returns within the cap. The queries only read the index Lachesis maintains. What the cap cuts off is not waited for. When background memory is available, Lachesis prepares the missed recall for the next turn if it finishes in time. So a new topic is usually recalled in the turn that raises it.
- **Person brief.** After an answer is delivered, Lachesis writes in the background a short brief, for the conversation's agent, of what the person cares about in the topic of the conversation, synthesized from the active memory items that agent reads, standing preferences and recent turns. Every sentence of the brief cites the memory items or ledger events it rests on. The brief is derived state, never canon, and never overrides what the person stated. Atropos puts it into the next turn's ContextPacket with the recalled memory items while it still matches the topic. A brief whose evidence is corrected, retracted or forgotten is discarded and rebuilt from the current items.
- Recall prepared while a turn runs enters the next turn's ContextPacket. Recall reaches a worker only through the projections defined in [work-and-delegation.md](work-and-delegation.md).
- Without embeddings, recall uses full-text search and model reranking. The function stays; only the quality drops. The embedding adapter is defined in [runtime.md](runtime.md). Embedding the conversation ledger sends its text to the embedding endpoint the person configured, so configuring the endpoint is the person's consent for LINA's own conversations. Imported conversations are embedded only after the person agrees in the import, which states the volume. Spans flagged `secret` are never embedded.
- When the person refers to an earlier conversation, the agent searches its own conversation ledgers with LINA's ledger search tool ([runtime.md](runtime.md)) before asking the person to repeat it. A result carries the matching messages, the messages around them and their ledger event ids. Retracted and deleted content never appears in a result. A result is a record of what was said and never becomes a current instruction or an approval. Ledger events that a forgotten memory item rests on are marked in a result, and the agent never brings the forgotten item back from them. When the person deletes conversation content, its copies in earlier results are retracted and erased with it. The search reads all conversations of the agent, not only the current one, imported conversations included, and labels an imported result `origin=import`. It combines full-text and semantic search without a model call, filters by time (`occurred_at`), speaker kind and origin, and leaves replay copies out unless asked. A hit on a summary-tree node is for navigation: it leads to the events the node covers, and a node is never evidence. A lookup by an old id of an imported system returns the LINA record and says which legacy reference matched. `expand` takes a node or an event and returns its children or its exact content a page at a time, for any conversation of the agent; another agent's conversation stays unreadable even where a common item cites it. Flagged `secret` spans read as a mask.
- An answer that used memory keeps the evidence revisions it used. That record stays when the item is later corrected or retracted, so a past answer can always be traced. After a permanent deletion, the record shows the tombstone in place of the evidence.
- The `recall_person` tool (see "Person model") answers a question about the person from memory items and the ledger, with their evidence.
- Imported observations, card lines and legacy summaries are never recall results, never in a ContextPacket and never evidence until the person promotes them (see "Importing a Hermes install").
- One event counts once along its lineage: the original record, an external summary of it and a LINA derivation of that are one lineage, and so are a replay copy and the event it copies. Repeated wording is never counted as independent evidence.
- Recall is for the answer, not for the person's screen. By default the person does not see memory items, the person brief or recall results. They see them when they ask: the remember, correct and forget actions, a view of what LINA remembers, and the Details of an answer. An agent brings memory up in a reply only when it answers what was asked.

### External work memory port

The port lets LINA read what a person's own Codex work left behind.

- The port reads the person's Codex home, the same one discovery reads ([work-and-delegation.md](work-and-delegation.md)): thread records in their original form and Codex memory files (`memories/`, which Codex leaves off by default). It never reads derived caches built by other tools.
- The port is read-only. It never writes, shares code or storage, or attaches a database.
- Each result is external evidence carrying `origin=codex_work`, namespace, id, revision, locator, `fetched_at`, scope and the source text.
- The port reads only formats LINA has verified; a record in any other format returns unsupported. It tells apart missing, stale, unrecognized format, retracted and partial. A change in format, hash or permission invalidates every derived candidate and pending input built from the port.
- Blocking local use of an external item is recorded by LINA and is not deletion of the external original. When a projection LINA exported is retracted, LINA blocks its reuse; LINA never claims the right to delete external originals.
- A missing port never blocks conversation, LINA's own memory or ledger continuity. Lachesis manages LINA's own memory and the port together, and Atropos selects from both for each answer.
- The port reads a live external store each time it is used. Importing a Hermes install is different: it reads once, keeps its own copy and does not read the source again (see "Importing a Hermes install").

### Importing a Hermes install

A person who used a Hermes install can hand its memory to LINA: the Honcho person data (peers, peer cards, conclusions, sessions, summaries and messages), the lossless context database of each profile (messages, summary nodes and the sidecar files of large outputs) and the host message databases that hold what the lossless context database lacks. Only these memory stores are imported. The install's persona files, skills, scheduled jobs and configuration are not. Handing the install over is the approval ([filesystem.md](filesystem.md#importing-another-memory-system)). The import is a copy made once, not a connection.

Principles:

- **Store first, interpret second.** Everything in the sources is stored before anything is interpreted. Interpretation (classification, replay marking, promotion) adds fields and links and never edits imported bytes.
- **The source is read-only, always.** SQLite sources are read through a read-only connection with the online backup API, Honcho with read and query calls only, and sidecar files by copying them with a hash check. LINA never calls a write endpoint and never changes or deletes anything in the install. The person supplies the Honcho address and a read credential, which is used for the run and not kept after it.
- **Repeatable.** An import has a rehearsal, which reports and writes nothing to canon, a first import, and a later import after the source has stopped changing. Records are keyed by legacy reference: a second run adds nothing, a new source row adds only itself, and a source row that changed since is reported as a conflict and never overwritten.
- **One source row, one LINA record.** Counts reconcile row for row. Decoding a tool-call list into its calls is a view of one record, not a split.
- **History is not canon.** Only the ledger side becomes canon, and it is sealed. Summaries, conclusions, cards and logs stay read-only history. Memory items come only from promotion with the person in the loop.
- **Never in a turn, no model call.** The import runs as a background job of the main ([cognition-and-life.md](cognition-and-life.md#memory-jobs)). Classification, replay marking and scans are deterministic. Its writes go through LINA Core's write paths like any other background write.
- **Schema gate.** Before reading, the importer checks the schema version and the column lists of each SQLite source and reads the published API description of the Honcho server. A source whose schema it does not know is refused, with the difference named, and nothing is imported from it.
- **Synthetic fixtures.** The importer is tested on data built to the source schemas. No real install's data enters the repository.

Maps. Before anything is written, the person confirms: each Hermes profile becomes a new agent (created with only a name, as "Agents" allows), maps to an existing agent, or is not imported; the identities of the person in the install are one person; each Honcho peer maps to an agent, to the person or to an external actor. A history is never filed under an agent it did not belong to, because an agent's ledger is read by that agent only. A peer that maps to no agent or person is recorded by name on its events and carries neither id.

What each source object becomes:

| Source object | LINA record | After the import |
| --- | --- | --- |
| Lossless context message | One event in a sealed imported conversation, with its content, tool calls, call id, tool name, channel, write time and ingest time | Sealed canon |
| Host database message, by message uid | The canonical event of that uid. The fields the lossless context database lacks (reasoning, finish reason, platform message id) are kept as preserved body fields. A uid it lacks gets its own event. Every (session, row) copy becomes a legacy reference of that event. | Sealed canon |
| Uid bindings and relations of the lossless context database | Legacy references, and `view_of` and `supersedes` relations between events | Legacy references stay |
| Summary node and its provenance | A node of the summary tree flagged `legacy` and `unverified`, and flagged `degraded`, `out_of_segment_text` or `language_mismatch` where they apply. Its source ids become child links, and a link that does not resolve is an import error. | Read-only history |
| Large-output sidecar file | A LINA material referenced by the output event. Its hash is recomputed and compared. Missing bytes are marked `bytes_unavailable`. | Sealed canon (bytes) |
| Honcho workspace and peers | An import batch record and a peer map the person confirms | The map is canon |
| Honcho session | `source_session` on events. An imported conversation only for agent-to-agent and gateway sessions with no host counterpart. | History |
| Honcho message | A legacy reference on the host or lossless-context event it matches, otherwise a standalone event flagged `honcho-only` | Sealed canon |
| Honcho session summary | A derived hint of its conversation | Read-only history |
| Honcho conclusion | One imported observation per source row, with a group key where rows repeat one statement; external evidence | Read-only history; a candidate for promotion |
| Honcho card line | A candidate draft, not an item | Read-only history |
| Review logs of deleted and moved conclusions | A deleted row becomes a retracted imported observation that keeps the logged text. A correction marker becomes a `supersedes` link (a marker can name several old ids), with its quality noted as parsed from text. | Read-only history |
| Search indexes, embeddings, rollups, lifecycle state | Nothing; they are named in the import report | Rebuilt if wanted |

Each conversation id of the lossless context database becomes one imported conversation of the mapped agent. A scheduled-run session is its own imported conversation, hidden from the default list. The source session id is kept as a legacy reference. Events chain by parent id in source order. A Honcho message is matched to a host or lossless-context event by session, role, the start of its text and a time window, with a text-only fallback inside the same session. The match rate and the unmatched messages are in the report.

Identifiers:

- LINA ids stay opaque and LINA assigns them. Every source key is a legacy reference: a row `(system, instance, kind, key)` that points to the LINA record, unique on that tuple, written in the same transaction as the record, with the hash of the canonical source row, the batch and the time. The systems are the lossless context database, the host message database and Honcho. The instance is the profile for the first two, because their ids restart at 1 in each database, and the workspace for Honcho. One record can carry several legacy references.
- A fully qualified key resolves directly. A bare number, as old transcripts quote one, resolves in the namespace of the agent whose transcript it came from; without an agent it returns the candidates of every namespace and never a guess. A key that was absent at import answers "not present in the source at import" with the batch, and for a logged deletion with the logged text. A tombstoned record keeps its legacy reference, so a repeat import skips it. The ledger search tool does the lookup.

Times and order. `occurred_at` of an imported event is, in this order, the host timestamp, the observed time of the lossless context database, its write time (marked `write_time_only`), or the Honcho time (marked `recorded`). A Honcho time is never preferred to a host time. The source's own time is kept as `recorded_at` with the source's name. Order is the source's order; for Honcho-only events it is the list order of the server.

History and canon:

| Class | Objects | Rules |
| --- | --- | --- |
| Sealed canon | Imported events and relations, legacy references, sensitivity flags, the materials of sidecars, the import batch, tombstones | Append-only; changed only by the person's deletion. Eligible as raw evidence and in ledger search, labelled `origin=import`. Replay copies are not counted as evidence. A last `import_sealed` event seals the imported range of a conversation, and native events never join it. |
| Read-only history | Legacy summary nodes and links, Honcho summaries, imported observations, card lines, review logs | Shown for navigation and old-id lookup, ranked below events. Never in a ContextPacket, a request, a memory write or evidence. Flagged `legacy` and `unverified`. |
| Canon through promotion | Memory items | Only through promotion, below |
| Not kept | Indexes, vectors, rollups, caches | Rebuilt from canon on demand; never the only copy of anything |

Requests. Imported conversations never enter a request's history. A person who wants to continue a past conversation gets a native continuation: LINA builds its summary from the end of the imported conversation within the normal limits and records a compaction event that points into it. Legacy summaries are never its input. The chat-platform history of an agent is not linked to its main conversation by a boundary event, so the imported history stays separate and sealed.

Sensitive content. Every body and sidecar is scanned on import. Secret-shaped values and private-key blocks are flagged and their bytes stay, because the imported ledger is the original record and the round-trip check needs the bytes. Agents read a flagged span as a mask, and flagged spans are excluded as under "Event fields". The person sees the original and can scrub it, which is a partial deletion with a tombstone. Personal identifiers and third-party content are tagged. A summary built from third-party content read through a tool is erased with that content. The report lists counts per kind.

Order of import:

1. Consent and maps, then a rehearsal.
2. Snapshot of every source into the staging area.
3. Identity maps. Nothing is written to a ledger before they pass.
4. The ledger, one transaction per conversation: messages of the lossless context database in id order, then host rows it lacks, then relations; then classification, replay marking and the sensitivity scan.
5. Sidecars as materials, linked to their output events.
6. Honcho messages.
7. The legacy summary tree.
8. The rest of the Honcho data: conclusions, cards, summaries, review logs, peers and sessions, as history.
9. Seal, verify and take a backup generation, so that the import is not lost on a restore.
10. Promotion, afterwards and optional.

Promotion. After the import, never as part of it, Lachesis proposes candidate items from imported events whose speaker kind is `person` and from card lines. The person reviews them in grouped batches, newest first, and can accept a whole cluster. Lachesis drops fan-out copies and rows that are not about the person, keeps person-subject rows, drops dated events older than a horizon that have not recurred and rows superseded by a newer statement, scans for secrets and identifiers, and drafts each candidate in the language of its source with the original sentence as a quote. A line that tells an agent how to behave becomes a persona change proposal and never an item. A promoted item has basis `imported`, cites the imported event, or the imported observation as external evidence together with the person's confirmation, and keeps evidence precision `session` where the source knew only the session. The precision is never raised silently; Lachesis may instead re-derive a concrete ledger event. Facts are never harvested from summary text.

Verification. The import is complete only when it proves that it lost nothing, and the proof is data:

- row counts of every source table against the snapshot, against the records and against the legacy references created
- every source key has exactly one legacy reference, in both directions, and gaps in the source are listed
- a reverse mapper rebuilds every source row from its LINA record, and its canonical hash equals the stored source hash with no mismatch
- every link of the legacy tree and of the observations resolves; a link to an id that the deletion log shows as deleted is kept as a link to a tombstone, and any other is flagged `source_missing`
- the hash of every sidecar equals its recorded hash, and missing bytes are listed
- classification and replay counts and the Honcho-to-host match rate are reported, not asserted as true
- 100 random legacy ids per system resolve forward and backward
- negative checks: the import created no memory item, recall returns no imported observation before promotion, no flagged span is in any index, and no write call reached a source
- a second run adds nothing
- a loss report names every table and column that was not imported, with the reason

Settled choices:

- Reasoning items are imported as preserved body fields and are never sent in a request.
- An agent-to-agent session between two Hermes profiles, while a conversation has one participating agent, is imported as the side each profile already holds, and the other agent is named in the event's legacy block.
- The curated built-in memory files of the install do not join promotion.

### Correction, retraction and deletion

Retraction is the first step of every removal. A retracted item or revision is exposed zero times in recall, search, answers, summaries and LINA APP. Retraction advances the revocation epoch and hides the item. It never rewrites the ledger: the ledger events and the evidence records of past answers stay.

- **Correction.** The person's correction creates a new revision of the same item and a linked correction item with the reason. The earlier revision is retracted. A different item that replaces an earlier one is linked to it through `supersedes`, and recall then returns the newer one and the earlier one only as history.
- **Forgetting.** A forgotten item is retracted at once and moves to the trash for 30 days, where it can be recovered. After 30 days it is permanently deleted.
- **Immediate permanent deletion.** The person may skip the trash. That choice is the approval for the unrecoverable deletion ([main-authority.md](main-authority.md)).
- **Permanent deletion.** Permanent deletion erases the item, every revision of it and all derived data built from it. It leaves a tombstone: the item id, its holder, the ids of the ledger events it rested on, the legacy references of an imported record, when it was deleted and the person who forgot or deleted it, with no content. The conversation the item came from stays in the ledger.
- **No re-extraction.** Lachesis never extracts a forgotten item again from the ledger events it rested on, while the item is in the trash and after it leaves a tombstone. A forgotten item never comes back from the same conversation.
- **Erasure scope.** Permanent deletion reaches every copy LINA manages: current canon, registered Nodes and remote copies. Backup generations are never rewritten: a restore applies every retraction and tombstone again, including those made after the latest generation, and the erased content leaves the backups as their generations age out ([filesystem.md](filesystem.md)). Exported files, messages already sent through an external channel, and copies held by other people are outside it, and LINA says so when it deletes.

Corrections and deletions apply from the next turn. A request already sent is not recalled; the next preparation's retraction check excludes the item.

Derived data (search and vector indexes, prepared recall material and summaries) records the revocation epoch it was built at. Derived data built before a retraction is rebuilt before it is used again. Derived data is never the only copy of anything and never outlives a deletion.

An item that Lachesis derived from other items cites them as its evidence. When a cited item is corrected, retracted or forgotten, the maintenance pass proposes to re-derive or retract the derived item, and its evidence shows the tombstone.

A restore never brings back a retracted or deleted item, or deleted conversation content. Before serving any read, it applies the revocation watermark and every retraction and tombstone again, from canon, newer generations and the removal list, as [filesystem.md](filesystem.md) requires.

## Deferred

- The interim core and voice text used while `persona/lina/` holds no material yet: written by the implementation issue that ships the first conversation product. When the material arrives, the agents created from the LINA preset receive it as the next revision of their core and default voice layers.
- The exact front matter keys of `SKILL.md`: set by the implementation issue that ships the first conversation product.
- Emotion names, values and decay rates: set by measurement during development and recorded in the implementation issue.
- The ContextPacket assembly time cap: set by latency measurement during implementation acceptance of the conversation engine.
- The size that standing preferences reach in item 6.2: measured together with the assembly time cap.
- How near the compaction threshold a turn's request input must be before LINA prepares a summary: set by measurement during implementation acceptance of the conversation engine.
- The full-text tokenizer that finds Korean words, and its fallback: chosen by the implementation issue that brings memory recall into answers. The starting design is a trigram index with a fallback for words of two syllables, a bigram table or a bounded scan. A choice must pass the Korean queries of "Writer and storage" on the conversation ledger and on memory.
- The retention period for raw model request bodies beyond the recorded request hash and assembly list: set during implementation acceptance of the conversation engine.
- The number of background run lines in item 6.3 of the request layout: set together with the assembly time cap.
- The round limit of one agent exchange, the mailbox deadlines and the re-dispatch interval: set by measurement during implementation acceptance of agent exchanges (the recommended round limit of 4 is proposed and unmeasured).
- How an agent exchange is presented when it has more than two agents: a group exchange is not designed; it follows the group conversation backlog item.
- The inline size above which an event body is kept as a material: set by measurement during implementation acceptance of the conversation engine.
- The caps of the standing profile (lines and tokens), the number of items and the time cap of depth 1 of `recall_person`, the window in which consecutive messages of the person are one utterance (tuned per person), the duplicate thresholds of the maintenance pass, how long an unanswered proposal waits before it expires, the horizon for dated events in promotion and the size of a promotion batch: starting values set by measurement during implementation acceptance of memory.
- The exact fields of the Hermes host message databases, and the schema versions of the lossless context database and of Honcho that the importer accepts: confirmed by reading the schemas of the real install before the importer is accepted. The reads need no personal content.
- The checks that implementation acceptance of memory runs on synthetic fixtures, as properties of the data and not of wording: no item whose subject is the person rests on an event whose speaker kind is not `person`; a fan-out fixture yields one item with two pieces of evidence; a chain of three successive statements returns only the newest valid one; Korean queries return hits; a forced summarizer failure publishes no node and leaves the events in the request; flagged spans are absent from every index.
- The bound on the part of a result's text that the model sees, and the size limit of a preview's file bytes held in the ledger: set by measurement during implementation acceptance of the conversation engine.
- The reply budgets, the size of a tuning step and the reaction classes: starting values set in Reply shape, tuned by measurement with real use and kept per person.
- The file format and the manifest fields of an episode export: set with the first development run that uses the replay command ([runtime.md](runtime.md#replay-command)).
