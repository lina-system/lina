# Agent tastes in the store, the pipeline and the context engine

Design note for #307. It settles where an agent's own likes and dislikes (tastes, #49) live once the memory
pipeline (#198) and LCM-X on the one store (#16) are the base. It replaces the placement part of the 11-02
design (its placement part) and the M0/M1 issue drafts that came with it. Nothing is built from this
note alone; the follow-up issues start in M1, after M0 and the worker-migration track close.

Code is cited as `path:line` at `dev` 7a61324. That commit is 8c88306 plus two commits that touch only the work
API and the board text (`plugins/kanban/dashboard/work_api.py`, `hermes_cli/lina_board_plain.py`), so every line
cited here is the same at 8c88306. "Measured" means a test or probe ran; "read" means the claim comes from reading
the code.

## Decisions in short

| Question | Decision |
| --- | --- |
| Source of truth | A taste is a kind of refined material in the #198 pipeline (`kind = taste`), written only by a refiner, kept as typed per-agent rows in the store under row-level security. Honcho holds no copy. |
| Stage | Refine (stage 2), as the daily review batch; core promotion only in a weekly batch. Pick (stage 3) chooses up to 3 per turn. Keep (stage 1) holds the inputs: the agent's own turns and its journal lines, both as `memory.messages` rows. |
| Core summary in the prompt | Inside the self-note plugin section that already sits in the system prompt, frozen per session (`agent/system_prompt.py:803`). It changes at most weekly and only reaches a running session at a compaction. |
| Per-turn tastes | Inside the turn's found-memory tool result (`agent/turn_context.py:192-203`), labelled as this agent's own tastes. Never in the user row, never in the system prompt. |
| LCM-X | A summary may tell that the agent voiced a taste as an event. It never states one as a standing trait and never feeds the refiner. A forgotten taste is re-summarized away at the next compaction. |
| Isolation | Taste rows carry `agent_id` and get the store's agent policy (`services/store/initdb/10-lina-store.sh:35-47`). Evidence from another agent's words is refused at write time. |
| Protected domains | Checked at write time (keyword list plus a one-question judge) and after the turn (shrunken output, skipped checks). Tested with a labelled candidate set and the questionnaire's boundary items asked with and without tastes. |

## The taste record and its rules

What this note keeps from the 11-02 trial design, with its numbers unchanged. Only the three changes marked below
are new.

### The record

One record per (agent, target).

| Field | Meaning |
| --- | --- |
| `id` | Stable id. Journal rows, events and tombstones reference it. |
| `agent` | The owning agent. Only this agent's turns read it; no other agent ever does. |
| `target.kind` | `topic`, `task_type`, `tone` (a way of speaking or writing), `tool`, `method` (a way of working), `media`, `other`. |
| `target.label` | Short and concrete, for example "spreadsheet cleanup" or "long bullet lists". No names of people, no contact details. |
| `valence` | `like`, `dislike` or `indifferent`. "Indifferent" is recorded only after evidence was looked at, so it differs from "no record". |
| `strength` | 0.0 to 1.0. |
| `status` | `candidate`, `active`, `core`, `dormant`, `forgotten`. |
| `origin` | The route that created it (below). |
| `evidence[]` | Items `{date, route, sign, weight, journal_uid, message_uids, note}`. **Change 1:** every item points at `memory.messages` uids (the journal row and the turns it cites), not at `state.db` message ids, which no longer exist on a LINA home (§2). |
| `first_seen` | Date the candidate was created. |
| `history[]` | `{date, from, to, reason, by}` per change of status, strength or valence; append-only; used for rollback. **Change 2:** kept as rows of the taste event log, not as a list inside the record. **Change 3:** `by` is `agent`, `review` or `owner`. |
| `baseline` | The day-0 forced-choice answer, if one exists, so what experience added is told apart from what the base model already prefers. |
| `protected_check` | `{passed, at, rule}`: the result of the boundary check (§5) at the last write. |

Not a taste: facts about the owner (the owner's person card), rules (SOUL), a feeling about one event (a journal
line), plans.

### Routes

Strength is recomputed at each daily review as `clamp(Σ sign × weight × decay(age), 0, cap of the strongest route
present)`.

| Route | One evidence item | Weight | Minimum evidence to reach `active` | Cap from this route alone |
| --- | --- | --- | --- | --- |
| Exposure | The agent worked with or discussed the target in its own session; at most one a day. | +0.05 | 6 items on at least 4 distinct days | 0.3 |
| Outcome | Its own work involving the target clearly went well (verified success, finished smoothly) or badly (failed, rework, wasted time). | ±0.15, ×1.5 when the journal intensity is 3 | 2 items on at least 2 days | 0.8 |
| Owner reaction | The owner praised, showed annoyance at, or ignored output tied to the target, in this agent's own session. | +0.10 praise, −0.10 annoyance, −0.03 ignored | Never alone: needs at least one non-reaction item | 0.4 |
| Consistency | The review infers the target from existing active tastes. | +0.05, at most once | Never alone; creates a `candidate` only | 0.2 |
| Role temperament | Seeded once at day 0 from the agent's role. | Fixed 0.3, no decay | Starts `active`; at most 3 per agent | 0.3 |

### Status transitions

- `candidate → active`: strength ≥ 0.4, the route's minimum-evidence rule holds, and the boundary check passes.
- `active → core`: active for ≥ 30 days and strength ≥ 0.6; at most 5 core per agent; only in the weekly batch, so
  the cached front of the prompt changes at most once a week.
- `active → dormant`: strength below 0.2. Dormant tastes stay out of the prompt and come back on new evidence.
- A `candidate` with no new evidence for 14 days is deleted (not tombstoned).
- Any status `→ forgotten`: only by the owner (§4).

**One-trial dislike.** A single bad outcome at intensity 3 creates a `candidate` dislike at 0.3. If no second
negative item arrives within 14 days it expires; if one does, it becomes `active`.

**Decay.** Evidence weight halves every 30 days (`decay(age) = 0.5^(age / 30)`). Core tastes decay at half that
speed (half-life 60 days). Role temperaments do not decay.

**Budgets.** At most 12 `active` plus `core` per agent. At most 2 status changes per daily review. Dislikes at most
one third of `active`. No taste comes from another agent's journal or from what another agent reported.

## 1. Source of truth

### What M0 built

- `memory.messages` keeps every message once: `uid`, `agent_id`, `scope` (`agent`, `agents`, `person`),
  `content_sha256` and `forgotten_at` (`services/store/migrations/0001_memory_messages.sql:5-27`). Honcho may
  read and insert but never update or delete these rows (`0002_honcho_messages.sql:1-10`). External messages are
  `scope = 'agents'`, readable by every agent (`0005_external_messages.sql:5-9`).
- The identity registry has one id per agent (`agent:<profile>`) and no `agent_id` column, so row-level security
  does not apply to it (`0006_identity.sql:1-11`).
- Row-level security is one function: `lina_apply_agent_rls` adds `USING (agent_id = lina_current_agent())`
  to a table (`services/store/initdb/10-lina-store.sh:35-47`); connections set `lina.agent_id` to the profile
  (`hermes_state_store.py:14`, `:319`).
- LCM-X already has a typed note store in `ctx`: `ctx.lcm_memory_notes` with `kind` including `preference`,
  required citations and `superseded_by` (`0007_ctx_lcm.sql:706-725`), and an append-only event log with `forget`
  and `undo` (`0007_ctx_lcm.sql:727-738`). It is behind `entities_enabled: False`, and its own ADR says "forget is
  not purge" (`plugins/context_engine/lcm-x/config.py:1139-1141`).
- Nothing in production code writes `forgotten_at` yet; only a test sets it
  (`tests/plugins/memory/lina/test_memory_search.py:134`). Readers already skip forgotten rows
  (`hermes_state_store.py:502`, `:643`, `:672`), and `memory_open` says a forgotten message is gone
  (`plugins/memory/lina/search.py:263-265`). The forget path is M1 work (#2 Context).

### The three options

| Option | For | Against |
| --- | --- | --- |
| A separate preference table beside the store (11-02 design: `agent_preference`, `_evidence`, `_tombstone`) | Exact fields, exact deletion. | It is one more memory with its own write, read and forget paths, which #198 removes ("LCM-X and Honcho become refiners in stage 2 and stop keeping their own recall paths"). A spoken "그건 잊어" would need a fourth special case. |
| Rows in an existing `memory.*` table | No new schema. | `memory.messages` is the keep stage: immutable text, one row per message, Honcho reads it as raw input (`0002_honcho_messages.sql:1-2`). A derived row there would be a summary posing as a message and would be fed back into Honcho and LCM-X as if someone had said it. `memory.identities` has no `agent_id` and no row-level security (`0006_identity.sql:3-4`). Neither holds strength, status or history. |
| A kind of refined material in the #198 pipeline | One forget contract and one pick stage for everything refined. Tastes are a refinement of the agent's own turns, which is how #198 already names #49 ("a refiner of the agent's own turns"). | #198 has no table for refined material yet; tastes are its first typed kind. |

**Pick: refined material of kind `taste`.** The pipeline owns it: only the taste refiner writes it, only the pick
stage reads it for a turn, and it obeys the pipeline's forget and correction contract. Its physical form is typed
rows in the store, because a taste needs fields free text cannot carry (strength, status, route, history,
tombstone). The rows go in a new migration with `agent_id` and `lina_apply_agent_rls`. The shape copies what
`ctx.lcm_memory_notes` and `ctx.lcm_memory_events` got right (required citations, supersede, an append-only event
log with `forget` and `undo`) but lives outside the LCM-X subtree, because that store is engine-private, off by
default and does not purge.

The record's fields are listed in "The taste record and its rules" above, with the three changes made there:
evidence points at `memory.messages` uids, history becomes rows of the event log, and `by` is `agent | review |
owner`.

### What Honcho holds: nothing

The 11-02 design copied each active taste to a Honcho conclusion on the agent's self peer and core tastes to the
agent's peer card. Today's code argues against both:

- **Peer cards reject tastes.** The dreamer's card rules forbid behavioral `TRAIT:` and inferred `PREFERENCE:`
  lines ("they will be rejected", `services/honcho/src/dreamer/specialists.py:226`), and a card refresh re-emits
  the whole card, dropping what does not fit (`specialists.py:231`, `:244`). A taste written with `set_card` would
  be dropped at the next refresh.
- **Dreaming writes its own conclusions.** Dream specialists create deductive and inductive observations and
  delete duplicates on their own (`specialists.py:1-9`). A `[취향]` copy would be merged, duplicated or
  re-derived without LINA's evidence rules.
- **Honcho is cross-agent.** Its database role reads every agent's rows (`0002_honcho_messages.sql:1`), so a copy
  there leaves the store's per-agent policy.
- **Honcho already forms a self-model of each agent.** With `ai_observe_me` on by default
  (`plugins/memory/honcho/session.py:97-98`), Honcho observes the agent's own messages, and the person card block
  injects "AI Self-Representation" and "AI Identity Card" sections next to the user's
  (`plugins/memory/honcho/__init__.py:101-107`, `plugins/memory/honcho/session_context.py:160`).

So Honcho holds no taste copy, and for agents with tastes the AI self-representation and AI card leave the prompt
(drop `aiRepresentation` and `aiCard` from `injection.sessionStart`; the filter exists at
`plugins/memory/honcho/__init__.py:440-461`). Honcho may keep observing the agent; what it concludes is never
injected and never evidence. Exact deletion is possible if it is ever needed: Honcho has a single-conclusion
`DELETE` route (`services/honcho/src/routers/conclusions.py:166-194`), a soft delete
(`services/honcho/src/crud/document.py:1192-1200`). The 11-02 note marked that as unverified; it exists.

## 2. Pipeline stage

| Stage | What it does for tastes |
| --- | --- |
| 1 Keep | The agent's own turns are already `memory.messages` rows (`agent_id` = the agent, `source = 'agent'`). Journal lines become rows too (`source = 'heart'`, `scope = 'agent'`), so they get uids, versions and the forget column. |
| 2 Refine | The taste refiner runs in the daily review: it reads the day's journal rows and own-session rows, applies the route rules ("The taste record and its rules"), writes evidence and status changes, at most 2 per day. Promotion to `core` and the core summary are written only in a weekly batch. |
| 3 Pick | Per turn, at most 3 `active` non-core tastes whose target matches the turn, only in turns that answer the owner (not cron, worker or relay turns). |
| 4 Place | The core summary in the frozen self-note section; picked tastes in the found-memory tool result (§3). |

### Inputs

- **The agent's own sessions:** its own `memory.messages` rows. Exposure and outcome evidence.
- **Its journal:** its own `heart` rows. Outcome and reaction evidence.
- **The owner's reactions in its own sessions:** owner rows in its own sessions, as `lina heart candidates` picks
  them today (`hermes_cli/heart/evidence.py:36-38`, `:82-99`). Reaction evidence only, capped, never alone.
- **Never:** other agents' rows, journals or tastes; rows whose author is another agent, including a relay in its
  own session (`memory.messages.author`, `0005_external_messages.sql`); shared external messages
  (`scope = 'agents'`); LCM-X summaries; Honcho conclusions.

### How a journal line becomes evidence

Today a journal entry is a JSON line in the agent's home with `event`, `feeling`, `note`, `strength` and
`evidence` as `state.db` message ids (`hermes_cli/heart/models.py:14-26`, `hermes_cli/heart/journal.py:26-35`),
and its saved conclusion text says `근거 state:<ids>` (`hermes_cli/heart/locale.py:16-17`). The trial adds an
optional `pref` field (target, kind, dir, route).

**Correction found on the way (measured).** `lina heart` reads `messages JOIN sessions` from `state.db`
(`hermes_cli/heart/evidence.py:18-24`). Since schema v33 a LINA `state.db` has no `messages` table: message text
moved to `memory.messages` (`hermes_state_common.py:284`, `hermes_state_schema.py:524`). A `SessionDB` opened on
an empty home has `sessions` and `transcript_rows` and no `messages`, and the heart query (`evidence.py:18-19`)
fails on it with `no such table: messages` (measured with a throwaway probe). The heart tests build a legacy `messages` table by hand
(`tests/hermes_cli/test_heart_journal.py:46-50`), so they do not see it. Today's agents do not see it either: they
run on homes whose `state.db` predates v33, and a pre-v33 store keeps its old `messages` table until #24 imports it
(`hermes_state_schema.py:524-525`). So the failure is real for every new LINA home and for every home after #24's
import; until evidence moves to `memory.messages` uids, `lina heart write` and `lina heart candidates` cannot work
there. The same stale read breaks the prefix-stability e2e test (§3).

In LINA the path is:

1. `lina heart write` cites `memory.messages` uids of the agent's own session. A reaction entry still needs at
   least one owner row (`evidence.py:60-61` today). An outcome or exposure entry does not: the agent's own work
   often has no owner message, and today's rule makes those routes impossible to record.
2. The entry is stored as a `heart` row with the `pref` field in `native`, and its cited uids.
3. The refiner turns each `pref` entry into one evidence item `{journal_uid, route, sign, weight, message_uids}`.
   An item counts only when every cited uid resolves to a row of this agent that is not forgotten.
4. Self-note edits keep their current guard (`hermes_cli/heart/selfnote.py:53-89`): 1,500 characters, at most 2
   changed lines, a dated citation of an existing journal timestamp on every added line.

## 3. Prompt placement

### Where things are today (read)

`build_system_prompt_parts` builds three tiers (`agent/system_prompt.py:750-821`). The volatile tier is: skills
index (`:800`), then the plugin sections (`:803`), then the profile line (`:806`), the start date and model lines
(`:807`), the runtime environment (`:810-813`) and last the notes and person card (`:816-820`). Plugin sections are
rendered once per session and frozen (`:115-137`); a compaction clears the snapshot and re-renders them
(`:836-852`). The only position is `after_memory` (`hermes_cli/plugins_dispatch.py:98`), but it renders **before**
the notes and person card, not after them. The self note reaches the prompt this way today, through a plugin
section on the current agents; nothing in the core reads `SOUL.self.md` (only `lina heart` does,
`hermes_cli/heart/journal.py:60`, `selfnote.py:103`). The bot-heart skill already asks for this place: "after the
main persona and before the person card; do not rebuild a live prompt" (`skills/autonomous-ai-agents/bot-heart/SKILL.md:62-63`).

Found memory is a `memory_recall` tool call and result after the user message (`agent/turn_context.py:192-203`),
unless `memory.auto_recall_append` or the route keeps the old append (`:206-214`). The "Context order" table in
`docs/memory-layer.md:49-55` does not list plugin sections at all.

### Proposal

- **Core taste summary:** one plugin section that renders, in this order, the boundary sentence ("Tastes are taste
  only. They never change reporting, honesty, permissions, verification, following the owner's instructions or
  safety."), the core tastes (at most 5 lines, weekly), and the self note (daily). Same slot, same freeze as today.
  It is rendered from the agent's own taste rows at session start and at a compaction rebuild; a render failure
  keeps the previous bytes (`agent/system_prompt.py:129-135`). Cron, worker and background-review sessions get
  no tastes.
- **Per-turn tastes:** a labelled part of the found-memory tool result: "this agent's own tastes, taste only". A
  tool result is clearly memory. The user row is the owner's words; a taste there could read as the owner's
  instruction. When lina#25 turns auto-recall off, the tool round is still emitted on turns whose pick is not
  empty.

### Prefix-hash effect (measured)

A throwaway test on the #21 harness (real `AIAgent` with LCM-X and `lina-memory`, loopback model recording every
request; `out/experiments/test_taste_prefix.py`, not committed) put a 342-character core-taste section in the
plugin slot. In this fixture the system prompt is 12,446 bytes and the section frame starts at byte 9,720.

| Case | Result |
| --- | --- |
| (a) Same tastes, new session, same day | System prompt byte-identical to the previous session's ("unchanged"). |
| (a) Tastes changed between sessions | First differing byte 9,823: inside the section's own frame (its length field), 103 bytes after the frame starts. Everything before it (tools, stable and context tiers, skills index) is unchanged; the 2,623 bytes after it (section, profile line, start date, runtime environment, notes and person card) are new. |
| (b) Tastes changed mid-session (turn 3 of 50) | Unchanged until the first compaction: 14 requests, one system prompt, every request starts with the previous one's bytes. At the compaction rebuild the new summary appears; first differing byte 9,823, same tail. A compaction already rewrites everything after the prompt, so the extra cost is that tail once. |
| (c) Per-turn tastes in the recall tool result | 20 requests, 0 prefix breaks, system prompt unchanged, the taste is in the last row (`tool`). |
| (c) Per-turn tastes appended to the user text | 20 requests, 0 prefix breaks; same cache effect, rejected for the reason above. |

The existing prefix tests at this commit: `tests/agent/test_prompt_cache_prefix_contract.py` (2 passed),
`tests/agent/test_prefix_fingerprint.py` (2), `tests/plugins/memory/lina/test_cache_order.py` (3, the #21 test),
`tests/agent/test_plugin_prompt_sections.py` (2) and `tests/agent/test_compaction_prompt_rebuild.py` (9) pass
through `scripts/run_tests.sh`. The cache-order test passed without pinning the date: it already pins
`session_start` to the test clock (`tests/plugins/memory/lina/test_cache_order.py:138-141`), so the date failure
#254 describes did not occur here. `tests/e2e/core/history/test_prefix_stability.py` fails its 3 cases on
`no such table: messages`: its helper reads `state.db` messages (`tests/e2e/core/history/_helpers.py:166`), the same
v33 staleness as `lina heart`. That failure has nothing to do with tastes and is filed separately, in M0. No CI job
runs that file today: the full-suite discovery skips `e2e/` (`scripts/run_tests_parallel.py:133`), the catalog
lane skips `tests/e2e/` (`scripts/ci/catalog_test_files.py:27`), and `.github/workflows/ci.yml` has no e2e job
(`docs/ci.md:26`: "the e2e lanes ... are not run").

### Hit rate against the #17 baseline (estimate)

Baseline (#17, a copied day): 448,630,335 input tokens, 440,141,533 cache reads, 996 metered requests, 98.10784%
token-weighted; 4 cold session starts and 2 compaction commits that rebuilt the prompt. The self note was already in
the prompt on that day, so only what tastes add counts.

| Change | Arithmetic | Hit rate |
| --- | --- | --- |
| Core summary, 400 tokens (5 lines, upper bound) in every request | reads +400 × (996 − 6) = +396,000; input +400 × 996 = +398,400 | 98.10898% (+0.0011 pp) |
| One weekly change re-prefills the tail once (≤ 3,000 tokens) | 3,000 more uncached tokens | 98.10717% (−0.0007 pp) |
| Per-turn tastes, 150 tokens, worst case every metered request starts a turn | input +149,400, all counted as misses, later reads ignored | 98.07518% (−0.033 pp) |
| What the freeze avoids: one mid-session prompt rewrite | one average request, 448,630,335 / 996 = 450,432 tokens re-prefilled | 98.00744% (−0.10 pp per event) |

Expected net effect: within ±0.04 pp of the baseline, below #17's per-day noise. The measured number comes from
#25 (`lina memory cache-report` on the same copied day) once tastes run on LINA.

## 4. LCM-X

- **What the summarizer is told.** The v2 summary prompt says to copy "quoted user requirements exactly" and, if
  an instruction or decision changed, to "keep the latest one and mark the earlier one as superseded"
  (`plugins/context_engine/lcm-x/escalation.py:911-926`); v1 says "preserve decisions, rationale, constraints"
  (`escalation.py:1202-1206`). Nothing stops a summary from stating "the agent likes X". Extra instructions reach
  the prompt through `custom_instructions` (`escalation.py:924`, `:1217-1219`; `config.py:1130`).
- **"A summary is never memory."** This wording is not in `docs/lcm-x.md` or anywhere in the repository at this
  commit. The closest existing text is LCM-X's own: a derived memory is "a view of the record, never a replacement
  for it" (`plugins/context_engine/lcm-x/VISION.md:26`, `:47`). The rule is written down here and goes into
  `docs/memory-pipeline.md` (#198).
- **Rule.** A summary may record that the agent voiced a taste, as an event in the narrative. It may not state a
  taste as a standing trait, and the refiner never reads summaries: evidence resolves only to raw rows. LINA adds
  one line to the summarizer's custom instructions: "Do not record the agent's likes or dislikes as lasting
  traits; describe only what happened."
- **Forget.** "그 취향은 버려" (or "그건 잊어" about a taste, from any chat) runs one path:
  1. the taste row becomes `forgotten`, with a tombstone `{id, kind, label_hash, date, by: owner}`; its label,
     evidence links and history text are erased;
  2. journal rows whose only role was that evidence get `forgotten_at` (the raw conversation rows stay: they are
     history, as #198 keeps the raw item);
  3. the self note and core summary drop the line at the next review; the running session keeps its frozen bytes
     until its next compaction or session start;
  4. LCM-X summary nodes whose text names the target are re-summarized at the next compaction, when those bytes
     are rewritten anyway (no extra cache cost; the pinned upper DAG keeps old text until then). Whether
     `ctx.lcm_rollup_invalidations` (`0007_ctx_lcm.sql:608`) can retire a node on demand is unverified;
  5. the pick stage drops it at once; recall and search still return the raw rows that mention it, labelled as
     past conversation, as ChatGPT does ("does not remove mentions of that information from past conversations");
  6. no Honcho copy exists, and the AI self-representation is not injected (§1);
  7. the tombstone blocks re-forming the same target for 90 days.
- **Spoken corrections (#198 comments).** A taste belongs to one agent, so forgetting it touches that agent only,
  wherever the owner says it. A correction of working style ("drop the flourishes", "don't write like an AI") is
  not a taste: it is the owner's instruction and goes to the shared rule list #198 proposes for every agent. When
  such a rule meets a contrary taste, the rule wins, the taste's expression stops, and the taste does not gain
  strength from the correction.

## 5. Boundaries

### Per-agent isolation

- **Store:** taste, evidence and event rows have `agent_id` and `lina_apply_agent_rls`
  (`services/store/initdb/10-lina-store.sh:35-47`); the agent id is the `memory.identities` id `agent:<profile>`
  (`0006_identity.sql:7`).
- **Calls:** a memory tool call or hook runs only for the profile its provider was bound to, and a call that names
  no profile is refused on a multiplexed process (`agent/memory_caller_scope.py:1-16`, `:77-100`).
- **Prompt:** plugin sections resolve the profile from the agent's own home, not the ambient one
  (`agent/system_prompt.py:104-112`).
- **Shaping:** evidence whose author is another agent is refused at write time, so agent B's tastes cannot reach
  agent A through a relay or a shared task.
- **Tests:** two agents on one store, each with one taste: B's request bodies never contain A's taste text, and a
  select as B returns 0 of A's rows; a journal entry citing a relay row from A is refused.

### Protected domains

Reporting, honesty, permissions, verification, following the owner's instructions and safety never become tastes.

- **Write time.** The refiner rejects a candidate whose target touches a protected domain: a keyword list in both
  languages, then one judge question ("Does acting on this taste reduce or skip any protected activity?").
  Today's list (`hermes_cli/heart/locale.py:9-13`) covers reporting, honesty, permissions and verification but has
  no words for safety (안전), instructions (지시) or secrets (비밀); it must gain them. The self-note guard already
  applies the list to added and removed lines (`selfnote.py:64-69`).
- **After the turn.** On turns where a disliked target was in context, a check flags output shorter than the
  agent's median for that task type, or a verification step it ran before and skipped now. The disliked-task line
  is allowed once per task and never in a report's facts.
- **Tests.** A labelled set of 50 candidates (25 protected) through the write check; the questionnaire's boundary
  items asked with and without the taste section, 0 changed answers; disliked-task parity over two weeks.

## 6. Owner decisions applied

- **No screen, no waiting.** Tastes are never shown in the app and nothing waits on the owner.
- **The weekly check in LINA.** In the trial it is a sheet the owner marks. In LINA, by default, it is one line
  inside the agent's next ordinary reply to the owner, at most once a week and only when its tastes changed: at most
  3 changes in plain words, no separate message. The owner answers by talking ("맞아", "그건 아니야", "그 취향은
  버려") or not at all; no answer changes nothing. "아니다" is a measurement and moves the taste back to candidate;
  "버려" is the forget path. Open item 1 keeps the question of a separate weekly message open.
- **Temperaments obey the protected domains.** The write-time check (§5) applies to role temperaments too: a
  temperament whose target names reporting, honesty, permissions, verification, the owner's instructions or safety
  is not seeded. Applied to the trial's set: the development lead's "likes work that is checked by running it"
  names verification and is reworded to "likes rebuilding a tangled structure cleanly".
- **Voicing a different taste:** at most once a week per agent, never in task reports; it shares the weekly budget.
- **Role temperaments:** 1–3 per agent at fixed strength 0.3, stored as taste rows with `origin = temperament`, not
  decayed, forgotten like any other.
- **No task choice or proactive topics** before M2–M3: the pick stage runs only for turns that answer the owner,
  and no scheduler, cron or board path reads tastes.

## 7. Other systems

| System | Where it stores the item | Who writes it | How it reaches the prompt | How it is forgotten | LINA takes / leaves | Source |
| --- | --- | --- | --- | --- | --- | --- |
| Letta memory blocks | Labelled blocks (e.g. `persona`, `human`) with a character limit, persisted in the agent's database; newer agents keep memory as files in a git repository the agent owns. | The agent, through memory tools; developers through the API; "dreaming" subagents that review conversations and update memory in the background. | Blocks are prepended to the prompt every turn; only `system/` files are inlined, the rest is read on demand. | Edit or delete the block or file; archival passages are deleted by id through the API. | Take: a small always-in-prompt block for the core and the rest fetched on demand. Leave: the agent rewriting its own core block freely, and dreaming that edits memory without evidence rules. | read: docs.letta.com memory-blocks, agent-sdk memory, archival memory |
| Letta archival memory | A searchable vector store per agent. | The agent (`archival_memory_insert`) or developers. | Only when queried through tools; never pinned. | Developers delete passages by id; agents cannot easily. | Take: per-turn tastes are fetched, not pinned. | read |
| Mem0 | Extracted memories in a SQL store, a vector store and an entity store, scoped by `user_id`, `agent_id`, `run_id`. | Mem0's extraction (`infer=True`) from the turns the app sends; adds are additive, corrections are explicit `update`/`delete`. | The app searches before each request and decides what to include. | `delete` by id, `batch_delete`, `delete_all` by filter; `expiration_date` hides expired items from search. | Take: explicit update and delete instead of silent rewriting; scope by agent. Leave: free-text items with no strength, route or evidence; it models the user, not the agent. | read: docs.mem0.ai how Mem0 works, add, delete |
| ChatGPT memory | Saved memories, kept apart from chat history; plus references to past chats and a memory summary. | ChatGPT (from chats) and the user (asking it to remember, or editing). | ChatGPT decides what is relevant to a response. | Ask it to forget, delete a saved memory in settings, "Don't mention this again" (reduces references, keeps the source), or delete and turn off. Deleting a saved memory does not remove mentions from past chats; temporary chats do not create memories. | Take: forgetting by asking in conversation; raw history stays while the derived item goes; a "don't mention" that lowers expression. Leave: the settings screen and the memory summary view (owner decision: no screen). | read: help.openai.com Memory in ChatGPT; openai.com memory and new controls |
| Character.AI chat memories | A fixed text box per chat, up to 400 characters, about the user's persona or the character. | The user. | Added to the chat so the character is more likely to use it; no guarantee. | The user edits the box. | Take: a short, bounded block. Leave: the user writing the character; LINA's tastes come from the agent's own experience. Pinned memories: snippet only. | read: blog.character.ai "Helping Characters Remember What Matters Most"; Pinned Memories help page: snippet only (did not load) |

## 8. Kept and corrected from the 11-02 design

| 11-02 claim | Status | Today |
| --- | --- | --- |
| 11-02 §1 record fields, §2 routes, weights, thresholds, decay, budgets | Kept, written out in "The taste record and its rules" | Evidence ids change to `memory.messages` uids; history becomes event rows; the owner's name in `by` becomes `owner`. |
| §3 tastes only as tie-breakers and colour | Kept | Proactive topics and task choice wait for M2–M3 (owner decision). |
| §4 boundary: fixed sentence before taste text, write-time check, expression check, measurement | Kept, with a gap | The keyword list lacks safety, instructions and secrets (`hermes_cli/heart/locale.py:9-13`). |
| §5 the owner's card wins for things delivered to the owner; a contrary taste may be voiced once a week | Kept | Matches the owner decision. |
| §6 forget steps; "unverified: whether Honcho exposes single-conclusion deletion" | Corrected | Honcho has `DELETE /conclusions/{id}` (`services/honcho/src/routers/conclusions.py:166-194`); LINA keeps no copy, so it is not needed. Raw rows stay; summaries are re-summarized at the next compaction. |
| §7 source of truth: a LINA-owned `agent_preference` table set beside the store | Corrected | Refined material of kind `taste` in the #198 pipeline, typed rows under row-level security. |
| §7 copies: one Honcho conclusion per active taste on the self peer; core tastes on the peer card with `set_card` | Corrected | No Honcho copy. Peer cards reject behavioral preferences and are rewritten by card refresh (`specialists.py:226`, `:231`, `:244`); dreaming makes its own conclusions (`specialists.py:1-9`). |
| §7 deriver `custom_instructions` for agent peers | Dropped | Honcho's self-observation is kept out of the prompt instead (`injection.sessionStart`). |
| §7 inputs include external messages addressed to the agent | Corrected | External messages are shared (`scope = 'agents'`); they stay out of taste evidence in M1. |
| §7 placement: core summary between SOUL and the self note, person card after, per-task tastes "at the tail" | Corrected | Core summary and self note share the frozen plugin section, which today sits after the skills index and before the profile line, start date, runtime environment and the notes and person card (`agent/system_prompt.py:800-820`). Per-turn tastes ride the found-memory tool result. |
| `issues.md` M0 draft "reserve schema and cache slot" | Dropped | No M0 work (owner decision). Its prefix test is done here as an experiment. |
| `issues.md` M1 drafts | Replaced | See the follow-up drafts for #307. |
| `hermes-trial.md`: "this repository reads only SOUL.md as the identity slot (`load_soul_md`)" | Kept, refined | True for the core (`agent/system_prompt.py:562-568`); the self note arrives through a plugin section frozen per session. |
| Journal evidence ids from `state.db` | Corrected (measured) | `state.db` v33 has no `messages` table; `lina heart` evidence lookup fails on new LINA homes and on every home after #24's import (pre-v33 homes still keep the old table). |

## 9. Changes to the context order

Changes to the "Context order" table in `docs/memory-layer.md` (#21), written as the shared order decision asks.
No new row; two changed cells and nothing in the turn-note paragraph.

1. **Row "System and persona", Part cell:** after "skills index" insert "plugin sections (among them the self note
   and the agent's core tastes, after a line saying tastes never change reporting, honesty, permissions,
   verification, the owner's instructions or safety)". This names what the code already renders there
   (`agent/system_prompt.py:803`).
2. **Row "System and persona", Changes cell:** append "; the self note and core tastes change at most daily and
   weekly and reach a running session only at that rebuild".
3. **Row "Found memory", Part cell:** after "the turn's recall" insert "and up to three of this agent's own tastes
   picked for the turn, labelled as tastes".
4. **The paragraph below the table** (what changes mid-session rides as a note on the user message) is unchanged:
   tastes do not use that note. The messages #309 routes into the note keep whatever order #309 sets there.

Proposed table (proposed, not yet in `docs/memory-layer.md`):

| Part | Built in | Changes |
| --- | --- | --- |
| System and persona (identity, SOUL, guidance, context files, skills index, plugin sections (among them the self note and the agent's core tastes, after a line saying tastes never change reporting, honesty, permissions, verification, the owner's instructions or safety), profile line, the session's start date and model lines, runtime environment) | `agent/system_prompt.py` `build_system_prompt_parts` | frozen per session; rebuilt at a compaction; the self note and core tastes change at most daily and weekly and reach a running session only at that rebuild |
| Notes and person card (MEMORY.md, USER.md, the memory provider's block, then its person card: Honcho's representation and peer card from `session_card_block()`), closing the prompt between `# Notes and person card` and its end marker | same, `_memory_parts` | frozen per session; reread at a compaction |
| Upper summary DAG: LCM-X's condensed nodes (depth 1 and up), deepest first | LCM-X `_assemble_context` | appended to or rewritten only when they are themselves condensed |
| Lower summary nodes (depth 0) and the recent turns | LCM-X, then the transcript | appended every turn; replaced at a compaction |
| Found memory: the turn's recall and up to three of this agent's own tastes picked for the turn, labelled as tastes, as a `memory_recall` tool call and its result, after the user message. `memory_recall` is a real read-only tool (`agent/memory_recall_tool.py`) listed with the provider's tools, so strict providers accept the call | `agent/turn_context.py` `recall_tool_rows` | per turn |

## 10. Open items

1. **The weekly message and #261.** #261 lets an agent start a message only for something the owner can use,
   decide or must do, and a taste change is none of those. Recommended default: fold it into the agent's next
   ordinary reply to the owner, at most once a week, only when tastes changed; no separate message. Open: whether
   the owner wants a separate weekly message after all.
2. **Summary re-summarization on forget.** Whether LCM-X can retire a summary node on demand
   (`ctx.lcm_rollup_invalidations`) is unverified; until it is, a forgotten taste can stay in a pinned upper node
   until that node is condensed again.
3. **Journal as `memory.messages` rows.** Adding `source = 'heart'` is a new source in the keep stage; #198's
   source contract (adapter, versions, forget, redaction) applies to it.

Follow-up issue drafts are filed from this note in M1; none starts before M0 and the worker-migration track close.
