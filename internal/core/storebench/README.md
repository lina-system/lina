# Store benchmark

This test-only package measures the current Core store without a model, network
service or production state. It runs the shipped migrations, `ledger.AdmitBatch`,
the memory tool's `Remember`, `compaction.Publish`, ledger search, memory recall,
depth-zero `recall_person` and `retract.Forget`. Large bodies use the real assets
writer. Each run owns a private scratch directory and removes it after closing
the stores. SQLite sort and temporary databases are confined there with a scoped
`SQLITE_TMPDIR`; the serial harness restores the caller's environment afterward.

```sh
go test ./internal/core/storebench
go test -bench . ./internal/core/storebench
go test -run '^$' -bench . -benchtime=1x ./internal/core/storebench -storebench-turns=1094
```

The default benchmark has 100 turns and seed 851. `-benchtime=1x` means one fresh
corpus, not one turn. Each calibration iteration also gets a fresh store. The
JSON report contains numeric values only. It reports nearest-rank p50, p95 and
maximum nanoseconds with sample counts. Admission is timed per turn; memory
writes and summary publication are separate. Initialization, query generation,
page inspection and report serialization are outside those latency samples.
Go's `ns/op` covers the complete measurement run, including store setup.

Size is measured before forgetting. `table_bytes` lists every `dbstat` B-tree,
including SQLite indexes and FTS shadow tables. `database_bytes` includes free
pages; `free_bytes` identifies them. `index_bytes` sums trees whose owning table
contains `search`, `fts`, `bigram` or `recall`, including duplicated index text and postings
in the current schema. Keep this classification current when adding a new index;
the per-tree report remains the authoritative breakdown. `material_bytes` counts
large event bodies outside the database. `source_bytes` sums event, summary and
active memory text bytes. `recall_text_bytes` and `recall_pair_bytes` sum their
FTS shadow trees. `recall_documents` checks one row per eligible source, and
`recall_parity_queries` counts exact event ID/order comparisons against ledger
search on the same 60 queries; these comparisons are outside latency samples.

The packaged SQLite engine does not expose `dbstat`. Page inspection requires
Python 3 with SQLite's `ENABLE_DBSTAT_VTAB` (available on the Linux CI runners).
Its standard library opens the committed scratch database read-only; the
inspector passes `uri=True`, because a `file:` URI is only parsed by default on
builds compiled with `SQLITE_USE_URI`. It neither loads the corpus nor times
queries. All timed operations use Core's pinned embedded engine. An unavailable
inspector fails the run rather than reporting zero bytes.

## Corpus

