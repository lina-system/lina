# Research: LINA compaction and memory against LCM-X and Honcho on real history

Research for [#849](https://github.com/thisisjun786/lina/issues/849). LINA's compaction was ported from LCM-X and its person memory from Honcho by reading their source and tests ([compaction reference](../../internal/core/compaction/reference.md), [memory reference](../../internal/core/memory/REFERENCE.md)). This note runs the owner's own Hermes history through both and compares the results. It is evidence, not a contract.

The history, the fact checklists, the questions and every answer stay private on the machine that ran the check. This file holds counts and ratios only: no message text, no summary text, and no names of people, accounts or hosts. Profiles are named by the preset role they correspond to: system (the default profile), LINA, dev, knowledge, life and creative, plus two small profiles that no question came from.

## Result

- **Compaction, one summary over the same messages: LINA matches LCM-X.** At depth 0 LINA's summaries kept 37 of 40 checklist facts and LCM-X's 33, at a similar size.
- **Compaction, deeper: LINA falls behind.** Over the seven deeper checkpoints LINA kept 57 of 209 facts (27%) and LCM-X 117 (56%). The loss grows with depth: 3 of 47 against 15 at the dev profile's depth 3. LINA updates one root summary at every compaction, where LCM-X keeps its leaf summaries and condenses them, and LINA's summarizer never sees tool calls.
- **Expand is exact.** Every LINA summary expanded to exactly the events it covers, in ledger order. Bodies were byte-identical to the source, apart from masked secrets and rows the staging copy had blanked.
- **Memory: Honcho is far ahead.** Honcho's dialectic answered 48 of the 50 questions about the owner. LINA's `recall_person` returned a source with the answer for 15 at depth 0. At depth 1 it answered 7 within its 10-second limit, and 12 with the limit lifted. On the 35 questions whose evidence LINA held, Honcho answered 34 and LINA's depth 0 found 11.
- **LINA did not run on the owner's install as shipped.** The stock importer refused five of the eight profiles and proved 121 of 117,061 imported events to be the owner's. The Claude adapter refused every request. Core runs neither compaction nor memory extraction in live turns, and has no depth-1 auxiliary.
- **13 gaps** are filed as issues in M2 ([below](#where-lina-is-behind)). Two more are already tracked.

## Method

- **Data.** Read-only SQLite snapshots of the owner's Hermes install, taken on 2026-10-23 with SQLite's backup API: the default profile and seven named profiles, each with a host database (`state.db`) and an LCM-X database (`lcm.db`). 111,489 LCM messages and 751 LCM summary nodes at depths 0 to 4. Hermes summarized with `claude-sonnet-5-5` (a few nodes with `claude-haiku-5-5`), through the owner's model gateway.
- **Candidates.** Baseline: the deployed Hermes LCM-X (its stored summary nodes) and Honcho (queried live through the Hermes Honcho tools of each profile). Variant: LINA at the `dev` commit this note was written on (`5735b5a9`), imported with LINA's Hermes importer into a scratch install, compacted with LINA's real summary builder and recalled with `recall_person`.
- **Harness.** A private, rerunnable script outside the repository. It takes the Hermes directory and a LINA checkout as parameters, writes only under its run directory, and opens source databases read-only. It builds LINA from a copy of the checkout and changes no LINA code, apart from the exceptions listed under [Limits](#limits).
- **Same model.** Every LINA model call used `claude-sonnet-5-5` through LINA's own Claude adapter and the same gateway Hermes used.
- **Fixed before any run.** The compaction ranges, a 122-item fact checklist and 50 questions with expected answers and aliases were written and hashed before LINA or Honcho produced anything. The checklist was written blind: its authors saw only the raw messages, never a summary.

### Compaction task

- **Ranges.** LCM nodes whose sources cover an exact prefix of their conversation, so LINA can compact the same events: four chains with eleven checkpoints. Dev profile: nodes at depths 0, 1, 2 and 3 covering 19, 98, 373 and 1,574 messages. System profile: depths 0 to 3, covering 21, 171, 258 and 1,104 messages. Knowledge profile: depths 0 and 1 (15 and 91 messages). LINA profile: depth 0 (35 messages).
- **LINA run.** The harness replays the same LCM leaf boundaries through LINA's summary builder: one compaction per LCM leaf, each new summary taking the previous root as its child, as successive compactions do in LINA. At each checkpoint it measures the root summary.
- **Measures.** Size of what replaces the range in the request (characters of the LCM node text; characters of LINA's summary item), facts kept (a checklist item counts as kept when one of its literal match strings appears in the summary, case and whitespace normalized), exact recovery through expand (event set, order and byte-equal bodies), and model calls.

### Memory task

- **Questions.** 50 questions about the owner, each answerable from a fact the owner stated in their own message, with 2 to 5 aliases a correct answer would contain. Profiles: life 11, LINA 10, knowledge 9, system 9, dev 7, creative 4 (the two small profiles had no owner self-statements). Categories: people 11, work 9, rules 7, possessions 6, preferences 5, plans 4, history 3, health 2, place 1, routine 1, other 1.
- **Systems.** Honcho `honcho_reasoning` and `honcho_search` from each question's profile; LINA `recall_person` depth 0 (sources only) and depth 1 (a cited answer). LINA's query is written by the model from the question alone, as an agent calling the tool would.
- **Rubric**, fixed before the run: correct (the answer contains an alias), cited (correct, and a cited or returned source contains an alias), wrong (a non-empty answer without an alias that does not abstain), no answer (empty, error, timeout or abstention). Depth 0 returns sources, not an answer: it is correct and cited when a returned source contains an alias. Alias matching is the only judge; no model scores.

## Import

The run imported every profile, but only after working around problems in the importer and the Claude adapter. Each is a gap [below](#where-lina-is-behind).

| Profile | Events | Conversations | Stock importer | Person events, stock owner map | Person events, best case | Memory items promoted, best case |
|---|---|---|---|---|---|---|
| system | 17,954 | 69 | refused: unknown table | 2 | 447 | 314 |
| dev | 84,210 | 1,879 | refused: UID lineage | 85 | 1,056 | 717 |
| LINA | 6,420 | 307 | refused: UID lineage | 0 | 469 | 331 |
| knowledge | 4,742 | 143 | refused: binding kind | 0 | 177 | 146 |
| life | 1,506 | 33 | refused: UID lineage | 0 | 102 | 97 |
| creative | 1,467 | 15 | imported | 25 | 159 | 108 |
| small (2) | 762 | 33 | imported | 9 | 16 | 14 |
| **All** | **117,061** | **2,479** | **3 of 8 imported as is** | **121** | **2,426** | **1,727** |

- **Refusals.** The stock command refused five of the eight profiles: one unregistered `state.db` table, one unregistered UID binding kind, and UIDs bound under more than one lineage. With those cleared in a staging copy, four profiles were still refused for an unregistered message relation kind (`composite`, 155 rows), and two for message bodies whose detected secret span could not be masked (`invalid_secret_span`, 186 rows). The staging copies dropped or blanked exactly those rows; 9,455 host rows with ambiguous UIDs were dropped (their LCM copies still imported).
- **Owner turns.** The stock owner map can confirm only CLI sessions. The owner writes to Hermes mostly through one Telegram direct-message session per profile, so 121 of 117,061 events became person events. A confirmed Telegram map was not enough on its own: Hermes marks 1,597 owner gateway turns with display metadata the importer does not know (`gateway_input_owner`), which leaves them unproven. The best case (Telegram and CLI sessions confirmed, that one metadata key cleared on plain owner turns) proved 2,426 person events.
- **Speed.** The dev profile did not finish preparing in 87 minutes. A CPU profile showed the importer rehearsing the whole plan again for every conversation it writes (about 90% of import time in secret scanning), and matching each host message against every UID binding. With both computed once (no change in output; the importer's tests pass), all eight profiles imported in 37 minutes, the dev profile in 27.
- **Tool calls.** Imported events keep message content only. Tool-call arguments stay in the legacy reference, so LINA's summaries and `expand` never see them. In the compaction ranges they were 41% of the covered characters (dev depth 3: 971,421 tool-call characters beside 1,016,693 content characters).
- Stock-map counts for refused profiles were taken on the staging copies, since the stock command stops before classifying them.
- **Sealed history.** An imported conversation is sealed: canon refuses new events in it, so LINA cannot publish a summary over imported history in place. The compaction run copied each range, byte for byte with its speaker kinds, into a fresh native conversation and compacted that.

## Compaction

Eleven checkpoints, four profiles, 78 LCM leaves replayed. "Summary chars" is what replaces the range in the request: the LCM node text, and LINA's summary item. LCM's model calls count its summary nodes; "fallback" nodes are LCM's deterministic truncation after failed model calls, whose failed attempts are not recorded.

| Profile | Depth | Messages | LCM summary chars | LINA summary chars | Facts kept, LCM | Facts kept, LINA | Model calls, LCM | Model calls, LINA | Expand exact, LINA | Events whose tool calls expand cannot return |
|---|---|---|---|---|---|---|---|---|---|---|
| dev | 0 | 19 | 4,313 | 2,583 | 9/10 | 9/10 | 1 | 1 | 19/19 | 6 |
| dev | 1 | 98 | 6,360 | 5,386 | 11/19 | 12/19 | 5 | 4 | 98/98 | 31 |
| dev | 2 | 373 | 9,424 | 4,445 | 15/31 | 7/31 | 20 | 15 | 371/373 | 116 |
| dev | 3 | 1,574 | 10,757 | 4,302 | 15/47 | 3/47 | 56 (+1 fallback) | 42 | 1,566/1,574 | 561 |
| system | 0 | 21 | 4,303 | 4,728 | 8/10 | 10/10 | 1 | 1 | 20/21 | 9 |
| system | 1 | 171 | 4,234 | 6,166 | 15/19 | 10/19 | 5 | 4 | 170/171 | 79 |
| system | 2 | 258 | 5,171 | 4,681 | 21/28 | 4/28 | 11 (+10 fallback) | 16 | 255/258 | 110 |
| system | 3 | 1,104 | 8,228 | 12,432 | 24/46 | 13/46 | 33 (+10 fallback) | 32 | 1,068/1,104 | 443 |
| knowledge | 0 | 15 | 5,209 | 4,151 | 10/10 | 10/10 | 1 | 1 | 15/15 | 6 |
| knowledge | 1 | 91 | 5,934 | 5,529 | 16/19 | 8/19 | 5 | 4 | 91/91 | 37 |
| LINA | 0 | 35 | 6,024 | 4,776 | 6/10 | 8/10 | 1 | 1 | 35/35 | 13 |

- **Depth 0 (one model call over the same messages): LINA matches or beats LCM.** Facts kept: 37 of 40 for LINA, 33 of 40 for LCM; decisions 9 of 9 against 6 of 9. Summary size is similar (LINA 7.8% to 13.2% of the covered characters, LCM 7.5% to 16.5%).
- **Depth 1 and deeper: LINA falls behind.** Summed over the seven deeper checkpoints, LINA kept 57 of 209 checklist facts (27%), LCM 117 (56%). The loss grows with depth: at the dev profile's depth 3, LINA kept 3 of 47 facts and LCM 15. By kind: paths 5 of 34 against 24, names 19 of 51 against 36, numbers 17 of 68 against 28, decisions 16 of 56 against 29.
- **Why.** LINA's successive compactions form one chain: each new summary rewrites the previous summary together with the next leaf, within one model reply, so older facts are summarized again at every step. After the first leaf, its root stayed between 4,300 and 6,200 characters in three chains whatever the range; only the system chain's last root reached 12,400. LCM keeps each leaf summary and condenses groups of them, so its deeper nodes carry more of the leaves. At the dev profile's depth 3, LINA's summary is 0.2% of the covered characters against LCM's 0.5%: a smaller request that no longer holds most of the facts.
- **Tool calls.** 20 checklist items (summed over checkpoints) were stated only in a tool call. LCM kept 19 of them, LINA 3: LINA's summarizer never receives tool calls (see Import).
- **Expand.** Every LINA summary expanded to exactly the covered events, in ledger order. At each chain's deepest checkpoint, 2,760 of 2,804 expanded bodies were byte-identical to the LCM source; the other 44 had a secret-shaped value replaced by `[REDACTED]` (40, by design) or were bodies the staging copy had blanked (4). LCM's expand returns its stored rows: all covered rows were present, 88 of them only as externalized-output pointers whose files were not part of the snapshot (LINA's copies carry the same pointer text).
- **Model calls.** LINA made one call per LCM leaf: 78 summaries, plus one failed call that was retried (and one failed first call in an earlier, discarded run). For the same four chains LCM made 95 model-made nodes (56, 33, 5 and 1), plus 11 fallback nodes. LINA re-sends its previous summary each time: 1.33 million input and 150,000 output tokens for the dev profile's depth 3 (LCM records only its own size estimates, which are not comparable).
- **Speed.** 22 to 27 seconds per LINA summary call through the gateway.

## Memory

Each system answered each of the 50 questions once. "Cited" is part of "correct". The last column counts correct answers among the 35 questions whose evidence LINA held as promoted person events.

| System | Correct | Cited | Wrong | No answer | Correct, of the 35 |
|---|---|---|---|---|---|
| Honcho `honcho_reasoning` | 48 | 38 | 1 | 1 | 34 |
| Honcho `honcho_search` (3 to 5 results) | 38 | 38 | 12 | 0 | 27 |
| LINA `recall_person` depth 0 (up to 12 sources) | 15 | 15 | 6 | 29 | 11 |
| LINA `recall_person` depth 1, as shipped (10-second limit) | 7 | 7 | 1 | 42 | 5 |
| LINA `recall_person` depth 1, limit lifted | 12 | 12 | 3 | 35 | 10 |

- **Honcho.** `honcho_reasoning` gave an answer containing an expected alias for 48 questions. For 38 of them, one of its search results also contained it. It missed 2, and LINA found neither of them.
- **LINA depth 0.** For 15 questions one of the returned sources contained an expected answer. For 29 it returned nothing, and for 6 only sources without the answer.
- **LINA depth 1.** Depth 1 retrieved exactly the same sources as depth 0 for all 50 questions, so it can only answer what depth 0 found. With the limit lifted, it answered 12 of those 15 correctly, abstained on 2 and gave 1 answer without an alias. Every correct answer cited a source that held the answer. The auxiliary replied in 2.9 to 20.4 seconds (median 8.5), through LINA's Claude adapter, which starts a runtime process for each request. 10 of its 21 calls took longer than the 10-second limit, which leaves 7 correct answers as shipped. As shipped, those calls would also not return at all: the adapter hangs once its context ends ([gaps](#where-lina-is-behind)). The 29 questions without sources make no model call.
- **Coverage.** For 15 questions the owner's statement exists only in turns LINA could not prove were the owner's: desktop sessions (14, one of them also dropped from the staging copy for its UID lineage) and one Telegram turn that stayed unproven. Honcho answered 14 of these; LINA's depth 0 found 4 through other statements.
- **Query.** For each of the other 35, some active memory item contained an expected answer, and depth 0 returned it for 11. LINA matches its query literally: a memory item must contain the query phrase (trigram index), and a person event must contain the whole query as a substring. Its query was written by the model from the question alone: one word for 18 questions, two for 27, three for 5. 29 of the 50 queries matched nothing.
- **Memory size.** The best-case import promoted 1,727 owner statements into 1,712 memory items; repeated statements became revisions. Nothing else writes person memory: no extraction runs.
- **Latency.** Depth 0 took a median of 21.6 seconds (90th percentile 36.1, maximum 43.1), and 40 of 50 calls took longer than 10 seconds. When memory items give fewer than 12 sources, the ledger fallback reads every person event of the agent (2,426 here) on each call. With the limit lifted, depth 1 took a median of 39.0 seconds end to end. Honcho's response times were not recorded.

By question category (correct answers):

| Category | Questions | Honcho reasoning | LINA depth 0 | LINA depth 1, as shipped | LINA depth 1, limit lifted |
|---|---|---|---|---|---|
| People | 11 | 11 | 6 | 2 | 6 |
| Work | 9 | 9 | 2 | 1 | 2 |
| Rules | 7 | 5 | 2 | 0 | 0 |
| Possessions | 6 | 6 | 2 | 2 | 2 |
| Preferences | 5 | 5 | 1 | 0 | 0 |
| Plans | 4 | 4 | 2 | 2 | 2 |
| History | 3 | 3 | 0 | 0 | 0 |
| Health | 2 | 2 | 0 | 0 | 0 |
| Place, routine, other | 3 | 3 | 0 | 0 | 0 |

## Where LINA is behind

One issue per gap, in [M2](https://github.com/thisisjun786/lina/milestone/3). Numbers for each are above.

Import of the owner's install:

- **Current Hermes layout refused** ([#1102](https://github.com/thisisjun786/lina/issues/1102)): an unregistered table, UID binding kind and message relation kind.
- **One UID in two lineages stops a profile** ([#1103](https://github.com/thisisjun786/lina/issues/1103)): three profiles refused; 9,455 host rows dropped to continue.
- **A secret-shaped value in a JSON body stops a profile** ([#1104](https://github.com/thisisjun786/lina/issues/1104)): 186 rows in two profiles.
- **The owner's Telegram and desktop turns are never person evidence** ([#1105](https://github.com/thisisjun786/lina/issues/1105)): 121 of 117,061 events with the stock owner map; 15 of the 50 questions unreachable even in the best case.
- **Tool calls are not in imported events** ([#1106](https://github.com/thisisjun786/lina/issues/1106)): 41% of the covered characters never reach summaries or `expand`.
- **Import speed** ([#1107](https://github.com/thisisjun786/lina/issues/1107)): the dev profile did not finish preparing in 87 minutes.

Model path:

- **The Claude adapter refuses the pinned runtime's built-ins** ([#1108](https://github.com/thisisjun786/lina/issues/1108)): every request failed.
- **`claudeproc` hangs after its context ends** ([#1109](https://github.com/thisisjun786/lina/issues/1109)): a timed-out depth-1 call never returns.
- **No gateway route** ([#1110](https://github.com/thisisjun786/lina/issues/1110)): the model gateway Hermes used is not a route LINA can take.

Compaction:

- **No live compaction** ([#1114](https://github.com/thisisjun786/lina/issues/1114)): the pass and summary builder run only when driven directly.
- **Successive summaries lose older facts** ([#1111](https://github.com/thisisjun786/lina/issues/1111)): 27% of facts kept at depth 1 and deeper, against LCM-X's 56%.

Memory:

- **No extraction** ([#1112](https://github.com/thisisjun786/lina/issues/1112)): person memory holds only promoted import statements.
- **No depth-1 auxiliary, and the deadline is tight** ([#1115](https://github.com/thisisjun786/lina/issues/1115)): 10 of 21 measured replies took longer than 10 seconds.
- **Literal matching** (already tracked in [#874](https://github.com/thisisjun786/lina/issues/874)): 29 of 50 queries matched nothing, and for each of the 24 reachable questions depth 0 missed, a memory item contained the answer.
- **Depth-0 latency** (already tracked in [#871](https://github.com/thisisjun786/lina/issues/871)): a median of 21.6 seconds, growing with the number of person events.

## Limits

- **One owner, one run.** Two weeks (2026-10-10 to 10-23) of one person's history, mostly Korean, on one Hermes install. Every LINA number is a single run with one model; model output varies between runs.
- **LINA was helped to run at all.** The import used staging copies with the refused rows removed or blanked (counts above), a performance-only patch to the importer, and the best-case owner map. The Claude adapter's copy accepted the runtime's built-in plugins, skills and agents, and passed the owner's gateway URL and key through its environment allowlist. For depth 1, the harness supplied the auxiliary, closed the adapter's stream when its context ended (so a timed-out call returns), and ran once with the 10-second recall limit lifted in a copy; the as-shipped row applies the limit to the measured reply times. No other LINA code changed. Without these, LINA's numbers here would be zero, not lower.
- **Compaction is forced, not live.** Core does not run compaction in live turns yet, so the run drove LINA's real summary builder directly, one compaction per LCM leaf, on a native copy of each range. A live LINA with a 200,000-token window would compact far less often, in larger segments; that schedule was not measured.
- **Facts are matched literally.** A fact counts as kept only when one of its match strings appears in the summary. A paraphrase or a translation (for example, a Korean decision summarized in English) counts as lost for either system. The checklists were written by model-assisted readers from sampled leaves (3 to 10 items per leaf), not by the owner.
- **Questions and answers.** The 50 questions were drafted by a model reading the owner's own messages and checked against their message ids; the owner did not write them. Alias matching can miss a correct paraphrase (counted wrong) or accept an answer that mentions an alias while getting it wrong; no model judged any answer.
- **Honcho ran live, LINA from a snapshot.** Honcho answered from its live state on 2026-10-23 through each profile's Hermes tools. LINA answered from the snapshot imported the same day. LINA's depth-1 answer used a minimal auxiliary written for this run, because Core has none.
- **Honcho's own records were not imported.** The importer has a Honcho stage (peers, peer cards, conclusions), but the snapshot held only the Hermes databases and Honcho was reachable only through its tools. LINA's memory here is what it builds from the conversation history alone; starting from Honcho's conclusions was not measured.
- **LCM-X version.** The baseline is the LCM-X build that produced the stored nodes, not the pinned reference revision; its token counts are its own estimates.

## Rerunning

The harness runs in steps: build, install, sanitize, rehearse, import, select, compact, recall and score. Its inputs are the Hermes directory (default `~/.hermes`), a LINA checkout and a run directory. Questions, checklists, Honcho answers and all summaries stay in the run directory. Delete the run directory after copying out the counts.
