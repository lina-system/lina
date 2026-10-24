# Shared text index and recall entry

This is migration steps 2 to 4 of the [store plan](../../../docs/research/unified-store.md):
the index, and the one read entry over it. `recall.Search` serves ledger search,
memory text match, record search and `recall_person`; the old indexes are still
written until the contract step, so a revert is safe.

`recall.Index` takes derived text in the source writer's canon transaction. It
reads source identity, revision, scope and visibility from `recall_source`, then
replaces one document and its contentless FTS rows. Native admission, summary
publication, memory revision writes and records projection call it. SQL triggers
queue direct event inserts (including result previews and sealed imports), state
changes and existing eligible sources. Imported observations and summaries stay
in import staging, outside the source view. Evidence quotes and glosses are never
read as index text. Event revisions use `0`, matching citations; records use the
file revision. Replay copies retain their own flagged document.

The trigram FTS table serves literal substrings of three or more characters.
`hangul_pair_words_v1` stores overlapping pairs from each composed Hangul run,
single-syllable runs, and lower-case words from other scripts in a second
contentless FTS table. Repetitions remain for BM25. Both tables support ordinary
DELETE without retaining a second body copy.

Core owns one background drain, starts it after socket binding and joins it
before closing canon or materials. Each batch snapshots at most 128 queue rows,
reads material bytes through the ledger outside canon transactions, and checks
the queue generation before publication. Changes and removals invalidate that
generation. Errors retain pending rows. A scan cursor passes failed rows so they
cannot starve the next batch; it wraps at the end and retries. Full successful
batches yield 10 ms; idle or failed passes wait one second. This is eventual
indexing only for queued sources; no search request runs the drain.

During the switch window the citation walk owns removal, recovery and erase for
this index: a removal deletes the documents it hides in its own transaction, a
recovery queues only what the source view admits again, and the source view
fences a late write through the candidate's citation ancestors. The existing
retraction indexes keep their own triggers until the contract step. Vectors and
messenger windows belong to later steps.

`recall.Match` applies scope (person, holder, conversation, kinds, time, speaker,
origin, replay, visibility class), matches, ranks and limits without reading a
published source body. A scope can also restrict only event candidates by
speaker, which keeps memory items eligible; `recall_person` uses it to read the
person's own events and never an agent's or tool's words as evidence about the
person. `PersonMemory` keeps only memory items whose subject is the person,
before ranking and the limit; `recall_person` and person-only memory search set
it. Vector candidates go through the same scope predicates and the same
restricted-event list before fusion. Query terms are literal, whitespace split,
at most eight, and any term may match. Documents holding more terms come first
by BM25 over the pair table; documents found only through the trigram table follow;
ties go to the newer `occurred_at`. A short term reaches memory items and
records by a literal scan of the already scoped set. A source that is still
queued keeps its text in canon; `Match` matches it there, so search does not
wait for the background drain. A queued event whose body lives in material is
read through the caller's `EventText` reader, which only `ledger.Store.Search`
supplies. `recall_doc.text_hash` collapses events with
identical derived text inside one conversation to the earliest of them. A
returned memory item hides its own evidence events, so lineage counts once. A
replay copy is a candidate only when the scope asks for copies, and even then
its lineage counts once: a copy appears when its original is out of scope.
`recall.Search` adds the bounded masked excerpt and the citations in force at
the result's revision. `VectorQuery` is the fusion hook for #874.

Memory items, events and records are the same list: `exec.Memory` asks `Match`
for events and items together, materializes the item set in one scoped read, and
reads an event's text only for the results that remain. Its cost follows the
hits, not the number of person events.

Person-model queries set `PersonOnly`, which filters person-authored events and
person-subject memory items before ranking and the limit, including queued
sources. General recall keeps its wider kind/speaker filters. Event bodies in
materials have a nullable inline body; search resolves those through the bound
reader or reports unavailable bytes without failing the whole result list.

Restricted-source selection follows protected memory to its evidence, then
walks current citations back to every dependent memory, record and summary.
Typed source identities preserve the boundary across nested summary children.
The same scope predicate applies before limiting indexed, queued and vector-hook
candidates. Internal reads retain their scope, while projection also excludes
private evidence. The recursive union terminates citation cycles.

Tests cover atomic native writes, queued sources and exclusions, generation
fencing, batch bounds, migrations, records revisions, Korean parity, any-term
ranking, identical-text collapse, lineage, scope and visibility classes,
retraction, summary navigation, excerpt bounds and the vector hook. The opt-in
store benchmark Core test proves startup fill and socket memory writes/forget
with the normal running Core.

`restrictions.go` owns `RestrictionCTE`, the canonical SQL relation shared by
search candidate selection, memory projections, expansion and replay/export
validation. `SourceRestricted` queries that relation for exact references.
Protected memory follows current citations to its sources; replay/view links
and event material locators participate in the same traversal. The reverse
walk reaches every dependent view, including all view targets. Typed identities
and recursive UNION terminate cycles. Legacy memory records without citation
rows contribute their canonical JSON evidence to the same relation. Each
reader retains its own identity,
revision, removal and availability checks; no restriction cache is persisted.
