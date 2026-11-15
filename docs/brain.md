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
  hand-written entry, is `external`, the least trusted grade. Nothing reads the grade yet; a later issue wires its
  meaning into injection and promotion.

The parser is lossless: every byte of a file lands in exactly one part, and `render(parse(text)) == text` holds for
any file, hand-written or not. The importer never needs to rewrite a file, and it never does.

### No status, no deadlines

The work ledger (`lina work`) is the only authority for status and deadlines. A page never stores them. The
renderer refuses `status`, `deadline`, `due` and `due_date` in new pages, and the frontmatter copy in
`brain.pages` leaves those keys out (a check constraint enforces it). A hand-written file that has them keeps them;
the import counts such files and indexes the rest of their frontmatter.

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
| `edges` | `from_slug`, `to_slug`, `type`, `source_ref`, `valid_from`, `valid_until` | shared |
| `timeline` | one row per timeline entry: page, path and line, date, `grade` (`owner`/`agent`/`external`, default `external`), source ref, text | shared |
| `atoms` | short claims a later cycle extracts from a page, with grade and source ref | shared |
| `merges` | recorded merges, replayed by every import and reindex | shared |
| `proposals` | changes an agent's cycle suggests (compiled-truth rewrite, merge, closing an edge, promotion, timeline line), `open`/`accepted`/`rejected` | **agent** (`agent_id`) |

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
  move any file, write `RESOLVER.md` or per-folder `README.md` files, or link ledger goals to project pages.
- **The nightly cycle** (lina#443, lina#444): lint, backlinks, link upkeep, the orphan report after a cycle, atoms,
  proposals and LLM phases. `atoms` and `proposals` exist but nothing writes them yet.
- **Grades** (lina#440): stored, defaulting to `external`. Injection and promotion do not read them yet.
- **Honcho and person pages** (lina#445): which facts live in a person page and which in Honcho conclusions is
  still the owner's decision. Nothing here reads or writes Honcho.
- **Search** (lina#446): page vectors and a Korean keyword index. No page is embedded yet.
- **Frontmatter `type` labels** that are not page types (a wiki's own labels) are kept in the frontmatter copy but
  do not decide the type. An owner mapping for such labels could come later.
