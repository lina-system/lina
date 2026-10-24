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
retraction indexes keep their own triggers until the contract step. Messenger windows belong to a later step.

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
its lineage counts once: a copy appears when its original is out of scope. A
queued event has no `recall_doc` row, so its candidate row id is NULL. Lineage
exclusion compares `source_id`, which queued and published candidates both have.
A queued `view_of` copy, or a `replay_of` copy when copies are requested, counts
with its source and does not drop the other matches. Vector candidates use this
same exclusion.
`recall.Search` adds the bounded masked excerpt and the citations in force at
the result's revision. `VectorQuery` binds the optional embedding adapter. Endpoint failures and rebuilds preserve the lexical result list and order.

Memory items, events and records are the same list: `exec.Memory` asks `Match`
for events and items together, materializes the item set in one scoped read, and
reads an event's text only for the results that remain. Its cost follows the
hits, not the number of person events.

Person-model queries set `PersonOnly`, which filters person-authored events and
person-subject memory items before ranking and the limit, including queued
sources. General recall keeps its wider kind/speaker filters. Event bodies in
materials have a nullable inline body; search resolves those through the bound
reader or reports unavailable bytes without failing the whole result list.

Tests cover atomic native writes, queued sources and exclusions, generation
fencing, batch bounds, migrations, records revisions, Korean parity, any-term
ranking, identical-text collapse, lineage (including a queued view or replay),
scope and visibility classes, retraction, summary navigation, excerpt bounds
and the vector hook. The opt-in
store benchmark Core test proves startup fill and socket memory writes/forget
with the normal running Core.


## Vectors

`recall_vec` is one sqlite-vec table keyed by document id. Search configuration
records the model, dimension, endpoint, generation and rebuild readiness.
`recall_embed_pending` stores monotonically numbered queue entries without text.
The optional Moirai worker calls Core's private `Embed` operation; Core selects
at most 32 documents, resolves material bytes and removes secret spans before
calling the adapter. Source hash, queue sequence, current eligibility and model
generation are checked before dispatch and again on publication. A failed row
remains queued; the cursor wraps so later rows can advance. Empty derived text
and ineligible documents consume their queue entry without a model call.

A model, dimension or endpoint change atomically replaces only the derived
vector table and queues the current documents. Reads stay lexical until this
queue and the text backfill drain. Normal incremental writes queue their own
vectors without blocking reads. Vector candidates pass the same scoped filters
and the same lineage exclusion before distance and limit. Imported-event consent and never-leaves sensitivity
are checked through current citation ancestors. Messenger provenance is checked
through its legacy references as well as the event's origin. Import consent
stores a conversation's event watermark; future imports are excluded. Consent
changes and chat embedding switches invalidate vectors and restart backfill.

sqlite-vec refuses virtual-table writes in triggers. Deletion triggers therefore
record document ids in `recall_vec_delete`; canon's sole writer flushes these
ids before committing the same transaction. Failed removals roll back both
sources and vectors. This also handles correction, retraction and late results.

Query embedding is separate from deterministic ranking and has a 200 ms cap.
Both lists supply up to 100 candidates; reciprocal-rank fusion uses constant 60.
The existing memory management search binds this implementation through Core;
`exec.Memory.WithVectors` is the seam for `recall_person`. Historical replay
keeps its recorded lexical path.

Reference: the repository's design and #1040 measurements were read. The supplied
Hermes source directories were unavailable; this is an implementation of LINA's
contract, not a claim to reproduce unseen upstream embedding behavior. Private
parity/model-comparison measurements are still required before closing #874.

## Messenger recall

Origin `messenger` selects current visible messenger records by their source's
person, rather than an agent holder or the presence of a person-authored message.
Platform, account, chat, author and person-only filters apply before ranking.
Every other origin excludes messenger documents, including LINA's own writes.
The live tools return the same source/chat/items/coverage/cursor envelope;
`MessengerExpand` rechecks its anchor and removal epoch on each page. Each item
on an expansion page carries the complete masked message body. The search
excerpt bound applies only to recall results, because a later offset selects
other records.

Windows use the shared index's summary navigation kind, an opaque
`messenger-window:` identity and `covers` citations. They have no `sum_node`
body and no FTS postings. The text drain builds up to 120 records of one queued
chat per batch, groups at most ten with gaps no greater than five minutes, and
retains only membership and a derived-text hash. A durable sequence fences
admission, edits, removal and consent changes during material reads. The vector
worker resolves each window's masked member text and applies the source, chat,
import and sensitivity consent checks through its citations. Window vectors
rank their permitted event members through the existing fusion entry.

`messenger_embed_source` records source consent; the default-on chat switches
never grant source consent. `messenger.Store.SetEmbedding` and the explicit
`lina embedding messenger` command are the control path. They are not model
tools. Revocation removes derived vectors in the same writer transaction.
Coverage stores the adapter cadence and read window with each complete read.
Older coverage without those values stays stale instead of claiming freshness.