The generator uses only its checked-in Korean and English word lists. Its shape
follows the published [method](../../../docs/research/unified-store.md#method):
18.3 events per turn, about 26% of events containing Hangul, one person event per
turn, alternating agent/tool replies, about 1.46% of bodies over 16 KiB, and
about 2.2 KB per event. Whole conversation prefixes receive summary nodes at
about 18 per 20,011 events. One stated memory write cites each person event.

The exact private length histogram was not published. The synthetic length
buckets (256, 2,048, 11,000 and 32,768 bytes) approximate the mean and large-body
share; they are assumptions, not recovered measurements. Fixed sampling makes
small corpora slightly different in proportion. Generated person statements are
unique, so this baseline does not reproduce private evidence-merge amplification
or prove recall quality. Reports include the actual sizes and counts.

Queries are 48 literal substrings of 3–6 characters from person events and 12
Hangul pairs, with repetition for small corpora. Search limits are 12. The three
forgets target distinct memory items, and each must become invisible. Tests
assert report shape, counts, path results and privacy, never wall-clock budgets.

## Private snapshot

```sh
go test -run '^$' -bench . -benchtime=1x ./internal/core/storebench -storebench-hermes=/absolute/offline-snapshot.sqlite
```

Use a checkpointed, offline Hermes LCM snapshot. The reader requires the
`messages` and `summary_nodes` tables described below. It refuses nonempty WAL
or rollback-journal files, unknown roles, incomplete summary references, cycles
and source changes. It hashes the source before and after reading, uses a
read-only connection and never prints text, queries, identifiers or the path.
No private source is needed for the committed tests.

- **Seen:** Hermes LCM-X `store.py` defines `messages(store_id, session_id, role,
  content, timestamp)`; `dag.py` defines `summary_nodes(node_id, session_id,
  summary, source_type, source_ids)`. Sources refer to messages or nodes.
- **Inferred:** ordered user rows and their following replies form benchmark
  turns. Source IDs become synthetic IDs. Secrets are masked before loading.
  Leading replies and empty bodies are counted as skipped. Summary text is
  retained, but coverage becomes a growing LINA prefix ending at its latest
  source event; equal endpoints become parent nodes. This measures publication
  and storage, not importer parity or human authorship.
- **Unknown:** other Hermes schemas, source identity, private recall quality and
  the original private length histogram. Unsupported snapshots fail closed.

## Running Core proof

The opt-in headless test loads a small corpus, closes its writer, starts normal
Core on that scratch state, and uses the client socket to list and forget the
remaining memory. It queues the shared index before startup and verifies Core
drains every source without a search request, then verifies socket remember and
forget update the shared index. It verifies recovery and real handlers without adding a
benchmark endpoint to the product.

```sh
dv heavy -- go test -tags storebench_core ./internal/core/storebench -run TestHeadlessCore -count=1 -v
```

## Recall eval

`BenchmarkRecallEval` runs named `recall` configurations over one private
question set and the same private snapshot. It loads the history through the
ledger's own admission path into a fresh scratch store, drains the shared
index, then asks every configuration the same questions. It writes nothing
outside its scratch directory and prints numbers only: no question, query or
message text, and no source path.

```sh
go test -run '^$' -bench '^BenchmarkRecallEval$' -benchtime=1x ./internal/core/storebench \
  -storebench-hermes=/absolute/offline-snapshot.sqlite \
  -recall-questions=/absolute/questions.jsonl
```

The question file is JSON lines, one object per question, never committed:

```json
{"id":"q01","question":"...","short_query":"...","evidence":["1234","message:5678"]}
```

`id` and `question` are required, `short_query` is optional, and `evidence`
lists at least one event: a LINA event id, or the snapshot's own row reference
as a decimal number, optionally prefixed `message:`. The reader refuses a
duplicate id, an empty question, missing evidence or a reference the snapshot
does not hold.

Configurations are the lexical ranking with the question as asked
(`lexical-question`) and with the short query (`lexical-short`); when a vector
source is supplied the same two run fused by reciprocal rank
(`fused-question`, `fused-short`). A question without a short query is left out
of the short rows rather than reusing the question. Vectors arrive through
`recall.VectorQuery`, so the command needs no endpoint of its own and runs the
lexical rows until #874 supplies one.

Each row reports recall at 5, 12 and 20, evidence recall at 12, MRR at 20,
empty results and latency at p50 and p95 over the questions it ran. Identical
derived text counts as one document, as `recall` returns it: evidence and
results are compared by that identity within a conversation. Each pair of
configurations reports the questions only one finds at 12 and the exact
two-sided sign test over those discordant pairs.

The run is private: the snapshot and the question file stay where the owner put
them, and only the numbers leave the machine.

The mode ports the reference's pipeline trial, not its pipeline search:

- **Seen:** AutoRAG's legacy `evaluator.py` evaluates a YAML file of node lines,
  runs every candidate module over the whole question set, scores retrieval
  (`evaluation/metric/retrieval.py`) and keeps each candidate's output with a
  summary table of times; `strategy.py` then keeps the best candidate by mean
  metric and writes the winning pipeline back as YAML.
- **Inferred:** LINA keeps the repeatable trial — fixed questions, one corpus,
  one row per configuration, paired counts — and drops automatic choice: the
  person or the issue reads the rows, because a one-question difference is
  noise (#1040). Metrics are the ones the owner's set reports, not the
  reference's full set: recall at 5, 12 and 20, evidence recall at 12, MRR at
  20, empty results and latency percentiles.
- **Unknown:** how the reference scores questions whose evidence is missing
  from the corpus; LINA counts the missing references and skips only a question
  left without evidence. The reference's own numbers on Korean questions were
  not re-measured here.
