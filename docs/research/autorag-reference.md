# Research: what LINA takes from AutoRAG

Research for [#1040](https://github.com/thisisjun786/lina/issues/1040). The owner named AutoRAG as a reference for LINA's data processing, search and suggestion path. This note reads it at a pinned commit, measures its main retrieval choices against LINA's on the owner's question set from [#849](https://github.com/thisisjun786/lina/issues/849), and decides per piece whether LINA adopts it. It is evidence, not a contract. The contract stays in [Store and recall](../design/conversation-and-memory.md#store-and-recall); the decisions reach it through the implementation issues listed under [Issue updates](#issue-updates).

The owner's history and questions stay private on the owner's machine. This file holds counts, sizes, times and generalized findings only: no message text, no question or query text, no names of people, accounts or hosts.

## Source

- Repository: [Marker-Inc-Korea/AutoRAG](https://github.com/Marker-Inc-Korea/AutoRAG) at commit `c1a4757d3bd540d8a44e7c0d165839e0c35c4530` (2026-10-23). Paths below are relative to its root.
- It has two parts. The legacy Python package (`legacy/`) is AutoML for retrieval: it runs combinations of retrieval, fusion, query expansion, rerank and compression on a question set and keeps the best pipeline. The current TypeScript application (`src/`) is a local assistant over files, chats and mail.
- License: `LICENSE` and `legacy/LICENSE` are Apache-2.0, and `NOTICE` covers both parts and lists the production dependencies. Reuse is allowed with the license, the notice and a statement of changes. A few files carry other terms: ports from an MIT project under `src/web/fetch/`, two encoder files under `legacy/autorag/nodes/passagereranker/tart/` with a copyright line and no license, an MIT test fixture, and bundled MIT Windows binaries under `vendor/`. LINA copies no code from it. If a later change does, it carries the license and `NOTICE` entry, as [THIRD-PARTY-NOTICES.md](../../THIRD-PARTY-NOTICES.md) does for other references.
- Several pieces of the current application are outside the repository and were not read: its BM25, vector and hybrid engine is an external binary (pinned 0.4.11) that loads the Kiwi morphological analyzer as a native library; version-twin detection is an external command-line tool; HWP and HWPX parsing is an MIT npm package; evidence judgment is a hosted judge model. Where this note describes them, it uses the repository's wrappers and documentation.

## What the reference does

**Pipeline trials (legacy).** One command evaluates a YAML file of node lines (`legacy/autorag/evaluator.py`). Each node (query expansion, lexical, semantic or hybrid retrieval, rerank, filter, compression, prompt, generation) lists candidate modules; list-valued parameters expand to every combination. Each candidate runs on the whole question set and is scored with retrieval metrics (recall, precision, F1, nDCG, MRR, MAP; `legacy/autorag/evaluation/metric/retrieval.py`) or generation metrics. The best candidate by mean metric, optionally under a time threshold, feeds the next node (`legacy/autorag/strategy.py`). Every candidate's output and a summary table with times are kept, and the winning pipeline is written back as YAML. Question-expansion candidates are scored by running a retrieval module on their output.

**Korean lexical search.** The legacy BM25 module offers Kiwi, Kkma and Okt morpheme tokenizers beside a Porter stemmer and whitespace (`legacy/autorag/nodes/lexicalretrieval/bm25.py`); its Kiwi option keeps every morpheme, particles and endings included. The current application always indexes the original words plus Kiwi morphemes (`src/minsync/lexical-language.ts`) and documents why: on 410 Korean documents, morphemes raised stem queries from 0.10 to 0.77 recall at 50 against whitespace splitting, and keeping the original words held exact-term queries at 0.98 (`docs/minsync-setup.md`). Without the native library it falls back to whitespace splitting and reports it. Indexing 5,456 chunks took 36 s with Kiwi against 2 s without. Its baseline is whitespace splitting, not the syllable pairs LINA indexes.

**Fusion.** The legacy hybrid modules fuse by reciprocal rank (constant swept from 4 to 80, default 60) or by a weighted sum of normalized scores (`legacy/autorag/nodes/hybridretrieval/`). The current application merges its methods by per-method min-max scores plus a capped bonus for several hits from one source (`src/retrieval/merger.ts`); it uses reciprocal rank with 60 only for its similar-question memory.

**Rerank.** The legacy package has about twenty rerank modules, including a Korean cross-encoder (`legacy/autorag/nodes/passagereranker/koreranker.py`). The current application reranks the merged list through a hosted rerank API, top 25 by default, and keeps the merged order when the call fails (`src/retrieval/rerank.ts`, `docs/rerank.md`). Rerank is off unless configured.

**Query expansion.** The legacy package has hypothetical-answer expansion (HyDE), multi-query expansion and decomposition (`legacy/autorag/nodes/queryexpansion/`). The current application keeps only decomposition into at most five sub-queries, on the first query of a conversation and only when its judge asks for it (`src/agent/query-decomposition.ts`).

**Judged evidence.** After an answer, each passage it cited becomes one yes-or-no question to a hosted judge, and passages with probability 0.7 or more are stored with the question (`src/agent/evidence-judgment.ts`, `src/memory/judged-evidence.ts`). The store never reorders, filters or boosts results; it feeds similar past questions and their sources back as context and as hints for which source to search (`docs/retrieval-memory.md`). Nothing expires.

**Version twins.** At index time only exact duplicates are dropped, keeping the latest copy (`src/dupey/filter.ts`). Near duplicates (Jaccard 0.9 or more) and containment are reported by a command and a tool, never collapsed in results (`src/dupey/cli.ts`).

**Parsing.** HWP, HWPX, PDF, DOCX and spreadsheet files go through one dependency that returns Markdown keeping headings, lists and tables, with a 120 s deadline per file in a worker thread (`src/parser/kordoc.ts`).

**Scope.** Stored evidence is shown only when its source is still configured, checked at read time from live settings (`src/memory/context.ts`), and retrieval queries only configured sources (`src/datasource/scope.ts`).

## Method

The design follows the paired-benchmark method of the owner's agent-evaluation practice: the question set, corpus, metrics and candidates were fixed before any candidate ran, every candidate saw the same inputs, and each difference is reported with a paired test.

- **Question set.** The #849 owner set: 50 questions in Korean that the owner wrote about their own history, across six assistant profiles (people 11, work 9, rules 7, possessions 6, preferences 5, plans 4, history 3, health 2, routine, place and other 1 each). Each question names one to three messages that hold the answer. 49 questions have their evidence in the corpus (75 evidence events); two evidence messages of the 50th did not import.
- **Corpus.** The #849 scratch import of the same history, read from a read-only snapshot: the 6,997 events whose speaker kind is `person` (2,426) or `forwarded` (4,571), 42.9 MB of derived text with secret spans removed, over 12.4 days. All evidence is in this set. Agent replies, tool output and automation (72,049 events, 184 MB) were left out: they hold no evidence, and embedding them on this machine's CPU would take hours. Absolute numbers are therefore higher than an index of every kind would give; differences between candidates are what this note uses.
- **Identical copies.** 1,718 of the 6,997 events repeat another event's text exactly. Most (1,198 groups) are one message imported once from each Hermes source into the same conversation at the same second. The importer is meant to map such copies to one event through the source's UID bindings, and #849's staging dropped some bindings, so this is probably a staging artifact; #849 owns that check. Scores count identical text as one document, in the evidence and in the ranked lists. Where it matters, the raw number is given too.
- **Two query shapes.** *Question*: the question as the owner asked it (median 31 characters, 7 words). This is the shape of the per-turn memory query, which uses the person's current input ([#873](https://github.com/thisisjun786/lina/issues/873)). *Short query*: the query #849's query writer produced for the same question (median 6 characters, 2 words). This is the shape of a model's own `recall` call.
- **Metrics.** Recall@k: the share of questions with at least one evidence event in the top k (the legacy package's recall with one evidence group per question). k = 12 is `recall_person`'s limit and the number #874 and #873 report. Evidence recall@12: evidence events in the top 12 over all 75. MRR over the top 20. Latency per query, first stage plus any model step. Paired comparisons count the questions only one candidate finds at 12 and give an exact two-sided sign test; with 49 questions, a difference of one or two questions is noise.
- **Machine.** Linux x86-64, 24 threads, 29 GB, no GPU, other jobs running (load 1 to 16). SQLite 3.53 FTS5 for every lexical index, contentless, one database file. Times show scale, not budgets.
- **Isolation.** A private harness outside the repository, Python 3.12, read-only inputs. Models: `BAAI/bge-m3` for vectors (1,024 dimensions, first 256 tokens of each event), `BAAI/bge-reranker-v2-m3` and `Dongjin-kr/ko-reranker` (the legacy package's Korean reranker) as cross-encoders, Kiwi 0.24 for morphemes. Expansion used `claude-sonnet-5-5` through the owner's gateway, the route #849 used, one call per question per method; only the question text was sent.

### Candidates

| Name | What it is |
| --- | --- |
| Shipped | `ledger.Store.Search` today: the whole query as one literal phrase (trigram match, or the pair posting for one two-syllable Hangul term), newest first |
| All terms | The current design rule: whitespace terms, every term must match, three or more characters as a trigram substring, a two-syllable Hangul term by its pair, shorter terms left out; ordered by BM25 over the trigram index |
| Trigram any | The same index with any term matching, BM25 over the trigram index, then pair-only matches |
| Pair BM25 | BM25 over a contentless FTS5 table whose tokens are adjacent Hangul syllable pairs (a one-syllable run stays whole) and lower-cased words of other scripts; any term matches |
| Kiwi all, Kiwi content, Kiwi words+content | BM25 over Kiwi morphemes: every morpheme (the legacy option), content morphemes only (nouns, stems, roots, foreign words, numbers), and the original words plus content morphemes (the current application's mode) |
| Vectors | Cosine similarity of `bge-m3` vectors |
| Fused | Reciprocal-rank fusion of a lexical list and the vector list, constant 60 unless stated, ties to the newer event |
| Rerank | A cross-encoder over the top 20 of a first stage, the rest unchanged |
| HyDE, Multi-query | One model call writes a hypothetical answer message, or three short queries; their lists are fused with the question's |

## Results

Run on 2026-10-23. Every row is one observed run over all 49 questions with evidence; nothing is extrapolated. Latency is per query on this machine; vector rows include embedding the query on the local CPU, which an embedding endpoint replaces with its own round trip.

### Question shape

| Candidate | Recall@5 | Recall@12 | Recall@20 | Evidence recall@12 | MRR@20 | Empty | p50 ms | p95 ms |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Shipped | 0.00 | 0.00 | 0.00 | 0.00 | 0.000 | 49 | 0 | 3 |
| All terms | 0.00 | 0.00 | 0.00 | 0.00 | 0.000 | 49 | 0 | 2 |
| Trigram any | 0.20 | 0.22 | 0.22 | 0.15 | 0.155 | 0 | 1 | 7 |
| Pair BM25 | 0.33 | 0.41 | 0.45 | 0.27 | 0.310 | 0 | 7 | 17 |
| Kiwi all | 0.39 | 0.41 | 0.41 | 0.27 | 0.342 | 0 | 25 | 50 |
| Kiwi content | 0.39 | 0.43 | 0.47 | 0.28 | 0.335 | 0 | 6 | 11 |
| Kiwi words+content | 0.33 | 0.35 | 0.41 | 0.23 | 0.277 | 0 | 7 | 11 |
| Vectors | 0.51 | 0.61 | 0.67 | 0.45 | 0.447 | 0 | 131 | 163 |
| Fused: pair BM25 + vectors | 0.51 | 0.61 | 0.63 | 0.45 | 0.474 | 0 | 138 | 169 |
| Fused: Kiwi words+content + vectors | 0.55 | 0.65 | 0.69 | 0.45 | 0.475 | 0 | 140 | 169 |
| Fused: all terms + vectors | 0.51 | 0.61 | 0.67 | 0.45 | 0.447 | 0 | 132 | 163 |
| Rerank (`bge-reranker-v2-m3`) of pair BM25 | 0.35 | 0.41 | 0.45 | 0.27 | 0.321 | 0 | 20,911 | 65,054 |
| Rerank (`bge-reranker-v2-m3`) of fused pair + vectors | 0.51 | 0.63 | 0.63 | 0.47 | 0.474 | 0 | 11,484 | 15,912 |
| Rerank (`ko-reranker`) of fused pair + vectors | 0.57 | 0.63 | 0.63 | 0.47 | 0.537 | 0 | 16,286 | 35,748 |
| HyDE: vectors of the written answer | 0.47 | 0.53 | 0.63 | 0.43 | 0.411 | 1 | 237 | 265 |
| HyDE: pair BM25 of the written answer | 0.12 | 0.16 | 0.18 | 0.12 | 0.089 | 1 | 17 | 40 |
| HyDE fused with the question's lists | 0.57 | 0.65 | 0.71 | 0.48 | 0.411 | 0 | 397 | 439 |
| Multi-query: pair BM25 of three queries | 0.35 | 0.47 | 0.51 | 0.32 | 0.281 | 0 | 14 | 30 |
| Multi-query fused with the question's lists | 0.53 | 0.57 | 0.59 | 0.41 | 0.454 | 0 | 153 | 193 |

Expansion latency above excludes the model call. That call took 3.5 s at p50 and 6.3 s at p95 for HyDE (150 input and 233 output tokens per question), and 1.9 s and 5.6 s for multi-query (147 and 57 tokens).

The fusion constant hardly matters here: recall@12 for pair BM25 + vectors was 0.63, 0.63, 0.61 and 0.59 at 10, 30, 60 and 100.

### Short-query shape

| Candidate | Recall@5 | Recall@12 | Recall@20 | Evidence recall@12 | MRR@20 | Empty | p50 ms | p95 ms |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Shipped | 0.12 | 0.16 | 0.18 | 0.11 | 0.083 | 26 | 0 | 1 |
| All terms, newest first | 0.18 | 0.24 | 0.27 | 0.16 | 0.134 | 6 | 0 | 1 |
| All terms | 0.18 | 0.27 | 0.29 | 0.17 | 0.186 | 6 | 0 | 3 |
| Trigram any | 0.29 | 0.39 | 0.41 | 0.27 | 0.240 | 0 | 0 | 5 |
| Pair BM25 | 0.33 | 0.43 | 0.51 | 0.29 | 0.289 | 0 | 1 | 4 |
| Kiwi all | 0.33 | 0.41 | 0.43 | 0.28 | 0.241 | 0 | 1 | 13 |
| Kiwi content | 0.33 | 0.41 | 0.45 | 0.27 | 0.241 | 0 | 1 | 5 |
| Kiwi words+content | 0.29 | 0.37 | 0.39 | 0.25 | 0.242 | 0 | 1 | 4 |
| Vectors | 0.35 | 0.43 | 0.49 | 0.32 | 0.286 | 0 | 170 | 201 |
| Fused: pair BM25 + vectors | 0.43 | 0.47 | 0.57 | 0.33 | 0.309 | 0 | 171 | 205 |
| Fused: Kiwi words+content + vectors | 0.35 | 0.43 | 0.51 | 0.31 | 0.296 | 0 | 173 | 202 |
| Fused: all terms + vectors | 0.39 | 0.41 | 0.53 | 0.31 | 0.321 | 0 | 170 | 201 |

### Paired comparisons at 12

"Only A" and "only B" count the questions one candidate finds in its top 12 and the other does not.

| A | B | Only A | Only B | Both | p |
| --- | --- | --- | --- | --- | --- |
| Pair BM25 (short query) | Shipped (short query) | 13 | 0 | 8 | <0.001 |
| Pair BM25 (short query) | All terms (short query) | 8 | 0 | 13 | 0.008 |
| Pair BM25 (question) | Trigram any (question) | 10 | 1 | 10 | 0.012 |
| Kiwi content (question) | Pair BM25 (question) | 3 | 2 | 18 | 1.000 |
| Pair BM25 (question) | Kiwi words+content (question) | 4 | 1 | 16 | 0.375 |
| Kiwi content (short query) | Pair BM25 (short query) | 3 | 4 | 17 | 1.000 |
| Vectors (question) | Pair BM25 (question) | 12 | 2 | 18 | 0.013 |
| Fused pair + vectors (question) | Pair BM25 (question) | 10 | 0 | 20 | 0.002 |
| Fused pair + vectors (question) | Vectors (question) | 2 | 2 | 28 | 1.000 |
| Fused Kiwi + vectors (question) | Fused pair + vectors (question) | 2 | 0 | 30 | 0.500 |
| Fused pair + vectors (short query) | Pair BM25 (short query) | 3 | 1 | 20 | 0.625 |
| Vectors (question) | Vectors (short query) | 11 | 2 | 19 | 0.022 |
| HyDE fused | Fused pair + vectors (question) | 2 | 0 | 30 | 0.500 |
| Multi-query fused | Fused pair + vectors (question) | 2 | 4 | 26 | 0.688 |
| Rerank `bge-reranker-v2-m3` of pair BM25 | Pair BM25 (question) | 0 | 0 | 20 | 1.000 |
| Rerank `bge-reranker-v2-m3` of fused | Fused pair + vectors (question) | 1 | 0 | 30 | 1.000 |
| Rerank `ko-reranker` of fused | Fused pair + vectors (question) | 1 | 0 | 30 | 1.000 |

A rerank of the top 20 cannot change recall@20. At 5, `ko-reranker` found 5 questions that the fused list did not and lost 2 (p = 0.45).

### Identical copies

Counting identical text as separate documents, as an index of this import would, identical copies filled 3.2 of the top 12 places on average (up to 10) for pair BM25 on questions, and 2.6 for Kiwi content. Recall@12 then drops from 0.41 to 0.35 for pair BM25 and from 0.61 to 0.57 for vectors. Part of that drop is the evidence naming one copy while the list shows the other.

### Cost

On 42.9 MB of derived text (6,997 events):

| Index | Bytes | Build |
| --- | --- | --- |
| Trigram text (contentless FTS5) | 84.5 MB | 8.5 s with the pair posting |
| Pair posting (pair, document), as designed | 26.5 MB | included above |
| Pair BM25 (contentless FTS5 of pair tokens) | 19.1 MB | 6.2 s |
| Kiwi all / content / words+content | 17.1 / 13.7 / 26.7 MB | 22.1 s of analysis on 8 threads, then 10.1 s for the three |
| Vectors, `bge-m3`, float32 | 28.7 MB (4,096 bytes per event) | 1,481 s on 16 CPU threads (4.7 events per second) |

Kiwi needs its 105 MB model and a native library; LINA builds with `CGO_ENABLED=0`. A cross-encoder over 20 documents took 11 to 21 s per query at p50 on this CPU, including load from other jobs. A hosted rerank service is faster, but adds a network call, a key and a third party that receives the person's text.

## Decisions

Each decision is scoped to this question set and this history, with the number behind it. Confidence is medium throughout: 49 questions find large differences (ten questions against none) and cannot separate small ones (two against none).

| Piece | Decision | Number behind it |
| --- | --- | --- |
| Any-term BM25 instead of all terms | Adopt | All terms found nothing for any question as asked (0 of 49) and lost 8 to 0 against pair BM25 on short queries (p = 0.008). The shipped literal phrase lost 13 to 0. |
| Hangul pairs ranked by BM25 | Adopt | Pair BM25 beat trigram any-term ranking on questions, 10 to 1 (p = 0.012). The pair table as FTS5 is smaller than the designed posting table (19.1 against 26.5 MB). |
| Kiwi morphemes | Not now | A tie with pairs: 3 to 2 on questions, 3 to 4 on short queries (p = 1.0), 2 to 0 once fused with vectors (p = 0.5). It costs a native library, a 105 MB model and longer builds: 22.1 s of analysis on 8 threads, where the pair table took 6.2 s on one. |
| Vectors fused by reciprocal rank (#874) | Adopt, first | Recall@12 on questions from 0.41 to 0.61, 10 to 0 (p = 0.002); fusion lost no question that BM25 found. Constant 60; 10 to 100 moved recall@12 by at most one question. |
| Query as a sentence | Adopt | With vectors, the question as asked found 11 questions the short query missed, against 2 (p = 0.022). BM25 was indifferent (0.41 against 0.43). |
| Model rerank | No | Over fused lists, a cross-encoder of the top 20 gained one question at 12 and lost none, with either model (p = 1.0); over pair BM25 alone it gained none. `ko-reranker` raised MRR from 0.474 to 0.537. It took 11 to 21 s per query at p50 here, far outside the 250 ms packet cap. The design's deterministic ranking stays. |
| Query expansion (HyDE, multi-query) | No | Fused HyDE 2 to 0 (p = 0.5), fused multi-query 2 to 4 (p = 0.69). Each needs a model call of 1.9 to 3.5 s at p50, outside the 250 ms packet cap. |
| One hit per identical text | Adopt | Copies held 3.2 of the top 12 places on average. |
| Pipeline trials as a repeatable eval command | Adopt, without automatic choice | This study ran 38 configurations over the same 49 questions; only the paired counts separate real gains from noise. Picking the highest recall@12, as the reference's default strategy does with that metric, would have picked Kiwi content for lexical search on a one-question difference (21 against 20). |
| Judged evidence | Not now | It never reorders results, so it cannot change any number above, and LINA's answers do not yet cite recalled items (#873). |
| Version twins for file results (#1014) | Not now | No file corpus was measured. The reference itself collapses only exact duplicates at index time and only reports near duplicates. The exact-copy rule above is the part with a number. |
| HWP and HWPX parsing (#198, #200) | Not now | 47 events in the history name an HWP or HWPX file, 46 of them tool output, against 470 for PDF, 269 for DOCX and 240 for XLSX. Returned artifacts are filed by hash and provenance, not by their text. |
| Read-time source scope | No change | LINA's recall already applies scope first and checks visibility in one place ([Store and recall](../design/conversation-and-memory.md#store-and-recall)). |
| Min-max merge with a same-source bonus | Not measured; keep reciprocal rank | Reciprocal rank needs no score calibration between BM25 and cosine, and the reference uses it for its own similar-question search. |

What the decisions mean for the design:

- **Queries.** A document matches when it holds any term, and BM25 orders the result, so documents holding more of the terms usually come first. The all-terms rule in [Store and recall](../design/conversation-and-memory.md#store-and-recall) returns nothing for a question asked in full, which is what the per-turn query sends (#873).
- **One index.** The Hangul pair postings become a contentless FTS5 table whose tokens are the pairs and the words of other scripts, so BM25 can rank them. The trigram table stays for literal substrings of three or more characters, such as ids, paths and parts of words in other scripts.
- **Ranking.** Unchanged in shape: deterministic, BM25 fused with vector distance by reciprocal rank, ties to the newer event. The constant stays 60.
- **One recall entry.** A result counts once per identical derived text within a conversation, beside the existing lineage rule. A model's `recall` call passes the question as a sentence.

These change the contract text of [Store and recall](../design/conversation-and-memory.md#store-and-recall). The implementation issues below carry them, and each changes its section in its own pull request, as the work rules require.

## Issue updates

- [#869](https://github.com/thisisjun786/lina/issues/869), one index: the pair table becomes a contentless FTS5 table of pair tokens and other-script words, so BM25 can rank it; the trigram table stays; no morphological analyzer.
- [#871](https://github.com/thisisjun786/lina/issues/871), `recall`: any term matches and BM25 orders; one hit per identical derived text within a conversation; no model rerank; the `recall` tool asks for a sentence; recall at 12 on the #849 set before and after. Its pull request changes [Store and recall](../design/conversation-and-memory.md#store-and-recall) (Queries, One recall entry).
- [#874](https://github.com/thisisjun786/lina/issues/874), vectors: the largest measured gain, so it goes first among the quality steps; fusion constant 60; `bge-m3` as the measured candidate for the default model; 4,096 bytes per document at float32.
- [#867](https://github.com/thisisjun786/lina/issues/867), store benchmark: its private-snapshot loader is reused by the eval command, and it reports the pair table's bytes in the new shape.
- New: [#1117](https://github.com/thisisjun786/lina/issues/1117), the recall eval command: named recall configurations over a private question set, with the paired counts of this note. Depends on #867 and #871.

No issue was filed for Kiwi, rerank, query expansion, judged evidence, version twins or HWP parsing: each is "not now" or "no" above, and the eval command is how a later question set reopens one.

## Open items

- **Index of every kind.** This study indexed the person's and forwarded events only. Agent replies, tool output and automation (about 10 times the documents) add distractors to every candidate. The eval command reruns these candidates over an index of every kind.
- **Identical copies.** Whether the shipped importer, with complete UID bindings, merges the two Hermes copies of a message is for #849 to confirm.
- **Kiwi on more questions.** A tie on 49 questions does not exclude a small gain. A larger owner set through the eval command can reopen it.
- **Answers.** This measures what recall returns, not whether an answer built from it is right; that stays with #849.
- **Endpoint latency.** Query embedding here ran on the local CPU (131 ms p50). The per-turn query must embed within the packet cap through the configured endpoint (#873, #874).

## Privacy and cleanup

The snapshot, corpus, indexes, vectors, expansions and ranked lists stayed in a private directory outside the repository, readable only by the owner's account, and were deleted after the run. Only the question text was sent to the model gateway. No message text left the machine.
