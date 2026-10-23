# Shared text index

This is migration step 2 of the [store plan](../../../docs/research/unified-store.md).
No product reader uses these tables yet. Ledger, memory and records retain their
existing indexes and query behavior.

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
retraction indexes keep their own triggers until the contract step. New
readers, ranking, vectors and messenger windows belong to later steps.

Tests cover atomic native writes, queued sources and exclusions, generation
fencing, batch bounds, migrations, records revisions, Korean ledger parity and
the generated store corpus. The opt-in store benchmark Core test proves startup
fill and socket memory writes/forget with the normal running Core.
