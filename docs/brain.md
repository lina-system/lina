# The brain

The brain is one linked index of what LINA knows about people, organisations, projects and topics (lina#438,
design lina#439 sections 1 and 3). Markdown pages stay the original. `lina brain` indexes them into schema `brain`
of the LINA store ([store.md](store.md)), and the index can always be rebuilt from the files. This first part
(lina#442) holds the page model, the schema and the import of an existing wiki. The nightly cycle, search and the
link to Honcho come later (see "Not done here").

The page model follows GBrain (garrytan/gbrain, MIT): compiled truth over an append-only timeline, typed pages
and typed links. It is ported to Python on the LINA store (option (a) of lina#438), so the agent_id row-level
security, the forget path and the shared `vec` ranking cover it like every other memory table.

## Page types

| Type | For |
| --- | --- |
| `person` | one person |
| `organization` | a company, label, team or institution |
| `project` | something with an end, often a ledger goal |
| `program` | an ongoing responsibility (an area) |
| `concept` | a topic, method or term |
| `idea` | a thought not yet acted on |
| `source` | a document, article, interview, recording or chat export the brain draws on |
| `meeting` | one meeting or call |
| `writing` | a text the owner writes or wrote |

## Page shape

```markdown
---
type: person
title: 미나
aliases:
  - Mina
---

One summary paragraph, then what is known now. Rewritten when new information arrives.

---

## Timeline

- 2026-03-04 | owner | messages:room-1 | met at the studio
- 2026-03-09 | external | mail:4711 | sent the contract
```

- **Frontmatter** (optional): YAML between two `---` lines at the top. `type`, `title` and `aliases` are read;
  every other key is kept as is.
- **Compiled truth**: everything after the frontmatter up to the timeline divider.
- **Timeline divider**: a `---` line whose next text line is a `## Timeline` (or `## 타임라인`) heading. Any other
  `---` is a horizontal rule and stays in the compiled truth. A page without the divider has no timeline.
- **Timeline entries**: each bullet (`- ` or `* `) below the heading is one entry, with the indented lines under it.
  An entry written by LINA is `- YYYY-MM-DD | <grade> | <source ref> | <text>`. A hand-written entry that starts
  with a date (`- 2026-03-04 ...`, `- **2026-03-04** ...`) is dated. Any other bullet is an undated entry.
  Entries are only ever appended (`hermes_cli/brain/page.py` `append`).
- **Grade** (lina#439 section 1): `owner`, `agent` or `external`. An entry without one, which covers every
  hand-written entry, is `external`, the least trusted grade. The person card reads it (Person pages, below);
  promotion does not yet.

The parser is lossless: every byte of a file lands in exactly one part, and `render(parse(text)) == text` holds for
any file, hand-written or not. The importer never needs to rewrite a file, and it never does.

### No status, no deadlines

The work ledger (`lina work`) is the only authority for status and deadlines. A page never stores them. The
renderer refuses `status`, `deadline`, `due` and `due_date` in new pages, and the frontmatter copy in
`brain.pages` leaves those keys out (a check constraint enforces it). A hand-written file that has them keeps them;
the import counts such files and indexes the rest of their frontmatter. A project page generated from a ledger goal
holds placeholders where it would name a status or a deadline, resolved from the ledger at read time (see "Project
pages from the ledger").

### Slugs and names

- A page's slug is its file name: NFC, lower-case, Hangul kept, and every run of characters other than letters,
  digits, `_`, `.` and `-` becomes one `-` (`김미나 프로젝트.md` → `김미나-프로젝트`). A decomposed (NFD) name,
  as macOS writes them, gives the same slug.
- When two files share a slug, each takes the slug of its path instead (`concepts/notes`, `ideas/notes`). When
  even the paths collide (`A.md` beside `a.md`), the first 8 hex digits of the path's SHA-256 are appended.
- `page_id` is derived from the slug (63 bits of its SHA-256), so a page keeps its id, and its vector, across
  a reindex.
- A page is named by its file name, slug, title, frontmatter `name` and `aliases`, and the names of the pages
  merged into it. Names are compared NFC, case-folded, whitespace collapsed (`brain.aliases.alias_key`).

### Links

Links are found by pattern matching, with no model call:

- `[[name]]`, `[[name|label]]`, `[[name#heading]]` and `[[folder/name]]`;
- markdown links to another `.md` file under the same directory (`[text](../concepts/mixing.md)`).

Links in code spans and fenced code blocks are not read. A web link is not a page link.

A link resolves, in this order: to the page at that path; to the page whose name it is; for a `folder/name` link,
to the page named by its last part. When one name belongs to several pages, a file name beats a title, and a title
beats a frontmatter alias. Only a true tie stays ambiguous and makes no edge. A Korean particle after the brackets
(`[[미나]]의`) is outside the link and never part of the name. When the brackets themselves end in a particle
(`[[미나의]]`) and the whole name names no page, the particle is dropped and the name tried again.

Each resolved link is one edge of type `mentions`, with open validity and `source_ref` set to the file the link is
written in. Korean text has no relation-verb extraction, so every link is a `mentions` for now. A link to the page
itself makes no edge.

### Merging pages

`lina brain merge FROM INTO [--reason ...]` records that page `FROM` folds into page `INTO` (both from the same
directory) in `brain.merges`, then syncs the directory again. On every import and reindex, the recorded merges are
replayed in order: `FROM`'s names become aliases of `INTO` (origin `merge`), edges to and from `FROM` re-point
to `INTO`, its timeline entries count for `INTO`, and its atoms move to `INTO`. A link a file gains later to
one of `FROM`'s names lands on `INTO`. The file of `FROM` is not changed, moved or deleted; that is a file
change for a later proposal step.

## The resolver: folder to type

A page's type comes from:

1. its frontmatter `type`, when that is one of the nine page types (a page the renderer wrote always has one);
2. else the folder it sits in, through the mapping;
3. else the mapping's `default` (`concept`). The import lists every folder that fell to the default.

The mapping is a YAML file the owner can edit. `lina brain mapping` prints the built-in one:

```yaml
default: concept
folders:
  people: person
  사람: person
  인물: person
  organizations: organization
  조직: organization
  projects: project
  programs: program
  areas: program
  concepts: concept
  ideas: idea
  sources: source
  _sources: source
  meetings: meeting
  writing: writing
  # ... (see hermes_cli/brain/resolver.py DEFAULT_MAPPING for the full list)
```

A key without `/` matches a folder name at any depth. A key with `/` matches a folder path from the top of the
imported directory. Keys compare case-folded and NFC. The deepest matching folder wins, so
`_sources/인물/x.md` is a `person` under the built-in mapping. An owner mapping that wants it as a `source`
says `_sources/인물: source`.

Pass an edited mapping with `--mapping FILE`. The directory remembers the mapping it was last imported with
(`brain.roots.mapping`), and later imports and reindexes reuse it until another `--mapping` is given.

## Importing a wiki

```sh
lina brain mapping > ~/brain-types.yaml        # edit it for the wiki's folders
lina brain import ~/Desk/Wiki --mapping ~/brain-types.yaml
lina brain import ~/Desk/Wiki                  # again: "Changes: none."
lina brain import ~/Desk/Wiki --json           # the full report, every skipped file with its reason
```

The command runs as the store owner (role `lina`, through `store/store.env`). Files are opened for reading only.
Every file under the directory is either a page or listed as skipped with one of these reasons:

| Reason | What |
| --- | --- |
| `metafile` | `index.md`, `README.md`, `SCHEMA.md`, `RESOLVER.md` (any case) |
| `sync conflict copy` | Synology Drive `_Conflict`, Dropbox `conflicted copy`, `(conflict ...)`, Syncthing `.sync-conflict-` |
| `sync artifact folder` | everything under `@eaDir`, `#recycle`, `#snapshot`, `.SynologyWorkingDirectory` |
| `git folder` | everything under `.git` |
| `hidden folder`, `hidden file` | names starting with `.` |
| `not markdown` | files that do not end in `.md` |
| `symlink` | symlinks, never followed |
| `not UTF-8`, `contains NUL bytes` | files that cannot be stored as text |
| `slug used by another imported directory` | a page whose slug a page of another directory already has |

A file name that starts with `YYMMDD` (read as 20YY) or `YYYY-MM-DD` gives the page its date (`brain.pages.dated`).

The report says: files in, pages out, pages merged, skipped files by reason, links (found, resolved, resolved
after dropping a particle, unresolved, ambiguous, to the page itself), edges, orphans (pages with no edge in or
out), types and what decided them, folders that fell to the default type, files that hold status or deadline
keys, files whose frontmatter is not YAML (indexed without it), and what changed in the store. `files in` always
equals pages out + merged + skipped.

The sync runs in one transaction and writes only rows that differ from the files. An unchanged directory imported
again changes nothing. An edited file changes only its own rows. A removed file takes its page, aliases, edges
and timeline rows with it, and its atoms are deleted (or move with a recorded merge).

### Reindex

```sh
lina brain reindex ~/Desk/Wiki
```

deletes the directory's pages, and with them their aliases, edges and timeline entries, then writes everything again
from the files in the same transaction. The result is the same index as before. Recorded merges and atoms are not
derived from files and are kept. Timeline entry ids are new. Page ids are not, because they come from the slug.

## Store schema

Migration `services/store/migrations/0014_brain.sql`, schema `brain`:

| Table | Holds | Scope |
| --- | --- | --- |
| `roots` | each imported directory and the mapping it was imported with | shared |
| `pages` | one row per page: slug, `page_id`, path, type, title, date, frontmatter (no ledger keys), compiled truth, file SHA-256 | shared |
| `aliases` | names of each page (`alias_key`, `alias`, origin `slug`/`title`/`frontmatter`/`merge`) | shared |
| `edges` | `from_slug`, `to_slug`, `type`, `source_ref` (the file the link is written in, or `ledger:<goal id>` for a project page's link), `valid_from`, `valid_until` | shared |
| `timeline` | one row per timeline entry: page, path and line, date, `grade` (`owner`/`agent`/`external`, default `external`), source ref, text | shared |
| `atoms` | short claims a later cycle extracts from a page, with grade and source ref | shared |
| `merges` | recorded merges, replayed by every import and reindex | shared |
| `proposals` | changes an agent's cycle suggests, `proposed`/`accepted`/`rejected`/`applied` (columns: "The nightly cycle" below) | **agent** (`agent_id`) |

The brain is one brain for every agent (lina#439, "one brain with trust grades"): there is no per-agent copy of a
person. So the tables that hold knowledge carry no `agent_id`. `lina_app` may only SELECT them, and only the
owner tools (`lina brain`, as role `lina`) write them, the way only the owner writes the shared external messages
([store.md](store.md)). Trust is carried by the grade on each entry, not by the row's owner.

A proposal is made by one agent's cycle and reviewed before anything changes, so `brain.proposals` carries
`agent_id` and gets the `lina_agent_rows` policy: an agent sees and writes only its own proposals. The migration
widens the row-level security event trigger from services/store/initdb (`public.lina_agent_rls_on_ddl`) to
schema `brain`, so any later brain table with an `agent_id` column gets the policy too.

Vectors live in `vec.embeddings`, not in `brain`. The migration adds the kind `page` (`item_id` =
`brain.pages.page_id`). Page vectors carry the shared owner `*`, like the external message vectors (migration
0009): no agent's `lina_agent_rows` can write one, and the SELECT-only policy `lina_shared_page_vectors` lets every
agent read them. A page's vector is deleted when the page is deleted or its file changes (trigger
`pages_drop_vector`). Nothing writes page vectors yet.

## What is deliberately not stored

- Status, deadlines, owners and dependencies: the work ledger's (`work` schema) alone.
- The file bytes: the markdown file stays the original. `brain.pages` holds the compiled truth and the parsed
  timeline for search and linking, plus the file's SHA-256 to see changes.
- Links that resolve to no page (unresolved or ambiguous): counted in the report, not stored.
- Web links: not page links.

## Not done here, and open decisions

- **PARA.** The owner does not classify (structure paper, 11-11). The brain is to link areas, projects and goals
  behind the scenes. This issue only maps folders to page types. It does not fold PARA folders into brain folders,
  move any file, or write `RESOLVER.md` or per-folder `README.md` files. Ledger goals become project pages
  through `lina brain projects sync` (below), not through a folder.
- **The nightly cycle** (lina#444): described below. Accepted proposals of kinds that would rewrite a file (a new
  page, an addition to compiled truth) are recorded, not carried out; no phase auto-applies yet.
- **Grades** (lina#440): stored, defaulting to `external`. The person card reads them (lina#445); promotion does
  not yet.
- **Honcho and person pages** (lina#445): done, see "Person pages" below.
- **Search** (lina#446): see "Search" below.
- **Frontmatter `type` labels** that are not page types (a wiki's own labels) are kept in the frontmatter copy but
  do not decide the type. An owner mapping for such labels could come later.

## Deterministic upkeep

```sh
lina brain upkeep --root ./wiki
lina brain upkeep --root ./wiki --json
lina brain upkeep                         # every imported root, in path order
```

Upkeep reads the files and their existing index once, without a model, embeddings, external API calls or a cycle
lock. It uses the mapping and merges recorded by import. All file reading and analysis happen outside database
transactions; each missing edge is inserted in its own short transaction. Files are never written. Run
`lina brain import DIR` first, and again after adding, removing or renaming pages: upkeep reports unindexed pages
but does not create pages or remove existing indexed edges. An unknown `--root` is an error.

The report separates `written_links` (wiki and Markdown links) from `inferred_mentions` (plain text naming an
indexed title or alias). Both use type `mentions` and the source file's relative path as `source_ref`, exactly
like import. If the same file has both forms for the same target, it is reported as written and stored once.
Code spans, fenced blocks, frontmatter and the text inside written links do not infer mentions.

Korean particles (`은/는/이/가/을/를/의/와/과/에/에서/으로/로/도/만/께/에게`, among others), compound particles
such as `에게도`, and common name endings such as `입니다`, `이에요`, `예요`, `이세요`, `이군요` and `이네요`
can follow a name. Names are NFC-normalized and case-folded; longer names are tried first, with word boundaries
on both sides of the complete name and suffix. A short name inside a longer Hangul word is not a mention.
Bracket links also accept these suffixes when the full name does not resolve.

`new_links` counts rows actually inserted. `fixed_backlinks` counts newly inserted edges whose reverse already
exists in the index. A reverse edge is inserted only when the reverse page's body supplies that link or mention.
Otherwise `missing_backlinks` identifies the missing direction and the file that would need it; upkeep never
invents reciprocal evidence. Orphans have no incoming or outgoing link in either the scanned graph or the existing
index, including other typed edges and edges from another imported root.

Lint is report-only. Every text finding is one line with the relative file path, rule name and detail:

| Rule | Meaning |
| --- | --- |
| `missing-title` | No nonempty frontmatter title or level-one heading outside code; the filename fallback does not count |
| `duplicate-slug` | Multiple files share the original filename slug, even when import disambiguated their paths |
| `duplicate-alias` | One normalized name belongs to multiple pages |
| `broken-link` | A `[[link]]` has no target or is ambiguous |
| `timeline-order` | A dated heading below the timeline divider goes backwards; equal dates are allowed |
| `unknown-type` | An explicit type is not a page type, or a folder fell through the mapping to its default |

Upkeep uses the importer's scanner and its single set of exclusions. Conflict-copy directories are excluded as
well as conflict-copy files; `@eaDir`, `#recycle` and the other exclusions in the import table apply to both.

## Search

Agents look the brain up first (lina#438 item 6, lina#439 section 3) with three tools of `lina-memory`
([memory-layer.md](memory-layer.md), One search): `brain_search` finds pages (and work items, repo files and
memory), `brain_get` opens a page, `brain_links` walks its links. The brain side is `hermes_cli/brain/search.py`.

```sh
lina brain import ~/Desk/Wiki
lina brain search-index            # after every import; --root DIR for one directory, --json for the report
lina brain search-eval tests/fixtures/brain_search/queries.json
```

`search-index` runs as the store owner and writes only what is out of date:

- **Page vectors** in `vec.embeddings` (kind `page`, owner `*`, migration 0014), one per page, with the home's
  embedding model (`embedding:` in config.yaml), of the page's title, its other names and its compiled truth (wiki
  links read as their words, at most 4,000 characters). A page with a vector of the model is skipped; a changed file
  has already lost its vector (trigger `pages_drop_vector`). Without an embedding endpoint no vectors are written and
  the report says so.
- **Keyword terms** in `brain.search_docs` and `brain.search_terms` (migration `0021_brain_search.sql`, shared and
  SELECT-only for `lina_app` like the other brain tables). Korean writes particles and endings onto words (서윤이,
  푸른소리랑) and joins or spaces compounds at will (기타줄, 기타 줄), so a term is not a space-separated word.
  Two analyzers are kept side by side: `kiwi` (Kiwi, the `korean` extra: nouns as written, predicates in their
  dictionary form, and each run of nouns joined) when Kiwi can be imported, and `bigram` (character bigrams of
  every Hangul run, other words whole) always. A page's title and names count three times. A page whose
  `content_sha256` changed is indexed again; a page that goes takes its terms with it.

`brain_search` ranks pages by two legs and fuses them with reciprocal rank fusion, with the other sources'
lists: the semantic leg (nearest page vectors to the query's vector) and the keyword leg (BM25, k1 1.2, b 0.75,
over the analyzer this process can produce: Kiwi when importable and indexed, else bigrams). Kiwi is never
installed inside a turn. A leg with no index is named in `skipped`; with neither, the brain is skipped with the
hint to run `search-index`. A page is current knowledge, so its age does not lower its rank.

A page hit's ref is `page:<slug>`, with its `title`, `type` and `path`. Its grade (lina#440) is the frontmatter
`grade` when that is `owner`, `agent` or `external`, else `external`: nothing else grades compiled truth yet, and an
item of unknown origin is the least trusted. `quote_as_external` is true for every hit and timeline entry not graded
`owner` or `agent`.

`brain_get` takes a ref, a slug, a path (with or without `.md`) or any name of a page (a name of two pages is an
error that lists both). It returns the compiled truth (paged like every original, `offset`), the page's names, its
root, and its newest timeline entries (`timeline`, default 20, at most 100) with `timeline_total` and
`timeline_truncated`; each entry carries its date, grade, source ref and `quote_as_external`. `brain_links` returns
the pages one or two links away (`depth`), by edge type (`relations`) and direction (`out`, `in`, `both`), nearest
first, with the edges between them; an edge whose `valid_until` has passed is not walked.

### Measured: the 20-query Korean set

`tests/fixtures/brain_search/` holds a synthetic wiki of 193 pages (Korean titles and aliases; people,
organizations, projects, programs, concepts, ideas, sources, 48 dated meetings, writing) and 20 queries with the
pages each should find: 7 paraphrases, 6 names and aliases written with particles or other spacing, 7 Korean forms
(particles, endings, compounds written together, a date). `lina brain search-eval` runs every query through
`brain_search` for each leg configuration and prints recall@5 and the queries each misses. On 2026-11-15, with
Qwen3-Embedding-0.6B (Q8, the small sibling of the default model) for the semantic leg:

| Legs | recall@5 | hit@1 | MRR@5 | misses |
| --- | --- | --- | --- | --- |
| semantic | 0.950 | 0.85 | 0.887 | q18 ("3월 4일 정산 회의에서 정한 것": three other 정산 meetings ranked first) |
| keyword, Kiwi | 1.000 | 0.95 | 0.975 | none |
| keyword, bigrams | 1.000 | 0.85 | 0.917 | none |
| semantic + keyword (Kiwi) | 1.000 | 0.90 | 0.935 | none |
| semantic + keyword (bigrams) | 1.000 | 0.90 | 0.935 | none |

The semantic leg alone misses a query, so the keyword leg is on. The pages and the queries were written together,
so they share more words than an owner's own wiki and questions will; measure again with the home's own model and a
set drawn from real questions before tuning weights.

The JSON and text reports contain no timestamp or elapsed duration. On unchanged input a second run reports zero
new links and zero fixed backlinks, with identical lint and orphan lines. Subsequent unchanged runs are byte-for-byte
identical. The first report's change counters and added-edge lists naturally differ when it repaired the index.

## The nightly cycle

```sh
lina brain dream --plan              # what would run and cost; no model call, nothing written
lina brain dream                     # one cycle; prints one line and the path of the report page
lina brain dream --phase drift       # one phase only (any of the eight below)
lina brain dream --json              # the full report
lina brain dream --metrics           # the counters of finished cycles
lina brain proposals list [--status proposed|accepted|rejected|applied|all] [--phase P] [--json]
lina brain proposals accept ID [--confirm-external] [--by WHO]
lina brain proposals reject ID [--by WHO]
lina brain proposals rates [--json]  # acceptance per phase
```

One cycle runs these phases in order (`hermes_cli/brain/dream/`, after GBrain's `runCycle`):

| Phase | Model | Writes |
| --- | --- | --- |
| `upkeep` | no | missing link edges, as `lina brain upkeep` does for every imported root |
| `orphans` | no | nothing; lists pages with no edge in or out |
| `atoms` | yes | `brain.atoms`: one to three short claims per page, each citing the page text or timeline entries it rests on |
| `patterns` | yes | proposals `new_page`: themes that recur across claims of at least two pages (claims of the last `pattern_days`) |
| `concepts` | yes | proposals `new_page`: claims grouped by concept; a group of at least `concept_min_group` claims that names no page yet, with its size tier (10, 5, 2, 1) |
| `consolidate` | yes | proposals `compiled_truth` (one claim for several, on pages with `consolidate_min_atoms` claims, the oldest at least `consolidate_min_age_hours` old) and `merge` (two pages of one directory that share a name and are about the same subject) |
| `contradictions` | yes | proposals `contradiction` (claims and dated entries of one page that cannot all hold) and `close_edge` (open typed relations of one page and type that cannot all still hold; `mentions` edges are not relations) |
| `drift` | yes | proposals `compiled_truth`: a statement of the compiled truth, quoted exactly, that later timeline entries show has changed, with the suggested statement |

The deterministic phases take no model call and no cycle lock. The model phases run under one lock, a row of
`brain.cycle_lock` that expires after `lock_ttl_minutes` and is renewed before every model call. When another
cycle holds it, the model phases are skipped and the report says who holds it until when; a lock whose holder
died is taken over once it has expired. No phase edits a page, a file or an edge.

### The model sees data, and answers in a fixed shape

A model phase gets a callable `complete(prompt) -> str` and nothing else: no tools. In production it is the
auxiliary client's task `brain_dream` (`auxiliary.brain_dream` in config.yaml chooses the model, as for every
auxiliary task); tests pass a fake. Every page text, timeline entry or claim enters the prompt as one numbered item
of the quoted data block (`plugins/memory/lina/grade.py` `data_block`) with its source, date and grade, so text in
a page can neither end the block nor pass for an instruction. The answer must be one JSON object of the phase's
shape, citing items by number. An answer that is not JSON, has another shape, cites an item that is not there, or
(`drift`) does not quote the compiled truth exactly is counted as invalid and writes nothing; its input is asked
again on the next night.

### Budgets

Each model phase spends from its own budget, `brain.dream.budget.<phase>` in config.yaml (defaults in
`hermes_cli/config_defaults.py`):

```yaml
brain:
  dream:
    budget:
      atoms: {calls: 100, input_chars: 400000}
      patterns: {calls: 1, input_chars: 60000}
      drift: {calls: 20, input_chars: 100000}
    report_dir: ""                 # "" = <HERMES_HOME>/brain/dream; never inside an imported wiki
    lock_ttl_minutes: 30
```

A call is made only while it fits: the first request that would pass `calls` or `input_chars` stops the phase,
and the line and the report say which budget stopped it. The next phase starts with its own budget. A subject a
phase already answered with the same input (`brain.dream_inputs`, keyed by the prompt's SHA-256) is not sent
again, so a night on an unchanged brain costs nothing. A model call that fails ends its phase, is reported, and
makes the run exit 1; the other phases still run.

### Proposals

| Column | Holds |
| --- | --- |
| `proposal_id` | the id `accept` and `reject` take |
| `agent_id` | the agent whose cycle made it (the profile); row-level security `lina_agent_rows` |
| `phase` | the phase or other writer that made it |
| `kind` | open text: `new_page`, `compiled_truth`, `merge`, `contradiction`, `close_edge`, and kinds later writers add (promotions, lina#441) |
| `target` | the page slug it is about, or `from->to` for a relation |
| `payload` | what the kind needs: the text to add or the suggested statement, the merge's `from`/`into`/`reason`, the edge, the cited atom ids |
| `grade` | the lowest grade of the material it is built from (lina#440); a page's compiled truth is of unknown origin and so `external`; a timeline entry and an atom carry their own |
| `source_refs` | the files, timeline entry sources and `atom:<id>` it cites |
| `status` | `proposed`, then `accepted`, `applied` or `rejected` |
| `cycle_id`, `fingerprint` | the cycle that made it; one fingerprint per agent, so a decided or open proposal is not made again |
| `created_at`, `decided_at`, `decided_by` | when it was made and decided, and by whom (`--by`, default `owner`) |

`accept` applies only what loses no text: a `merge` runs the recorded merge of `lina brain merge` (no file is
changed) and a `close_edge` sets the edge's `valid_until` to today (the row stays); both become `applied`. Any
other kind becomes `accepted` and is carried out by hand. A proposal graded `external` is refused unless
`--confirm-external` says its source is confirmed. Only a `proposed` row can be decided.

### Report and counters

The one line gives new links, atoms, proposals by phase, contradictions, orphans of all pages and the spend (model
calls and input characters), plus every phase a budget stopped, a failure, or a held lock. The report page,
`<report_dir>/<date>-cycle-<id>.md`, adds a table per phase and the orphan list. The cycle refuses a
`report_dir` inside an imported directory.

Each finished cycle is a row of `brain.dream_cycles`. The view `brain.dream_metrics` gives per cycle: orphan
rate, the share of pages first seen in that cycle (`brain.dream_pages_seen`) that had a link by its end, new
links, calls, input and output characters, and proposals made. `brain.proposal_acceptance` gives per agent and
phase the proposals, open, accepted (with applied), applied, rejected and the acceptance rate over decided ones.
Both views are `security_invoker`, so an agent sees only its own rows. A phase moves to auto-apply only once its
acceptance rate has been measured over two weeks; nothing auto-applies yet.

### Scheduling

Nothing installs a schedule. A user crontab line for 03:30 every night (cron mails the one line):

```cron
30 3 * * * lina brain dream
```

or a systemd user timer:

```ini
# ~/.config/systemd/user/lina-brain-dream.service
[Service]
Type=oneshot
ExecStart=lina brain dream

# ~/.config/systemd/user/lina-brain-dream.timer
[Timer]
OnCalendar=*-*-* 03:30
Persistent=true

[Install]
WantedBy=timers.target
```

enabled with `systemctl --user enable --now lina-brain-dream.timer`.

## Project pages from the ledger

A ledger goal (`lina work`) is a `project` page. The page is generated, never hand-written:

```sh
lina brain projects sync            # write and index one page per goal, link it both ways
lina brain projects sync --json     # the full report
lina brain projects show G1         # the page as read now: ledger status, tasks, links both ways
lina brain projects show goal-g1 --json
```

- **Where.** Pages are files under `projects/` of the brain work folder: `brain.work_dir` in the root
  `config.yaml` (`~` expanded, a relative path is under the LINA root), default `<LINA root>/brain`. The folder is
  indexed as one more brain root with the importer's own scanner, so `lina brain import` of it agrees with the sync.
  The sync refuses a work folder that lies inside an imported wiki, holds one, or is one: no file of an imported
  directory is ever written. The Desk wiki stays the original for hand-written pages.
- **Shape.** Slug `goal-<id>` (`goal-g1`); frontmatter `type: project`, `title` (the goal's title), `aliases:
  [<id>]` (so `[[G1]]` names it) and `ledger_goal: <id>`; compiled truth: the goal's why and done text and its task
  list (every task of the goal, whatever its state, so a state change never changes the list). A `[[link]]` in the
  goal's text is written as its plain label: the page's links are the edges below, and a written link to a page of
  another root would read as broken to the work folder's own lint. A timeline a page gains later is kept when the
  page is generated again.
- **Status is read, never stored.** Where the page would say a status or a deadline it holds a placeholder,
  `{{ledger:G1:status}}`, `{{ledger:G1:deadline}}`, `{{ledger:T3:status}}`. `show` resolves them at read time from
  the ledger's own state views (`v_goal`, `v_todo`), so a status change through `lina work` shows on the next read
  with no page edit and no sync. Placeholders were chosen over index columns because a column is a second copy that
  is only as fresh as the last sync; a placeholder has no copy to go stale, and neither the file, `brain.pages`, nor
  compiled truth ever holds a status.
- **Links both ways.** The goal's text (title, why, done, and each task's title, why and done) is matched against the
  names of every page outside the project folder with the upkeep matching: `[[links]]`, and plain names with Korean
  particles and endings (`미나와` names `미나`). Project pages are not targets, so a goal's own title never hides a
  shorter page name inside it. Each named page gets two `mentions` edges, project to page and page to project, with
  `source_ref` `ledger:<goal id>`; no wiki file is written for the backlink. An import or upkeep of a directory never
  counts or removes these edges (they are not derived from its files); a `reindex` of a wiki deletes its pages and
  their edges with them, and the next `projects sync` adds them again.
- **Goals gone from the ledger.** A page whose goal the ledger no longer has keeps its file, row and links and is
  marked `archived: true`; `show` says `gone from the ledger` for its status.
- **The report** says goals, pages, pages created, updated and archived, links added from goals to pages and back,
  and links removed (a page the goal's text no longer names). The sync reads files and the ledger outside the store
  transaction, takes no model call, and an unchanged ledger reports `Changes: none.` with no file written.

## Person pages

A person's durable facts live in one place: their page of type `person` (lina#445, design lina#439 section 1).
Honcho keeps conversation memory and goes on deriving conclusions, but it is not where a person's facts are kept
or read from (`hermes_cli/brain/people.py`).

```sh
lina brain people link mina gateway_user:telegram:1001   # or local_user:owner, or a person id person:...
lina brain people card local_user:owner                  # the card a new session injects
lina brain people import-honcho [--json]                 # Honcho's existing conclusions, once
```

- **Which page is whose.** A page names its person in the frontmatter: `person_id: person:<hash>`, the id of the
  identity registry ([memory-layer.md](memory-layer.md)). The link lives in the file, so it survives every reindex,
  and no table holds it. `lina brain people link PAGE PERSON` writes the key (adding a frontmatter block when the
  file has none; every other byte stays) and indexes the directory again. PERSON may be one of the person's handles:
  it resolves to the person that handle is confirmed as. A person has one page: linking a second page is refused,
  and when two files name one person by hand, neither is used and the log says which.
- **Honcho's conclusions.** Each conclusion about the person is appended to the page's timeline as
  `- <date it was made> | agent | honcho:<conclusion id> | <text on one line>`. An id already on the page is
  never written again, so every write is idempotent. The file only grows: what was written stays byte for byte,
  and compiled truth is never rewritten (a change to it is a proposal, lina#444). lina-memory writes the
  conclusions of the session's person when a session ends; `lina brain people import-honcho` copies the existing
  ones of every linked person once, reading the conclusions the person's Honcho peers (in this home's workspace)
  hold about themselves and the ones the agent's own peer holds. Nothing in Honcho is changed or deleted. Writers of
  one file take a lock in the store, and the directory is synced into the index as the store owner, as by `import`.
- **The card.** The person card in the system prompt of a new session (`session_card_block`, the "Notes and
  person card" part of the context order) is built from the indexed page: `## Person card: <title>`, the compiled
  truth, then the newest timeline entries that fit in 2400 characters, oldest first. Owner and agent entries are
  lines with their grade and source. An `external` entry, which covers every hand-written entry without a grade,
  appears only inside the quoted `<external-data>` block ([memory-layer.md](memory-layer.md), Trust grades).
  Honcho's session summary and the agent's own sections follow the card; Honcho's own representation and peer card
  of the person are left out. A person with no page keeps Honcho's card until a page is linked. An edited file is
  read after `lina brain import` or `reindex`, by the next session.
