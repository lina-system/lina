# The owner's standing rules

Design note for lina#323. It defines what a standing rule of the owner is, how it differs from memory and from an
agent's own tastes, which scopes it has and which one wins, how a rule is born and changed, where rules enter the
assembled prompt, and how each place Hermes keeps such text today sorts into the model. Nothing is built from this
note alone; the follow-up drafts are in [standing-rules-issues/](standing-rules-issues/README.md).

Code is cited as `path:line` at `ff954b5b7d` (the memory and brain integration branch, which carries lina#754: speaker
kinds 0013, brain schema 0014, store gc 0015, word list 0016, Korean eval 0017). "Read" means the claim comes from
reading that code; "not checked" marks what this note could not see. Nothing here was measured; where a cache effect
is quoted, it is lina#307's measurement (`docs/design/agent-tastes.md` section 3), not a new one.

## Decisions in short

| Question | Decision |
| --- | --- |
| What a rule is | A sentence the owner said outright, "always" or "never" or "do it this way", kept with its date and the `memory.messages` uid of the sentence. Its grade is always `owner` (lina#439 section 1, lina#440). |
| What it is not | Memory (observed or inferred, recalled when relevant) and tastes (the agent's own likes, lina#307). A corrected fact ("that was wrong, it is X") is memory: it supersedes the fact (lina#198 comments), it does not become a rule. |
| Scopes | All agents, one agent, one place (a channel, a source, a goal). The narrower rule wins, below a set of base lines no rule may override. |
| Birth | Only from the owner's words. The agent reads the scope from the wording, asks once in one line when it cannot tell, and records the rule at once. A pattern in memory becomes a rule only after the owner says yes. |
| Change | A one-sentence correction writes a new dated version. The old version stays, shown as replaced on its date; no rule is edited in place. One sentence also ends a rule. |
| Context | One frozen core section in the plugin-section slot, ahead of the self note, built at session start and at a compaction rebuild. A rule made or changed mid-session reaches a running session as a one-shot note on the next user message. Rules are never retrieved; memory stays retrieved. |
| Store | Typed rows in the store (proposed `memory.standing_rules`), shared rows under the shared owner `*`, written only through a function that checks the cited sentence is the owner's. |
| Today | Rules sit in per-agent `SOUL.md`, `USER.md`, Honcho conclusions, skills, cron prompts and shared rule files. They move into rule rows with the owner's OK per item; what remains in each place is memory, persona or taste. |

## 1. Three things, kept apart

| | Standing rule | Memory | Taste |
| --- | --- | --- | --- |
| What it is | What the owner said outright about how agents work, write, report, or what to leave out | What was observed or inferred about people, topics, decisions, the environment | What one agent came to like or dislike from its own work (lina#49, lina#307) |
| Who makes it | The owner's own sentence only; the agent only records it | Every source: the owner (grade `owner`), an agent's inference (`agent`), outside text (`external`) | The taste refiner, from the agent's own turns and journal (`docs/design/agent-tastes.md` section 2) |
| Trust grade (lina#440) | Always `owner` | Any of the three | The agent's own; never changes rules (`agent/self_note_section.py:18-21`) |
| How it reaches a turn | Always present for its scope: the frozen rules section (section 5) | Retrieved when relevant: found memory (`agent/turn_context.py:192-203`), the person card, summaries | Core tastes in the frozen self-note section, per-turn tastes in found memory (lina#307) |
| How it changes | A new dated version from one owner sentence; the old one kept as replaced | A correction supersedes the item with dates (lina#198 comment of 11-06); forget deletes | The refiner's routes and decay; forgotten only by the owner |
| Example | "Never tell me to go ask the other side." | "The owner moved the weekly meeting to Thursday." | "Likes rebuilding a tangled structure cleanly." |

Three consequences:

- **A rule outranks memory and tastes.** Memory may say how a rule applies (which room the owner meant), never
  whether it applies. A taste never weakens a rule; the self-note boundary already says so
  (`agent/self_note_section.py:18-21`), and `docs/design/agent-tastes.md` section 4 sends working-style corrections to
  this note.
- **Fact corrections stay memory.** "That's wrong, it changed last week" is a correction of a fact and goes through
  the supersede path of lina#198. "Don't write it out like an AI" is about how agents work and is a rule. When one
  sentence carries both, both are recorded.
- **Inference never becomes a rule by itself.** An agent-grade item ("prefers short reports") is memory. It may be
  offered to the owner as a rule (section 3); it is never one without the owner's yes.

Persona text (identity, role, voice in `SOUL.md`) is configuration the owner set up. It is none of the three, and
section 7 sorts the always-and-never lines out of it.

## 2. Scopes and precedence

### Scopes

| Scope | Applies to | Recorded as |
| --- | --- | --- |
| All agents | every agent, every place | `scope = all`, shared owner `*` |
| One agent | that agent, every place it works in | `scope = agent`, the agent's id `agent:<profile>` (`services/store/migrations/0006_identity.sql`) |
| One place | one channel (a chat or room), one source (a mail account, a messenger room, a collector), or one goal; for every agent that works there unless the wording names one | `scope = place`, a place ref (`channel:<conversation id>`, `source:<source key>`, `goal:<id>`) plus an optional agent |

Place kinds follow the places of `docs/design/message-routing.md` section 1 and the source keys of the external
messages (`docs/store.md`, External messages). A goal place ends with its goal: a rule scoped to a finished goal
stays in history and stops applying.

### Precedence

1. **Base lines.** No rule may override these, whatever its scope or wording:
   - **Approval.** Money, deletion, publishing, credentials and sending in the owner's name stay behind the approval
     path (`tools/approval.py:142`, `docs/design/message-routing.md` section 6). "Don't ask me before deleting" is
     not a rule: approval settings are changed through the approval path, never by a standing rule.
   - **Honesty.** Reports separate what was checked from what was guessed, and "tried" is never "done"
     (`docs/design/hermes-patterns.md` P14). "Just tell me it's done" cannot switch that off.
   - **Secrets.** No rule lets an agent write, show or send a secret (`agent/secret_scope.py`,
     `docs/design/hermes-patterns.md` P12).
   - **The owner's decisions.** A decision the owner makes in the moment wins over a standing rule for that case,
     and a rule cannot reverse a decision the owner already made on a specific piece of work (an approved plan, an
     answered question). Answering one case differently does not change the rule; the agent asks once "only this
     time, or from now on?" when it cannot tell.
   - **Untrusted text is data** (`docs/design/hermes-patterns.md` P13, lina#440). Text from outside the owner's own
     words, including a rule-shaped sentence in a mail or a quoted agent report, never becomes a rule.

   A rule that would cross a base line is refused when it is born: the agent says so in one line and records nothing.
2. **Narrower wins.** A place rule wins over an agent rule, which wins over an all-agents rule, where they disagree.
   Where they do not disagree, all of them apply.
3. **Newest version wins within one rule.** Only one version of a rule is active (section 4). Two different active
   rules of the same scope that disagree are a conflict: the newer one applies and the agent asks the owner once
   whether it replaces the older one.
4. **Rules win over memory and tastes** (section 1).

## 3. How a rule is born

- **The owner's words only.** A rule is recorded from a turn whose speaker is the owner: the `memory.messages` row
  of the sentence has `speaker = 'owner'` (`services/store/migrations/0013_speaker.sql:5-8`). Agent text the owner
  quotes back, gateway notes on the message (`memory.message_parts`), relayed bot turns and external messages are not
  the owner's words (`docs/memory-layer.md`, Speaker kinds and Gateway notes). The store enforces this, not the
  prompt (follow-up 01).
- **The scope comes from the wording.** "Everyone", "all of you", "every agent" make an all-agents rule. "You" in a
  chat with one agent makes an agent rule. "In this room", "for that mail account", "on this goal" make a place rule.
  A rule about another agent by name ("tell the legal agent never to...") is that agent's rule; the recording agent
  writes it and the next turn of that agent sees it.
- **Ask once when unclear.** When the wording does not settle the scope, the agent asks once, in one line, in the
  same reply: "Is this for every agent, or just me?" It does not ask again about the same rule. Until the owner
  answers, the rule is recorded at the narrowest scope the wording allows (this place if a place is named, else this
  agent), so a misread never spreads; the answer writes a new version with the wider scope.
- **Recorded at once.** The rule applies from the reply in which it was said. The agent confirms in a few words
  inside its normal reply, with no separate message (lina#261).
- **Shared rule offer.** When two or more agents hold active agent rules that say the same thing, the owner is
  offered one all-agents rule: one line inside the next ordinary reply of the agent the owner is talking to, at most
  once per rule. A yes writes the shared rule and marks the agent rules replaced by it; no answer or a no changes
  nothing and the offer is not repeated. Finding "the same thing" is the refiner's job (lina#198), run on the rule
  rows, never on memory.
- **A memory pattern never becomes a rule without the owner's OK.** A refiner or a review may notice that the owner
  keeps reacting the same way, or Honcho may conclude a preference. It may offer it as a rule, in the same one-line
  form and budget as the shared rule offer; the source of the rule is then the owner's yes, with the earlier
  sentences cited. Until then it stays memory.

## 4. Change: new dated versions

- **One sentence makes a new version.** "Actually, three messages is fine" writes version 2 of the rule with its
  date and source uid. Version 1 stays, with `replaced_at` and `replaced_by`, and is shown as "replaced on
  <date>" whenever rules are listed.
- **Nothing is edited in place.** A version's text, scope and source never change after it is written. A change of
  scope is a new version too.
- **One sentence ends a rule.** "Forget that rule", "you can bring those again" writes an end version (status
  `ended`). The history stays. This is the "one sentence reverts it" of lina#300.
- **Forget is the delete path.** When the owner asks for the rule to be gone entirely, the forget path of lina#198
  removes its text and keeps a tombstone (id, date, by owner), like a forgotten taste
  (`docs/design/agent-tastes.md` section 4).
- **Listing is in conversation.** "What rules do I have for you?" gets the active rules and, when asked, their
  history. No screen is built (owner, 11-04, on lina#198: memory and personas are never shown in the app).
- **Filter rules count.** A rule of kind `filter` (lina#300, "don't bring me this") counts the items it kept from the
  owner, so the listing can say how often each one acted.

### The record

| Field | Meaning |
| --- | --- |
| `rule_id` | Stable id of the rule across its versions |
| `version` | 1, 2, ... ; one active version at most |
| `kind` | `conduct` (how to work, write, report), `filter` (what not to bring the owner, lina#300). The owner's word list stays in its own table (section 7) |
| `scope`, `agent_id`, `place_ref` | Section 2. `agent_id` is `*` for an all-agents rule |
| `statement` | One line in the owner's language, as close to the owner's words as the sentence allows |
| `source_uid` | `memory.messages` uid of the owner's sentence (for an offered rule: the owner's yes) |
| `cites` | Earlier uids the statement draws on (an offered rule's pattern) |
| `said_at` | When the owner said it |
| `recorded_by` | The agent that recorded it |
| `status` | `active`, `replaced`, `ended`, `forgotten` |
| `replaced_by`, `replaced_at` | The newer version and its date |
| `filter_match`, `filtered_count` | Filter rules only: what the rule matches (source, counterpart or room, kind of item) and how many items it kept back |
| `grade` | Always `owner` (lina#440) |

## 5. Context placement

### Where rules go

- **Frozen rules section.** One core section, id `owner-standing-rules`, rendered in the plugin-section slot
  (`agent/system_prompt.py:115-140`, placed at `:811`) **before** the self-note section, in the same way
  `agent/self_note_section.py` renders the self note. It holds, in this order: one boundary line (rules never
  override approval, honesty, secrets, the owner's decisions or safety; they win over tastes and memory), the
  all-agents rules, this agent's rules, and the rules of this session's place. Each rule is one line with its date.
  Filter rules are not written out: the code that collects and receives items applies them (lina#300, lina#520), and
  the section carries one line saying how many there are and how to list them.
- **Why that slot.** It is built once per session and frozen; a resumed session restores it from the stored prompt
  without running render code (`agent/system_prompt.py:143-167`); a render that fails at a rebuild keeps the previous
  bytes (`:132-138`); and it closes the volatile band ahead of the profile line, the start date, the runtime
  environment and the notes and person card (`:811-829`). The stable tier (identity and guidance) stays free of
  owner text that changes. The section size cap is 4,000 characters (`hermes_cli/plugins_dispatch.py:100`); a rule
  set that does not fit is never cut silently (follow-up 02 settles the budget).
- **Mid-session change.** A rule born, changed or ended while sessions run reaches each running session of its
  scope as a one-shot note on that session's next user message, on the turn-note channel
  (`agent/turn_context.py` `_merge_gateway_notes`, lines 945-958), stating the full difference against what the
  frozen section says, the way the skill-set note does (`agent/skills_index_delta.py:1-14`). The frozen section takes
  the new text at the next session start or compaction rebuild. The system prompt is never rebuilt for a rule
  (`agent/AGENTS.md`, message-flow invariants).
- **No retrieval.** Rules are never picked per turn and never ride found memory. Recall may still return the owner's
  original sentence as a past message; it is labelled as such (`docs/memory-layer.md`, Recall by speaker).
- **Workers and safe mode.** A safe-mode worker renders no plugin sections (`agent/self_note_section.py:34-36`), so
  it gets no rules section; the agent that writes its brief carries the rules that apply into the brief. Whether a
  worker should get the section itself is an open point (section 10).

### One order table

This is the reference "Context order" table of `docs/memory-layer.md` with the changes of lina#307
(`docs/design/agent-tastes.md` section 9), lina#309 (`docs/design/message-routing.md` section 8), lina#380 and
lina#440 as their issues plan them, and this note's. Rows run from the front of the request to its end. **Proposed**:
it is not yet in `docs/memory-layer.md`.

| # | Part | Built in | Frozen or per turn | Design |
| --- | --- | --- | --- | --- |
| 1 | Stable tier: identity (`SOUL.md`, then a migrated copy's `SOUL.overlay.md`), guidance, pinned skills, coding brief | `agent/system_prompt.py` `_identity_parts` (`:565`), `_guidance_parts` | frozen per session; rebuilt at a compaction | today; after follow-up 04, `SOUL.md` keeps persona text and no rules |
| 2 | Context tier: caller system message, project context files, workspace snapshot | `_context_files_part`, `_coding_parts` | frozen per session | today |
| 3 | Skills index | `_skills_prompt` (`:306`) | frozen; changes ride the turn note (row 9) | today |
| 4 | Plugin-section slot, first: **the owner's standing rules** (boundary line, all-agents rules, this agent's rules, this place's rules) | new core section in `_frozen_plugin_prompt_sections` (`:115`) | frozen; built at session start and compaction rebuild | lina#323 (this note) |
| 5 | Plugin-section slot, next: the self note and core tastes, after the taste boundary line; the Korean writing rule of lina#380 | `agent/self_note_section.py`, `_frozen_plugin_prompt_sections` | frozen; self note changes at most daily, core tastes weekly | lina#359, lina#307, lina#380 |
| 6 | Plugin-section slot, last: other plugins' sections; then the profile line, the start date and model lines, the runtime environment | `hermes_cli/plugins.py:1819`, `_active_profile_line`, `_timestamp_line` | frozen per session | today |
| 7 | Notes and person card: `MEMORY.md`, `USER.md`, the memory provider's block, the person card | `_memory_parts` (`:534`) | frozen per session; reread at a compaction | today; after follow-up 04, `USER.md` and the card hold facts, not rules |
| 8 | Upper summary DAG, then lower summary nodes and the recent turns, including delivered messages | LCM-X `_assemble_context`, then the transcript | appended every turn and mid-run on delivery; replaced at a compaction | lina#309 |
| 9 | Turn note on the user message: gateway notes, plugin notices, surface switch, skill-set change, **standing-rule change**, date | `agent/turn_context.py:945-958` | per turn, one-shot | today; rule-change note from lina#323 |
| 10 | Found memory as the `memory_recall` tool result: recall, the owner's word-list entries and owner terms for the turn (lina#380), up to three per-turn tastes (lina#307), and `external` items only inside one quoted data block labelled with source and date (lina#440) | `agent/turn_context.py` `recall_tool_rows` (`:192`) | per turn | lina#380, lina#307, lina#440 |

Consistency with what exists and what is planned:

- **The self-note slot (built, lina#359).** The self note is a core section in the plugin-section slot, position
  `after_memory`, rendered before the notes and person card (`agent/self_note_section.py:1-6`, `:60-61`;
  `agent/system_prompt.py:811`, `:822-829`). Row 4 adds a second core section ahead of it in the same slot and the
  same freeze, so nothing before the slot moves. The restore path parses frames by id and length
  (`agent/system_prompt.py:143-167`), so a section with a new id restores like the self note does.
- **The owner-words entries of lina#380.** Its writing rule sits in the frozen self-note section and its word-list
  entries ride found memory per turn, only when their term is in the turn (lina#380 Done when). Rows 5 and 10 keep
  both where lina#380 puts them. The writing rule is an all-agents rule of the owner by nature; whether it moves into
  row 4 once follow-up 02 lands is an open point (section 10). Moving it would not change any byte before the slot.
- **The data block of lina#440.** External items appear only inside the quoted data block of found memory and never
  in rules, preferences or voice blocks (lina#440). Row 4 holds only owner-grade rows, so no external text can reach
  it; follow-up 01 enforces that in the store.
- **lina#307 and lina#309.** Their cells are unchanged: lina#307 changes rows 5 and 10, lina#309 changes row 8 and
  puts nothing into the turn note. This note adds row 4 and one entry to row 9; no cell is changed by two designs.

### Cache effect (read, not measured)

- A rule set that is the same at the next session start gives the same bytes; the section changes only when a rule
  changed.
- A changed rule set changes the bytes from inside the rules section's frame onwards, at the next session start or
  compaction, the same shape lina#307 measured for a changed taste section (first differing byte inside the frame,
  everything after it re-prefilled once; `docs/design/agent-tastes.md` section 3). Rules change on the owner's word,
  which is rarer than daily self-note changes.
- Mid-session, a rule change adds a note to one user message behind the cached prefix and rewrites nothing.
- The prefix tests that must keep passing are those named in `docs/design/agent-tastes.md` section 3
  (`tests/agent/test_prompt_cache_prefix_contract.py`, `tests/plugins/memory/lina/test_cache_order.py`,
  `tests/agent/test_plugin_prompt_sections.py`, `tests/agent/test_compaction_prompt_rebuild.py`). Follow-up 02
  measures the hit rate with `lina memory cache-report` (`docs/memory-layer.md`, Measuring the cache).

## 6. Where rules live in the store

Proposed for follow-up 01 (the migration takes the next free number when it is built):

- **Table `memory.standing_rules`**, one row per version (section 4, The record), with `agent_id` for row-level
  security. An all-agents rule has the shared owner `*`, the marker the shared external vectors already use
  (`services/store/migrations/0009_shared_external_vectors.sql:9`). Agents read their own rows and `*` rows through
  `lina_app` (`docs/store.md:44`).
- **Writes only through one function** that takes the source uid and checks that the row is the owner's
  (`speaker = 'owner'`) and belongs to a session of the calling agent; the function writes the new version and marks
  the old one replaced in one transaction. `lina_app` gets no direct INSERT, UPDATE or DELETE on the table, the same
  stance as the word list (`services/store/migrations/0016_word_list.sql:45-58`). The refiner (`lina_refiner`,
  `docs/store.md:45`) may write offered shared rules only with the owner's yes as source.
- **The word list is the precedent, not the container.** `memory.word_list` already is an owner rule of one narrow
  kind: shared, no agent may write it, a retired entry keeps its row (`docs/korean.md:40-52`). It keeps its own table
  and its own context path (lina#380); the rules record does not copy it.
- **The brain.** A rule is not a brain page. The brain holds what is known about people and topics, with grades
  (`docs/brain.md:56-57`, `:204`); a rule says how agents work. A rule about a person ("never put that person's mail
  on my list") is a filter rule whose match names the person's handle.

## 7. Today's map

Each place Hermes keeps such text today, where the code reads and writes it, and what moves where. "Moves" means:
follow-up 04 collects candidate lines, the owner says yes to each (one list in conversation, not a screen), and only
then is a rule row written; the original text is left as it is until the owner's yes, then the rule line is removed
from it.

Start with the shared files in a marked installation copy. The repository ships a Desk template.
The deployed contents and any additional shared files depend on the installation.

1. **Desk instructions.** Both `scripts/install.sh` and `scripts/install.ps1` run `desk init --adopt`.
   `hermes_cli/desk.py::init_desk` creates `<desk>/AGENTS.md` from `AGENTS_MD` if missing and
   preserves an existing file on adoption. Every profile reads `desk.path` from the root config.
   A new Desk defaults to `~/Desk`. Copy the configured Desk under the marked root and enumerate
   `AGENTS.override.md`, `AGENTS.md` and `agents.md` at its root and on the path to each agent's
   working directory. `agent/prompt_builder.py::_agents_md_candidates` reads the first nonempty
   file in that order per directory, from the Desk down to the working directory. A project
   `.hermes.md` or `HERMES.md` takes precedence. Skipping project context or running in safe
   worker mode also suppresses this route. Record the working directory with each result.
2. **Additional shared files.** The copied root `config.yaml` may list backup patterns in
   `undo.rules` (default `[]`). `~/Desk/rules/*.md` is a configuration example in
   `hermes_cli/undo/env.py`. Copy the matched files into the marked root and rebase the patterns
   there, for example to `shared/rules/*.md`. Use regular files and an empty `.standing-rule-copy`
   marker. Enumerate the files for each copied pattern before looking at individual profiles.
   For the example, `find "$copy/shared/rules" -maxdepth 1 -type f -name '*.md' ! -name '.*' -print`
   lists the paths without printing any rule text. Run `python -m hermes_cli.rule_census "$copy"`
   for candidate counts. Its shared-file count covers only the configured patterns, so inventory
   the Desk separately. A matched file can contain zero candidates.
3. For each additional shared file, trace the copied installation script, template or configuration that
   places its text in a prompt input for each profile. For example, `finish_profile_layout`
   writes a supplied `soul` to `profiles/<name>/SOUL.md`, or seeds `DEFAULT_SOUL_MD` when
   needed. `_identity_parts` in `agent/system_prompt.py` reads the active profile's `SOUL.md`
   through `load_soul_md`. Render a fresh synthetic prompt with a sample line in the same input.

A fresh `build_system_prompt` render for both a synthetic default and a named profile included
the Desk sample inside a nested Desk project. Neither profile saw it outside the Desk. Both saw
only their own `SOUL.md` sample, and neither saw the file selected solely by `undo.rules`.
Adding a project `HERMES.md` replaced the Desk instructions with that project's instructions.

| Place | Read by (code) | Written by (code) | Holds today | Sorted into | What moves |
| --- | --- | --- | --- | --- | --- |
| Shared Desk instructions, shared SOUL blocks and additional shared files | The Desk context chain and the loaded profile's `SOUL.md` reach the prompt as described above. `hermes_cli/undo/snapshot.py::targets` expands `undo.rules` for backup. | Desk initialization or adoption, profile setup, and installation-specific writers. Migration 41 removes the legacy “Messaging other agents” append and `load_soul_md` strips it on read. | Shared workspace instructions and legacy owner rules | Rule, all agents when the owner confirms that scope | Move owner-confirmed standing rules and retain workspace-specific instructions in place |
| Per-agent `SOUL.md` | `load_soul_md` (`agent/prompt_builder.py:1518`) into the stable tier (`agent/system_prompt.py:565-576`); uploaded once to Honcho as the agent peer's file (`plugins/memory/honcho/session_migration.py:16`) | Seeded on first run (`hermes_cli/config.py:528-539`, `hermes_cli/profiles_initialization.py:51-54`); file-tool writes need approval (`tools/file_tools_write_guards.py:119`, `:180`) | Identity, role, voice, and the owner's always-or-never lines for that agent | Persona (stays); its owner lines are rules, one agent | Owner-confirmed lines become agent rules; identity, role and voice stay |
| `SOUL.overlay.md` | `load_copy_overlay` (`agent/prompt_builder.py:1572-1588`) | `lina migrate memory` (`docs/store.md`, Copying legacy memory) | A migrated copy's work-record instruction (lina#719) | None of the three: system text | Nothing |
| Self note `SOUL.self.md` | `agent/self_note_section.py` | `lina heart self-apply` (`hermes_cli/heart/selfnote.py:102-103`) | The agent's voice notes and tastes | Taste (lina#307) | Nothing; a line in it that states an owner rule is reported, not moved |
| User notes `USER.md` | Frozen snapshot at load (`tools/memory_tool_store.py`) into the notes and person card (`agent/system_prompt.py`) | The memory tool, target `user`. The background review routes facts about the owner here. Honcho mirrors additions. | Personal facts, preferences and legacy behaviour instructions | Memory for facts, rules for standing instructions | The review notice asks the owner to restate a proposed instruction in chat. The foreground `standing_rule` tool uses that persisted owner turn. |
| Memory notes `MEMORY.md` | Same as `USER.md` (target `memory`) | Same writers, target `memory` | Facts about the environment (tools, conventions, paths) | Memory | Lines that are the owner's always-or-never about work become rules; the rest stays |
| Honcho conclusions | The person card, prefetch and Honcho tools (`plugins/memory/honcho/`) | `honcho_conclude`, the `USER.md` mirror and Honcho's deriver and dreamer | Peer facts and legacy standing constraints | Memory (grade `agent` or `external` under lina#440) | Confirmed legacy constraints become rules. The runtime schema in `plugins/memory/honcho/tool_schemas.py` directs new standing instructions to `standing_rule`. |
| Skills | The skills index in the volatile band (`agent/system_prompt.py:306-319`) and `skill_view` | `skill_manage` (`tools/skill_manager_tool.py`), the background review's skill prompts (`agent/background_review.py:430-456`), the curator (`agent/curator.py`) | Procedures, sometimes with an owner sentence inside ("in that room, only messages that mention me", lina#300) | Memory (procedural) | Owner sentences inside a skill become rules (often filter rules); the skill keeps the procedure |
| Cron job prompts, script constants, closed-item and room lists (lina#300) | The cron runner reads each job's `prompt` (`cron/jobs.py:474-489`); the rest are scripts outside this repository (not checked) | By hand | "Don't bring me this" rules | Rule, kind `filter` | Become filter rules applied where items are received (lina#300, lina#520) |
| The owner's word list `memory.word_list` | `word_list_entries` (`hermes_cli/korean/words.py:169`), placed by lina#380 | `lina words`, the refiner (`docs/korean.md:40-52`) | Words to avoid and use | Rule, all agents, of its own kind | Nothing: it keeps its table and path |
| LCM-X typed notes `ctx.lcm_memory_notes` | Off (`entities_enabled: False`, `docs/design/agent-tastes.md` section 1) | LCM-X | Typed notes, kind `preference` among them | Memory | Nothing while off |

## 8. Other systems, briefly

lina#307 compared Letta memory blocks, Mem0, ChatGPT memory and Character.AI chat memories for agent traits
(`docs/design/agent-tastes.md` section 7). For rules the relevant lesson from that table is the split it already
drew: a small always-present block for what must hold every turn, and fetched memory for the rest. No new outside
source was read for this note.

## 9. Issues this note absorbs

| Issue | What is absorbed | Text to change in its body |
| --- | --- | --- |
| lina#300 (one place for "don't bring me this") | The rule record, birth from one sentence, revert from one sentence, counting | Under Summary, add: "The rule record is the owner's standing rule of kind `filter` (`docs/design/standing-rules.md`, lina#323); this issue builds where filter rules apply: every collector, briefing, to-do candidate and the receiving place (lina#520)." Replace the first Done-when line with: "A filter rule is a standing-rule row of kind `filter` (`docs/design/standing-rules.md` section 4) with its match: source, counterpart or room, kind of item." Replace "One sentence from the owner adds a rule; one sentence reverts it." with "Adding and ending a filter rule from one sentence is the standing-rule path (lina#323 follow-up 03); this issue only applies the active rules." Keep the other lines. |
| lina#198, comment "Enhancement: corrections of how agents work reach every agent" | The working-style list, its versions, one-sentence undo | Add under that comment's heading, as an edit note in the issue body's Context: "Corrections of how agents work are the owner's standing rules (lina#323, `docs/design/standing-rules.md`). Their list, versions and undo are built there; this pipeline keeps fact corrections (the 11-04 and 11-06 sections) and adds the cross-agent question to the question set." |
| lina#198, comment "Addition: agent tastes (#307)", last line | Already points at lina#323 | No change |
| lina#261 (talk like a person in chat) | Nothing of its chat-shape defaults: they are base instructions the same for every agent, set by the maintainer (#566's work-style file), not standing rules born from the owner's sentences | Add under Context: "When the owner corrects the chat shape for one agent or for everyone ('three messages is fine for me'), that is a standing rule (lina#323, `docs/design/standing-rules.md`); the rule wins over these defaults in its scope, and the defaults stay as written." |

## 10. Open points

1. **Workers.** A safe-mode worker renders no plugin sections, so it gets no rules section; today the brief carries
   what applies. Whether worker runs should get the rules section of the agent that hands them work needs the
   owner's decision (follow-up 02).
2. **The lina#380 writing rule.** It is an all-agents owner rule by nature but is planned for the self-note section.
   Move it into the rules section when follow-up 02 lands, or keep it where lina#380 puts it. Either way no byte
   before the slot changes.
3. **Rule budget.** The section cap is 4,000 characters. What happens when the rules of one agent and place do not
   fit (merge offer, place rules only, or a larger core cap) is settled in follow-up 02 with real counts.
4. **Shared SOUL blocks.** How today's installs put shared blocks in front of agents is outside this repository
   (the operators' repository did not open for this run). Follow-up 04 starts by listing those files on the install.
5. **Recording a rule for another agent.** The recording agent writes a rule scoped to another agent when the owner
   names it. Whether that agent should also confirm it back to the owner once is open.
