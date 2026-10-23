# Research: one store and one recall

LINA keeps conversation history, summaries, memory items and records in one SQLite canon, but each grew its own text index, provenance shape, retraction check and read path. This note audits those paths, measures them, and gives the evidence for the one-store model in [conversation-and-memory.md](../design/conversation-and-memory.md#store-and-recall) and [filesystem.md](../design/filesystem.md#originals-and-derived-data) (#851). It is not normative.

The owner's history used for measurement stays private on the owner's machine. This file holds counts, sizes and times only: no message text, no query text, no names of people, accounts or hosts.

Line numbers are at commit `5735b5a9`.

## What each path stores, indexes and answers

### Raw events

| What | Where |
| --- | --- |
| The ledger event, the only raw unit | `conv_event`, `ev_rel`, `ev_span` (`internal/core/canon/schema_ledger.go:5-56`), append-only by trigger |
| Native admission | `ledger.Store.AdmitBatch` (`internal/core/ledger/store.go:155-221`); indexes the text in the same transaction (`store.go:254`) |
| Result events | `results.LedgerJournal` inserts into `conv_event` directly (`internal/core/results/ledger.go:74`), so they are indexed later from the pending queue |
| Imported events | staged in `import_event`, `import_event_relation`, `import_event_span` (`schema_import.go:12-37`), then published by the seal (`internal/addons/importer/hermes/seal.go:128,147`); indexed later from the pending queue |
| Large bodies | a material revision above 16 KiB (`ledger/store.go:23`); search reads it back to index it (`ledger/search.go:230-238`) |

### Derived views

| View | Stored as | Points to its sources through |
| --- | --- | --- |
| Summary node | `sum_node` (`schema_compaction.go:5-17`), no agent or person column | `sum_cover(node_id, event_id, ordinal, reason)` and `sum_edge(parent_id, child_id, ordinal)` (`:18-36`) |
| Memory item | `memory_item.record` JSON and a full JSON copy per revision in `memory_revision` (`schema_memory.go:5-24`) | an `evidence` array inside the JSON: kind, id, revision, precision, source agent, time and quote (`internal/core/memory/types.go:96-104`) |
| Record | Markdown files in the records repository; projection `records_index`, `records_text`, `records_edges` (`schema_records.go`, `schema_records_search.go`) | a `source` field of the record and its candidate row (`record_candidates`, `schema_record_operations.go:13-20`) |
| Person brief | `memory_person_brief.record` JSON (`internal/core/memory/job_schema.go:15-18`) | `memory.Evidence` citations per sentence (`memory/brief.go:21-23`) |
| `recall_person` answer | not stored | citation strings `memory:<id>` and `event:<id>` (`internal/core/exec/memory.go:133`, `exec/memory_ledger.go:98`) |
| Imported history | `import_record`, `import_link`, `legacy_ref`, `import_source_span` (`schema_import.go:38-72`) | `import_link` rows and legacy references |
| Request evidence | `turn.Reference{ID, Revision}` per ContextPacket item (`internal/core/boot/conversation_memory.go:63`) | the memory item id and revision |

So a pointer from a derived view to its sources has five shapes: two relational tables, a JSON array, citation strings and import links. Sensitivity spans have three: `ev_span` on events, `import_event_span` on staged events, and `import_source_span` keyed by source column.

### Text indexes

| Index | Tables | Tokenizer | Short query |
| --- | --- | --- | --- |
| Ledger | `ledger_search_text` (a second copy of the derived text), `ledger_search` (FTS5), `ledger_search_bigram(gram, event_id)` and its index, `ledger_search_pending` (`schema_ledger_search.go:5-28`) | trigram | fewer than 3 characters and not one Hangul pair: no result (`ledger/search.go:73-75`) |
| Memory | `memory_search_text` (a second copy of the normalized content), `memory_search` (FTS5), `memory_search_bigram(gram, item_id)` and its index (`schema_memory_search.go:5-37`) | trigram | other short text: literal scan of the scoped items (`memory/store.go:381-385`) |
| Records | `records_fts` (`schema_records_search.go:15`), `records_korean_fts`, `records_korean_bigram` (`schema_records_korean_search.go:8-13`) | `unicode61` words for text without Hangul, trigram otherwise | any length; a word match for non-Hangul text, a substring match for Hangul (`internal/core/records/index.go:192-203`) |
| Summaries | none | | |
| Vectors | none. sqlite-vec is linked into the engine (`canon/embedded.go:11-15`, `canon/module.go:88`), but no migration creates a vector table | | |

The same query means different things per index: "abc" finds the substring in conversation and memory text, but only the whole word in a record. "Go" finds memory items and records, and no conversation event.

### Retraction and scope checks

| Path | Check |
| --- | --- |
| Events | view `retract_visible_event` (`schema_retract_views.go:9-12`); triggers purge ledger index rows when a forgotten item excludes an event (`schema_retract_search.go:8-27`) |
| Memory | recursive view `retract_memory_exclusion` (`schema_retract_views.go:13-21`); every change to `retract_target` and every memory state change deletes and reinserts the memory index rows of all visible items (`schema_retract_search.go:34-48`) |
| Memory writes | triggers `memory_no_forgotten_insert` and `_update` scan the new item's JSON evidence against removal tables (`schema_retract.go:34-63`) |
| Summaries | recursive view `sum_affected` and trigger `sum_revoke` on each epoch change (`schema_compaction.go:41-51`) |
| Scope across holders | `restrictedEvents` decodes the JSON of every private or never-leaves item of the person on each call (`memory/store.go:473-502`) |
| Conversation ownership | no owner table; each ledger search proves ownership with two `EXISTS` scans over person events (`ledger/search.go:27-35`) |
| Records | none in canon; files are canon |

### Read paths

| Entry | What it reads | Who calls it |
| --- | --- | --- |
| Standing profile | up to 32 standing memory items, newest first, with no query | every live turn (`boot/conversation_memory.go:53`) |
| `query_records` | records index, ordered by id | live turns (`exec/records.go:128`) |
| `ledger.Store.Search` | ledger index | tests only; no production caller |
| `recall_person` | memory index, then a scan of every person event of the agent, read one at a time and matched in Go (`exec/memory_ledger.go:57-103`) | replay only (`boot/replay_reads.go:73`) |
| `expand` | summary coverage and events | replay only (`boot/replay_reads.go:43`); `compaction.Expansion.Read` (`compaction/expansion.go:49`) has no production caller |
| Memory management | `memory.Store.Recall` | TUI memory screens (`boot/management.go:146-202`) |
| Agent exchange | common memory items, no query | exchange turns (`boot/exchange_runtime.go:44`) |

A live turn today recalls nothing by topic: no ledger search, no expand, no `recall_person`, and no query against the memory index. [Recall with evidence](../design/conversation-and-memory.md#recall-with-evidence) describes all four.

### Findings

1. **Five provenance shapes and three span shapes.** Retraction cannot find "every view citing this event" with one index; it scans JSON or recursive views per kind.
2. **Evidence copies event text.** Each memory evidence entry stores the event's full derived text as its quote (`memory/store.go:212` from `exec/memory_ledger.go:47`), in the item and in every revision. No path removes that copy when the event is erased, so it would outlive a permanent deletion of the conversation.
3. **Every index stores its text twice.** `ledger_search_text` and `memory_search_text` keep a full copy of what `conv_event` and `memory_item` already hold, to serve an external-content FTS5 table.
4. **Postings repeat 32-character text ids.** Each Hangul pair row stores the event or item id as text, plus a second index on that id.
5. **One forget rewrites the whole memory index.** The refresh triggers delete and reinsert every visible item's index row on each removal, and each reinsert re-evaluates the recursive exclusion view. Time grows close to the cube of the item count (measured below).
6. **`recall_person` does not use the ledger index.** Its ledger side reads every person event of the agent, newest first, one query each, until it finds 12 substring matches. Its cost grows with history, not with hits: 2.3 s at p95 on the sample.
7. **Summaries are not searchable** although the design says search reaches summary navigation nodes.
8. **Short queries differ per index** (table above).
9. **Live turns do not recall by topic** (table above).
10. **Three writers append to `conv_event`** and only one indexes in the same transaction; the other two rely on the pending queue, which is drained only inside `ledger.Store.Search` for the caller's scope.
11. **Each evidence merge copies every earlier quote again.** Repeating a statement adds evidence to the same item and writes a new full revision (`memory_revision`), so k repeats store about k²/2 quotes (measured below).

## Method

- **History.** The owner's Hermes history on the measuring machine: 8 lossless-context databases, 111,489 messages and 176 MB of text over 12.4 days. 26 percent of messages contain Hangul and 5.9 percent are the person's. Hermes kept 751 summary nodes (5.5 MB). Read-only, counts only.
- **Sample.** Whole sessions drawn at random (seed 851) until 20,011 messages: 784 sessions, 1,094 person messages, 9,487 agent messages and 9,430 tool outputs, 44 MB of derived text, and the 18 summary nodes of those sessions. 292 bodies were above 16 KiB and became materials.
- **Shipped paths.** A private harness outside the repository, built against this commit, loads each turn (a person message and what follows it) through `ledger.AdmitBatch`, one stated memory item per person event through the memory write path, and the summary nodes as rows. It then times 60 queries (48 literal substrings of 3 to 6 characters taken from person events, 12 single Hangul pairs) through `ledger.Store.Search`, `memory.Store.Recall` and `recall_person` at depth 0, and three forgets at the canon level (removal row, `retract_target`, item state, epoch), which is what fires the triggers.
- **Index shapes.** The same derived text, turn by turn, one transaction per turn, into three fresh databases on the same embedded SQLite 3.51.2: no index, the shipped ledger and memory indexes, and the one shared index (contentless trigram table plus integer pair postings). The shared index also takes the memory items and summary nodes. Sizes come from `dbstat` on the closed files.
- **Forget probe.** Synthetic sentences, 25 to 800 memory items, median of three forgets, through the shipped migrations.
- **Machine.** Linux x86-64, 24 threads, Go 1.27.3, with other jobs running (load about 7). Times show scale and shape, not exact budgets. Sizes and row counts are exact.
- **Not measured.** Recall quality on the parity set: #849 has not published its question set yet (see [Recall quality](#recall-quality)).

The committed [store benchmark](../../internal/core/storebench/README.md) (#867)
provides a reproducible generated corpus and an opt-in reader for offline Hermes
LCM snapshots. Its generated baseline below is separate from the private sample
above. The private sample and scratch databases were deleted after the original run.

## Measurements

All numbers below were observed on the sample and machine in [Method](#method).

### Committed benchmark baseline

Observed on schema 52, based on `33aa9346`, with the committed benchmark's default
seed 851 and 100 turns. Command: `go test -bench . ./internal/core/storebench`.
One measured fresh corpus; Linux amd64, Go 1.27.3, Intel Core Ultra 7 265KF,
20 logical CPUs, other jobs running. Times are observations, not CI budgets.
The following subsection alone uses generated data; the later measurements
retain the original private sample.

The generated corpus has 1,830 events, 100 person events, 476 events containing
Hangul (26.01%), 4,011,575 text bytes (2,192 per event), 27 large bodies,
100 distinct memory items and 2 summary nodes (14,604 bytes). The length buckets
approximate the published mean and large-body share; the private length
histogram is unknown. Unique generated person statements do not reproduce the
private sample's repeated-statement revision amplification. This is a fixed
workload for comparing schema steps, not a recall-quality or full-history estimate.

| Allocation before forget | Bytes |
| --- | ---: |
| Database, including free pages | 21,614,592 |
| Search trees, including index text and postings | 14,282,752 |
| Free pages | 0 |
| Large event bodies outside the database | 884,827 |

| Main `dbstat` trees | Bytes |
| --- | ---: |
| `conv_event` | 4,177,920 |
| `ledger_search_data` | 6,778,880 |
| `ledger_search_text` | 5,062,656 |
| `ledger_search_bigram` | 475,136 |
| Its primary-key index | 548,864 |
| `ledger_search_bigram_event` | 421,888 |
| `memory_item` | 942,080 |
| `memory_revision` | 479,232 |
| `memory_search_data` | 376,832 |
| `memory_search_text` | 286,720 |
| `sum_node` | 20,480 |
| `sum_cover` | 4,096 |

All writes and queries use Core's pinned embedded SQLite. Because that engine
omits `dbstat`, Python's standard SQLite reads allocation metadata from the same
committed scratch database in read-only mode. The JSON report lists bytes for
every B-tree. `index_bytes` sums trees whose owning table contains `search`,
`fts` or `bigram`; future index changes must keep that classification current.

| Path | Samples | p50 (ms) | p95 (ms) | max (ms) |
| --- | ---: | ---: | ---: | ---: |
| `ledger.AdmitBatch`, one turn | 100 | 19.922 | 51.330 | 126.104 |
| Memory `Remember` | 100 | 3.008 | 9.927 | 23.006 |
| Summary `Publish` | 2 | 2.277 | 2.698 | 2.698 |
| Ledger search, limit 12 | 60 | 16.180 | 27.215 | 27.982 |
| Memory recall, limit 12 | 60 | 6.233 | 10.316 | 12.948 |
| `recall_person`, depth 0 | 60 | 6.397 | 29.401 | 33.726 |
| `retract.Forget`, distinct items | 3 | 241.698 | 353.361 | 353.361 |

Queries use 48 literal substrings and 12 Hangul pairs from generated person
events. The three paths returned 720, 637 and 637 total hits, respectively.
All three forgotten items became invisible. No generated or private message,
summary or query text is retained in this note. Later steps should run the same
command and corpus settings and report their own before/after numbers.

### Index size

| Database (same 44 MB of derived text) | Total | Index part | Index bytes per text byte |
| --- | ---: | ---: | ---: |
| Events, no index | 51.7 MB | 0 | 0 |
| Shipped ledger and memory indexes | 397.4 MB | 345.7 MB | 7.9 |
| One shared index, with memory items and summary nodes added | 175.0 MB | 120.4 MB, plus 2.9 MB of free pages | 2.7 |

Where the shipped index bytes go, and what the shared index keeps:

| Part | Shipped | Shared |
| --- | ---: | ---: |
| Trigram index data | 95.1 MB | 95.0 MB |
| Second copy of the text (`ledger_search_text`) | 49.4 MB | 0 |
| Hangul pair postings with text ids, their key and their second index | 194.2 MB | 21.2 MB (integer ids, no second index) |
| Document table and its key | | 3.4 MB |
| Memory index | 5.5 MB | included above |

At the measured ratio, the owner's full 176 MB of history would need about 1.4 GB of shipped index and about 0.48 GB of shared index. This is a projection from the ratio, not a run.

The shipped canon after loading the sample through the real write paths was 1.81 GB. Of that, 1.41 GB was `memory_revision`: the sample's 1,094 memory writes merged into 342 items, and each merge wrote a new revision that copied every earlier evidence quote again. The worst item had 152 revisions and 749 MB. The current items held 12 MB of quotes. Part of this is the sample: Hermes `user` rows include repeated automated prompts that LINA's attribution would not count as the person's words. The growth is real for any statement the person repeats: k repeats store about k²/2 quotes.

### Write cost per turn

A turn is one person message and what follows it: 18.3 events on average.

| Path | p50 | p95 | max |
| --- | ---: | ---: | ---: |
| Shipped admission (`ledger.AdmitBatch`, full schema, masking, spans, materials, ledger index) | 85.1 ms | 311.3 ms | 1,832.8 ms |
| Events only, no index | 0.17 ms | 0.87 ms | 3.6 ms |
| Events and the shipped ledger index | 32.6 ms | 111.7 ms | 626.9 ms |
| Events and the shared index | 19.2 ms | 72.1 ms | 394.5 ms |

Between the index steps and the drop step, writers update both indexes: about 52 ms per turn at p50 on this machine, on top of the rest of admission.

A memory write through the shipped path took 7.6 ms at p50 and 121.2 ms at p95, growing with the size of the item's record.

### Recall latency

| Query path, 60 queries | p50 | p95 | max |
| --- | ---: | ---: | ---: |
| `ledger.Store.Search`, limit 12, reads each hit's body | 25.6 ms | 851.1 ms | 1,208.4 ms |
| `memory.Store.Recall`, limit 12 | 110.6 ms | 181.6 ms | 209.2 ms |
| `recall_person` depth 0 (memory, then the per-event ledger scan) | 172.4 ms | 2,255.4 ms | 2,678.0 ms |
| Shared index, same scope and recency order, ids only | 4.4 ms | 32.2 ms | 43.6 ms |

- The shared index returned the same event ids in the same order as `ledger.Store.Search` for 60 of 60 queries.
- The packet time cap is 250 ms. `recall_person` at depth 0 exceeded it on its slow queries, because its ledger side reads person events one by one until it finds 12 matches.
- Memory recall reads each candidate's JSON record, quotes included, and decodes its evidence on every query. With records of 60 KB on average in the sample, that, not the item count, is the likely cost; it was not profiled.

### Forget cost

One forget at the canon level, which fires the retraction triggers:

| Active memory items | Text | Forget time |
| ---: | --- | ---: |
| 25 | synthetic | 16 ms |
| 50 | synthetic | 43 ms |
| 100 | synthetic | 145 ms |
| 200 | synthetic | 560 ms |
| 400 | synthetic | 2.8 s |
| 800 | synthetic | 19.1 s |
| 342 | the sample's items | 66.0 s (p50 of 3), 88.5 s max |

Doubling the items multiplies the time by 2.7 to 6.8, close to the cube at 800. The canon writer is held the whole time, so turns, admissions and other writes wait behind a forget.

## Recall quality

Not measured. The parity set of #849 (the owner's questions and expected answers over the same history) is not published yet, and a synthetic question set would measure the generator rather than the owner's use. What was checked instead: the shared index returns exactly the shipped event set for literal queries in recency order (above), so the switch alone does not change results. The changes that can change quality are the multi-term rule, BM25 ranking, live-turn recall and vectors. Each of their issues reports recall at 12 on the parity set before and after, and the drop step waits for it.

## Design choices

The model is in [Store and recall](../design/conversation-and-memory.md#store-and-recall). Choices, with what was weighed:

- **Citations are canon, the index is derived.** A citation is the provenance of the view that holds it, so losing it loses the view's meaning. The index can be rebuilt from canon and the records files. This keeps the existing rule that derived data is never the only copy of anything.
- **One citation table instead of a view over the old shapes.** A view would give readers one shape, but retraction would still decode JSON per item. An index on the cited target is what makes a removal cost proportional to what it hides.
- **No text in a citation.** A quote is a span of the cited event, read when shown. This closes finding 2: an erased event cannot survive inside memory evidence.
- **Contentless FTS5 and integer postings.** The text already lives in `conv_event`, `memory_item`, `sum_node` and `records_text`; the index needs only document ids. Measured in [Index size](#index-size).
- **One trigram index and one pair table for every kind.** The same query now means the same thing everywhere. Records lose word matching for text without Hangul: "cat" also finds "category", as it already does in conversation and memory text.
- **Short terms reach memory items and records only.** Those sets are small enough to scan literally, which memory does today. For events and summaries a term needs three characters or one Hangul pair, which the ledger requires today; a shorter term is left out there.
- **Several terms, all required.** Today a query is one literal phrase, so "도쿄 여행" misses "여행 계획은 도쿄". Splitting on whitespace and requiring every term is the smallest change that fixes it. Its effect is measured on the parity set before the switch.
- **Deterministic ranking, no model rerank.** BM25, fused with the vector rank by reciprocal rank when vectors exist, ties to the newer time. Core can run it inside the 250 ms packet cap. The model stays where it is needed: summaries and the depth-1 answer of `recall_person`. This replaces "full-text search and model reranking" in the design.
- **Summaries are searchable but stay navigation.** A hit on a node leads to its events; a node never enters a ContextPacket and is never evidence, as before.
- **Records stay canon in files.** Their documents and citations are rebuilt from the files, like the records index today.
- **Imported history stays out until promoted**, as before.
- **Not chosen: a second store or engine.** A separate search engine or a server database would add a second copy to erase, back up and restore. SQLite with FTS5 and sqlite-vec is already pinned and in every backup generation.

## Migration and rollback plan

The normative rule is in [filesystem.md](../design/filesystem.md#backup-generations): steps, each migration filling the new shape from the old inside the forward chain, the updater's activation backup as each step's rollback point, and the drop step only after an older generation restores. Core is the only writer and runs migrations at startup before it serves, so old and new code never share a canon; an older binary refuses a newer schema with `schema_too_new`.

| Step | What ships | Old shape | Rollback |
| --- | --- | --- | --- |
| 0 | Forget fix: refresh triggers touch only what a removal hides | unchanged | restore the activation backup before READY; after READY, a new release can put the old triggers back with another migration |
| 1 | `cite` added and filled from `sum_cover`, `sum_edge`, memory evidence and briefs; all writers write both | canonical | old shape is still complete; a release that ignores `cite` is safe |
| 2 | `recall_doc`, `recall_text`, `recall_pair` added; queued fill; all writers write both | canonical | old indexes are still complete; drop the new tables in a new release if needed |
| 3 | Retraction through `cite`; old triggers kept | canonical | revert code; both paths stay correct |
| 4 | `recall` serves `recall_person`, memory text match, ledger search and record search; one `expand` reader | still written | revert code; old indexes are current |
| 5 | Evidence quotes become spans; new items carry no copied text | old quotes still readable | revert code; new items lack quotes, which readers already resolve from spans |
| 6 | Live turns recall by topic; `recall` and `expand` tools in live turns | still written | revert code |
| 7 | Contract: drop the old indexes and copied quotes; `sum_cover` and `sum_edge` become views over `cite` | dropped | restore the activation backup; data written after it is lost. Ships only after step 8 passes and 14 days stable on the owner's install |
| 8 | Restore test of an older generation, in CI from steps 1 to 7 | | none needed |
| V | `recall_vec` and the embedding adapter, any time after step 4 | not affected | drop the derived table in a new release; recall falls back to full text |

Backups made before the change:

- Every step is a release, so the updater's activation backup at the old schema is taken and verified before the step's migration runs ([runtime.md](../design/runtime.md#updates)). It is the step's rollback point until READY.
- Steps 1 to 6 keep the old shape complete and current, so after READY a problem is fixed forward by a release that stops reading the new shape. No data is lost.
- Step 7 is the only irreversible one. Its pull request names the activation backup generation of its release.
- Older generations stay readable because the migration chain fills the new shape from the old one: a generation taken before step 1 restores through steps 1 to 7 in order. Step 8 proves it in CI.
- The removal list is replayed onto the new tables before any read, so an item forgotten after a generation stays hidden after it is restored.

Cost of the switch window: between steps 2 and 7 every write updates both the old and the new index ([Write cost per turn](#write-cost-per-turn)).

## Implementation issues

Filed in M2. Each names its area so dispatch can keep other work out of it, and each waits for the refactor phases of #794 that hold the same files, so it lands on the new layout instead of against it.

| Step | Issue | Change | Area | Depends on |
| --- | --- | --- | --- | --- |
| 0 | #866 | Forget touches only what it hides | canon retraction triggers | #839 (holds `migrations.go`) |
| | #867 | Store benchmark committed | new `internal/core/storebench` | |
| 1 | #868 | Citation table, filled and written beside the old shapes | canon, compaction, memory, records projection | #839, #866 |
| 2 | #869 | One search index, filled and written beside the old indexes | canon, new `internal/core/recall`, ledger, compaction, memory, records | #839, #868, #867 |
| 3 | #870 | One retraction path through citations | canon, retract, recall, memory | #868, #869 |
| 4 | #871 | `recall` entry; `recall_person`, ledger, memory and record search move onto it; one `expand` reader | recall, exec, ledger, memory, records, boot replay reads | #869, #870 |
| 5 | #872 | Evidence quotes as spans | memory, exec, boot memory view | #868 |
| 6 | #873 | Live turns recall by topic; `recall` and `expand` tools | boot conversation memory and tool family | #871, #825, #833, #849 |
| V | #874 | Vector table, embedding adapter, fused ranking | canon, recall, new adapter, moirai jobs | #871, #849 |
| 8 | #875 | Restore test of an older generation | backup tests | #868, #869, #872 |
| 7 | #876 | Drop the old indexes and copies | canon, ledger, memory, records, compaction | #870, #871, #872, #873, #875 |

The benchmark can start now, and step 0 as soon as #839 merges. Steps 1 to 5 touch canon, memory, compaction, ledger and records, which no refactor phase holds after #839. Step 6 edits `internal/core/boot/conversation_memory.go` and the tool family table, so it waits for phases 11 (#825) and 18 (#833). No issue edits `docs/design/go-core.md` while phases 28 and 29 (#843, #844) hold it; as-built notes go to the package docs.
