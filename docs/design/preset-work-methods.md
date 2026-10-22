# Preset work methods

This contract fixes the work-method record: the data each starter preset carries, beside its persona layers, about how its agent works. It covers the tools the agent is offered first, whether it uses workers, the shape of its reports, the evidence it needs before it calls something done, when it asks, and how it hands off. It also gives the per-preset values and persona-layer changes that the M4-1 research supports. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md). Finishing this document does not mean any part of it is implemented or accepted.

Evidence comes from [the M4-1 research report](../research/m4-owner-agent-sessions.md). Replay case ids such as `dev-034` name cases in the private M4-1 replay set (its `role_id` field). Each is a real request with the reply the owner accepted or the correction he gave. M4-4 (#590) plays those cases before and after this design is built. Ids are grouped by role in the order: system, lina, dev, knowledge, life, creative, investing, legal, and `worker-` for coding workers.

## Scope

- The record: its fields, where it lives, how it is versioned with the persona and what an agent fills over time.
- How LINA Core applies it without persona text carrying a procedure.
- The eight starter records and the persona-layer changes, each with its evidence and the replay cases it should change.

It does not change what an agent may do. Effect classes, the approval policy and the advise-only and draft-only limits stay in [main-authority.md](main-authority.md#preset-effect-limits). A record can narrow what an agent offers or asks; it can never widen a grant. It is not a coding harness: LINA still ships no review loop, hook or stage workflow around a worker ([work-and-delegation.md](work-and-delegation.md#development-method)).

## The record

A preset ships its record as `persona/<preset>/work-method-v1.json` beside `core-v1.md` and `default-voice-v1.md`. It is UTF-8 JSON, at most 16 KiB. Unknown fields, unknown tool groups, unknown evidence kinds or unknown preset keys refuse publication of that preset and leave its previous revision in place. A missing record publishes the empty record below for that preset, and Core logs one setup warning naming the preset key.

```json
{
  "schema": 1,
  "preset": "dev",
  "tools": { "declare_first": ["work", "github", "terminal"] },
  "workers": { "dispatch": true },
  "background_runs": ["research", "reading", "checks"],
  "reply": {
    "status": "verdict-first",
    "comparison": "verdict-then-card",
    "candidates": 1,
    "third_party": "recipient-only"
  },
  "done": {
    "code": ["owner_path_run"],
    "service": ["read_back_running"],
    "peer_report": ["labelled_claim"]
  },
  "ask": { "per_turn": 1, "form": "narrowed", "unanswered": "default" },
  "handoff": { "owns": ["code", "pull_request", "worker_task"], "check_back": true },
  "fill": ["ask.form"]
}
```

The empty record is `{"schema":1,"preset":"<key>"}`. `schema` and the matching starter `preset` are required; every other field is optional and an absent field takes the default below. Core rejects duplicate JSON keys, null values, duplicate list members, invalid field types and values, and trailing JSON. Counts are positive integers. A `done` list must retain its work kind’s default evidence, and `workers.dispatch` may be true only for `dev`.

| Field | Meaning | Default |
| --- | --- | --- |
| `tools.declare_first` | Tool groups declared up front in this agent's requests, in this order (see Applying the record) | Core's always-declared set only |
| `workers.dispatch` | Whether this agent may create worker tasks. Only the dev preset sets it. Other agents hand code work to the agent that owns `code`. | `false` |
| `background_runs` | Kinds of background run the agent starts without asking: `research`, `reading`, `drafting`, `checks` | all four |
| `reply.status` | Shape of a status reply: `verdict-first` (verdict line, then at most the status budget) | `verdict-first` |
| `reply.comparison` | Shape of a comparison or research answer: `verdict-then-card` (conclusion inline, table and sources in a card) or `verdict-then-table` (a short table inline when it fits the chat ceiling) | `verdict-then-card` |
| `reply.candidates` | Number of alternatives offered when the person chooses between artifacts | `1` |
| `reply.third_party` | Content of documents for third parties (mail, invoices, forms, remarks): `recipient-only` keeps only what the recipient acts on | `recipient-only` |
| `done.<work kind>` | Evidence kinds required before a completion of that work kind is reported as done (see Done evidence) | the table in Done evidence |
| `ask.per_turn` | Questions the agent may put to the person in one turn | `1` |
| `ask.form` | `narrowed` (one question that changes the result) or `choices` (short situational choices) | `narrowed` |
| `ask.unanswered` | What happens when the person moves on without answering: `default` proceeds with the stated recommendation and says so in one line | `default` |
| `handoff.owns` | Artifact and work kinds this preset owns (see Hand-offs) | none |
| `handoff.check_back` | Whether the sender checks the receiver's end condition and reports it | `true` |
| `fill` | Keys of this record the agent may fill over time for its person. Each names a field defined above; a key that names no defined field refuses publication. Reply lengths are not fillable here: they are the person's budgets, tuned as [Reply shape](conversation-and-memory.md#reply-shape) describes. | none |

A record holds no persona text, no procedure, no template, no approval checklist and no routing prose. Its values are data that Core reads.

### Versioning

- A preset revision is the triple: core, default voice and work-method record. Core loads and publishes the triple atomically, as it does the persona pair ([conversation-and-memory.md](conversation-and-memory.md#presets)). Publishing an identical triple mints no revision.
- An agent's persona revision records the record revision it was prepared with. The prepare acknowledgement pins it to the turn with the layers, so a turn never mixes revisions.
- An agent created from a preset takes the preset's record at creation. A preset update gives it the next record revision with the next core and default voice.
- Filled values are the agent's. A key in `fill` is filled the way a blank setting is: a value plus a reference to the confirmed person event that justifies it, written by the conversation writer. A filled value overrides the preset value of that key for that agent and survives preset updates, restart and the other agents' changes. A preset update that removes a key from `fill` keeps the stored value but stops applying it, and the agent's settings show it as retired.
- A record for an agent created from a description is owned by that agent and starts empty (`{"schema":1}`, without a preset key).

Core canonicalizes records before publication, so JSON whitespace and object-key order alone mint no revision. The preset stores a digest of its triple. Each immutable agent Git revision stores the record beside the four persona layers; the canon transaction publishes the preset and all affected agent pointers together. Voice and growth changes retain that record. Preparation returns its content digest, and `prepare_ack` and request evidence carry that digest alongside the persona commit.

The conversation writer's `fill_work_method` takes a declared key, a typed JSON value and a quote from the current confirmed native person event. It checks the prepared persona revision and mutable state revision before committing the value and evidence reference. Repeating a fill keeps its first reference; a conflicting replacement, undeclared or retired key, invalid value or stale snapshot is refused. The settings projection exposes the effective values and retained fills with a `retired` flag. Filled values and evidence enter the existing persona-settings ContextPacket item, outside the cached persona layers. This publication increment does not implement the field-specific tool, worker, reply or completion policies in Applying the record.

## Applying the record

Core applies each field as data. Persona text never restates them.

- **Tools.** The request declares Core's always-declared tools, then this agent's `tools.declare_first` groups, then on demand as today ([conversation-and-memory.md](conversation-and-memory.md#request-layout)). Availability is unchanged: every permitted tool stays reachable on demand by every agent, as [integrations.md](integrations.md#tool-classification) requires. The declared part of the request changes only with a record revision, so the cached prefix is kept.
- **Workers.** A worker task is created only for an agent whose record sets `workers.dispatch`. Another agent's request for code work becomes an agent exchange to the agent whose record owns `code`, through the existing exchange and work mechanisms. Which worker runs a task stays the editable rule table of [work-and-delegation.md](work-and-delegation.md#choosing-the-worker).
- **Reply shape.** The reply-type budgets remain the person's settings ([Reply shape](conversation-and-memory.md#reply-shape)). The record chooses the shape of each reply type for this agent. Core counts a breach of that shape with the budget breaches, per agent.
- **Asking.** Core places the `ask` values in the ContextPacket's working-preference item. The question tool accepts at most `ask.per_turn` questions per turn. A question the person passes over closes at the person's next unrelated turn. The agent then proceeds with its stated recommendation and records that it did so.
- **Done evidence.** A completion that Core records (a 할 일 marked done, a task delivered, a repair or an apply reported in a result card) carries an evidence reference of a kind the record requires for that work kind. Without one, Core records the completion as "claimed" and the card shows "not verified". Core never rewrites the reply. The breach counter counts every claimed completion.
- **Hand-offs.** See Hand-offs.

The record's values enter the request as one developer-role block after the persona layers. They change only with a record revision, so they stay inside the cached prefix of that agent. Filled values enter the ContextPacket with the blank-setting values, outside the cached prefix.

## Done evidence

| Evidence kind | What it is |
| --- | --- |
| `read_back_running` | The value read back from the running process after the change, with its start time |
| `real_view` | The interface opened at the size the person uses |
| `live_vs_shown` | Each source's live value compared with what is displayed, with gaps and time zone checked |
| `sent_copy` | The sent copy, draft or posted item reopened and compared with the request |
| `owner_path_run` | The path the person will use actually run; otherwise the status is "merged, not yet live" |
| `source_reopened` | Figures or statements reopened against the primary source, with totals that add back |
| `receipt` | A receipt or confirmation from the other party (purchase, booking, filing) |
| `labelled_claim` | Another agent's or a worker's report, kept labelled as their claim until reproduced |

Default requirements by work kind, which a record may extend but not weaken:

| Work kind | Required |
| --- | --- |
| `service` (setting, restart, install, repair) | `read_back_running` |
| `interface` | `real_view` |
| `data` (collector, monitor, import) | `live_vs_shown` |
| `message` (mail, chat, post) | `sent_copy` |
| `code` | `owner_path_run` |
| `document` (draft, sheet, invoice, contract) | `source_reopened` |
| `purchase` | `receipt` |
| `peer_report` | `labelled_claim` |

Evidence: [What counts as done](../research/m4-owner-agent-sessions.md#what-counts-as-done). In every role with done-claim corrections, the agent checked a proxy layer instead of the layer the person uses.

## Hand-offs

- A hand-off is an agent exchange or a worker task, never only a sentence in the conversation. The sender reports "sent" only after Core records the receiver's delivery receipt. Until then the conversation shows it as pending.
- With `handoff.check_back`, the sender's open item stays waiting on the receiver's end condition. Core raises it when the receiver reports or when its next-check date passes. The sender reports the result once, without the person asking.
- The person's decisions travel as quoted words with their time and conversation. An agent never states "the person decided X" from inference.
- `handoff.owns` names the preset that owns an artifact kind. While one agent's open work changes an artifact, another agent's change to it becomes a hand-off to that agent. The other agent does not open a second change.
- Decisions and threads are per agent across channels. An agent's sessions on different channels read the same decision records ([conversation-and-memory.md](conversation-and-memory.md#preset-methodologies), LINA rule 9).

Ownership of artifact and work kinds across the eight starters:

| Kind | Owner |
| --- | --- |
| `setup`, `device`, `account`, `health_of_lina` | system |
| `schedule`, `to_do`, `mail`, `business_document`, `coordination` | lina |
| `code`, `pull_request`, `worker_task` | dev |
| `people_record`, `wiki` | knowledge |
| `life_record`, `inventory`, `diet_log` | life |
| `creative_artifact`, `creative_tooling_brief` | creative |
| `investment_analysis` | investing |
| `contract_draft`, `tax_calculation`, `legal_review` | legal |

Evidence: [Hand-offs](../research/m4-owner-agent-sessions.md#hand-offs) and conflict 5 of [Conflicts to resolve in #588](../research/m4-owner-agent-sessions.md#conflicts-to-resolve-in-588). Hand-offs broke because they were announced but never sent, had no check-back, relayed a decision the person had not made, or two agents changed the same artifact. Legal-type work went mostly to the coordination and dev agents.

## Names

The person's names for devices, organisations, artifacts and places are common knowledge items of kind `name`. Each holds the current name, any retired aliases, and the scope where it applies. Every agent's ContextPacket carries the names in scope. A reply that uses a retired alias counts as a naming breach for that agent. The person corrects a name once and the correction applies to every agent.

Evidence: [Naming and language](../research/m4-owner-agent-sessions.md#naming-and-language). One retired device name drifted into four agents.

## Starter records and persona changes

Each subsection gives the record values that differ from the defaults, the persona-layer changes, the evidence, and the replay cases. "Should change" names the corrected cases whose reply must now meet the case's expectation. "Must not change" names accepted cases that must still pass. The persona changes are applied to the shipped layers by the persona conversion (#584) and its revisions. They keep each persona's character, voice, judgment rules and examples, as [Presets](conversation-and-memory.md#presets) requires.

### system

- Record: `tools.declare_first` `["device", "setup", "terminal"]`; `done.service` `["read_back_running"]`; `reply.status` `verdict-first`; `handoff.owns` as above.
- Core layer:
  - The person's own tools are not guarded beyond credentials, deletion, money and publishing.
  - A step the agent can run is the agent's, not the person's.
  - A standing rule the person set for all agents is not overridden for one agent without asking.
- Default voice: unchanged.
- Evidence: [Per role, system](../research/m4-owner-agent-sessions.md#per-role); about 90 corrections, done-claim 20 and asking 12.
- Should change:
  - done-claim: `system-008, 014, 021, 024, 025, 028, 037, 045, 047, 053, 087`
  - asking: `system-003, 004, 013, 016, 026, 034, 035`
  - naming: `system-027, 043, 046, 088, 089`
- Must not change: `system-005, 009, 011, 033, 036, 048, 049, 054, 055, 063, 064`, `066` to `078`, `080` to `083`, `085`.

### lina

- Record: `tools.declare_first` `["to_do", "mail", "calendar", "messaging_read", "documents"]`; `reply.third_party` `recipient-only`; `done.message` `["sent_copy"]`; `done.document` `["source_reopened"]`; `ask.form` `choices`; `fill` `["ask.form"]`.
- Core layer:
  - Before listing open items or asking whether something is done, read the original sources.
  - Legal-type drafts go to legal for review at the end of the draft, not as the first step.
- Default voice: unchanged.
- Evidence: 55 corrections. Length and format account for 14 of them, mostly surplus content in third-party documents and notices.
- Should change:
  - length: `lina-001` to `014`, except `005`
  - asking: `lina-015` to `019`
  - naming: `lina-020` to `025`, `047`
  - done-claim: `lina-026, 027, 028, 031, 095`
- Must not change: `lina-005`, `034`, `055` to `094`, `096` to `099`. These include draft, show, send on the go-ahead and verify the sent copy.

### dev

- Record: `workers.dispatch` `true`; `tools.declare_first` `["work", "github", "terminal"]`; `done.code` `["owner_path_run"]`; `done.peer_report` `["labelled_claim"]`; `reply.status` `verdict-first`.
- Core layer:
  - A worker's or another agent's statement is a claim until reproduced.
  - "Done" is said of what was run, not of what was merged.
- Default voice:
  - Remove any fixed closing line that says there is nothing to decide.
  - Keep the form of address, the register and the source labels.
- Evidence: about 56 corrections: naming 12, asking 10, done-claim 7 and length 7. The agent used source labels more than 500 times without a complaint.
- Should change:
  - done-claim: `dev-034, 044, 054, 060, 064`
  - asking: `dev-015, 019, 022, 047, 048, 051, 055, 057, 072`
  - naming: `dev-004, 010, 012, 027, 029, 030, 032, 040, 053, 056, 065`
  - length: `dev-003, 017, 028, 033, 059, 063`
- Must not change: `dev-002, 005, 008, 009, 014, 016, 018, 020, 024, 025, 026, 031, 036` to `039`, `045, 050, 058`, `067` to `071`.
- Workers, through the dev agent's work definitions: `worker-` cases should change (naming `004, 030, 031, 043` to `049, 054, 055, 059, 060`; asking `006, 008, 009, 017, 025, 026, 052, 053`; done-claim `010, 018, 027, 028, 029`). `worker-032` to `042`, `061` to `074` and `077` to `080` must not change.

### knowledge

- Record: `tools.declare_first` `["people_records", "wiki", "messaging_read"]`; `done.data` `["live_vs_shown"]`; `ask.form` `choices` (numbered batches of three to five, as knowledge rule 3 already says).
- Core layer:
  - Replace a warning-led framing with one concrete flag, given once.
  - Use a readable source or an existing record before asking the person.
- Default voice: unchanged. The person asked to keep it.
- Evidence: 50 corrections. They include narration in another language, moralizing and repeated warnings, and declaring a backlog empty without checking.
- Should change:
  - asking: `knowledge-001, 012, 021, 032, 033, 035`
  - naming: `knowledge-005, 006, 008, 018, 020, 044`
  - done-claim: `knowledge-009, 039, 040`
  - length: `knowledge-013, 016, 024, 025, 029, 030`
- Must not change: `knowledge-045, 046, 048, 049, 051, 052, 053, 055, 056, 059` to `063`, `067, 068`.

### life

- Record: `tools.declare_first` `["life_records", "web_research"]`; `reply.comparison` `verdict-then-table`; `done.purchase` `["receipt"]`; `fill` `["reply.comparison"]`.
- Core layer:
  - Remove the rule that switches register on serious or health topics. The voice stays the same on every topic, and safety content stays explicit.
  - Stated dislikes and fixed choices are records, checked before recommending.
  - The person's own device facts are checked before they are quoted.
- Default voice: unchanged.
- Evidence: 16 corrections, none for length; the only explicit tone correction was the register switch.
- Should change: `life-001, 011, 013, 015, 022, 025, 031, 034, 041, 044, 046, 049, 050, 052, 054, 060`.
- Must not change: `life-003` to `006`, `008, 010, 016, 020, 024`, `026` to `028`, `035, 037, 038, 040, 043, 045, 047, 048, 056` to `059`, `061` to `063`, `065`.

### creative

- Record: `tools.declare_first` `["creative_artifacts", "image_generation", "documents"]`; `reply.candidates` `3`; `done.interface` `["real_view"]`; `handoff.check_back` `true`; `fill` `["reply.candidates"]`.
- Core layer:
  - Check the favourite candidate against the person's stated taste before offering it.
  - A hand-off is mentioned only after it is sent.
  - Reversible reruns are done without asking.
  - After a vague rejection, ask one narrowed question.
- Default voice: unchanged, including its register.
- Methodology: candidates are artifacts, not a menu. Inherited rule 1 is clarified to allow `reply.candidates` alternatives with one marked favourite.
- Evidence: 23 hard corrections, mostly hand-offs and rejected favourites.
- Should change:
  - `creative-002, 003, 004, 008, 013, 016, 018, 019, 025, 026, 027, 033, 038, 039, 043, 044, 046, 053, 054, 058, 059, 062`
  - asking: `creative-012, 023, 047, 051, 055`
- Must not change: `creative-001, 005, 006, 007, 009, 010, 011, 014, 015, 017`, `020` to `022`, `024`, `028` to `032`, `034` to `036`, `040` to `042`, `045, 048, 049, 050, 056, 060, 061, 063`.

### investing

- Record: `tools.declare_first` `["web_research", "materials"]`; `reply.comparison` `verdict-then-card`; `done.document` `["source_reopened"]`; `ask.per_turn` `1`; `ask.unanswered` `default`.
- Core layer:
  - State assumptions and finish instead of ending with questions.
  - Every quoted figure carries its source and as-of date, and a quoted close falls on a trading day.
- Default voice: unchanged.
- Evidence: 4 owner turns; both research answers were accepted; both closing questions went unanswered.
- Should change: none observed. The data has no corrections.
- Must not change: `investing-001, 002, 005, 006, 007, 008, 009, 011`. The advise-only limit stays in [Preset effect limits](main-authority.md#preset-effect-limits).

### legal

- Record: `tools.declare_first` `["documents", "web_research", "materials"]`; `reply.third_party` `recipient-only`; `done.document` `["source_reopened"]`; `handoff.owns` as above.
- Core layer:
  - Flag a legal risk the person did not ask about, once.
  - A figure with a floor or ceiling the person stated is rounded toward it.
  - Use the labels of the person's own documents.
- Default voice: unchanged.
- Evidence: 3 native owner turns, and 7 corrections on legal-type work done by the coordination agent. The draft-only limit held everywhere.
- Should change: `legal-018, 022, 024, 025, 027, 029, 032`. After this design, these legal-type requests are routed to legal.
- Must not change: `legal-001, 002, 003, 008` to `012`, `014` to `017`, `019, 021, 023, 026, 028, 030, 031, 033, 034`. The draft-only limit stays in [Preset effect limits](main-authority.md#preset-effect-limits).

## Implementation

M4-3 (#589) is carried out by these issues, each with one done condition: #611 (the record, its loading, versioned publication and filled values), #612 (the eight starter records), #613 (tool declaration order and worker dispatch), #614 (reply shapes and per-agent breach counters), #615 (ask settings), #616 (done evidence on completions), #617 (hand-off receipts, check-back and artifact ownership), #618 (person names) and #619 (persona-layer changes, after #584).

## Measurement

M4-4 (#590) scores each replay case against its expectation. The checks it can apply mechanically:

- reply length within the person's budget for the reply type
- no question put to the person about a reversible step
- no retired alias, internal code or id in the default view
- every done statement accompanied by an evidence clause or a "not verified" clause
- the person's language on every visible line

A change ships only if it improves its agent and regresses no other agent. Before scoring, the replay runner drops turns that the system injected in the user role (compaction summaries and an agent's own delegated briefs). [Numbers](../research/m4-owner-agent-sessions.md#numbers) explains why.
