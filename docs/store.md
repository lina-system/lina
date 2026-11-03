# The memory store

All memory lives in one Postgres with pgvector, run in Docker and set up by LINA. The Honcho server
(api and deriver), built from `services/honcho`, runs beside it and keeps its tables in schema `ltm`.

| Schema | Holds |
| --- | --- |
| `memory` | messages |
| `ctx` | context summaries |
| `ltm` | long-term memory (the Honcho server's tables) |
| `vec` | embeddings |

## Installing

```sh
lina store install   # also run by `lina install`
lina store status
```

`install` needs Docker (Docker Desktop on a Mac). It builds the Honcho image from
`services/honcho`, starts the database, runs Honcho's migrations as one job, starts the api and the
deriver, and runs the smoke test (`GET /health`, `POST /v3/workspaces/list`, and the Alembic
revision in `ltm.alembic_version`). Run on a machine that already has a store, it upgrades instead
(see below).

The files are in `services/store/`: `compose.yaml` (compose project `lina-store`) and
`initdb/10-lina-store.sh`, which runs once on an empty cluster. They and `migrations/` are package data of
`services.store`, so an installed wheel carries them; the Honcho build context (`services/honcho`) is not
packaged, and `install` needs a source checkout for it. One store serves every profile, so
its settings are under the LINA root, not a profile home, in `store/`:

| File | What it is |
| --- | --- |
| `store.env` | Generated on first install (mode 0600): the three role passwords, the ports (Postgres `54320`, Honcho `8000`, both on 127.0.0.1) and the data volume's name |
| `honcho.env` | Honcho settings such as LLM and embedding keys (`LLM_*`, `EMBEDDING_*`); read by the api, deriver and migration job. Install and upgrade add the embedding defaults it does not set yet (see Configuration) and leave the lines already there alone |
| `backups/` | Backup generations |

## Roles and row-level security

| Role | Used by | Can |
| --- | --- | --- |
| `lina` | `lina store` (over the container's local socket) | everything (cluster superuser) |
| `lina_app` | agents | read and write its agent's rows in `memory`, `ctx` and `vec`; also read shared `memory.messages` rows with `scope = 'agents'` |
| `honcho` | the Honcho server | own schema `ltm` and migrate it; SELECT and INSERT on `memory.messages`, REFERENCES on its `message_id`, and USAGE on schema `memory`; read and write its own vectors (kinds `message` and `document`) in `vec.embeddings`, nothing else in `vec` |

Every table in `memory`, `ctx`, `ltm` or `vec` that has an `agent_id` column gets row-level
security when it is created (or gains the column): an event trigger enables and forces it and adds
the policy `lina_agent_rows`. A session says which agent it acts for with
`SET lina.agent_id = '<agent>'`; it then sees and writes only that agent's rows, and with no agent
set it sees none. The superuser `lina` is not limited by the policy.

The `honcho` login is a trusted cross-agent server, not an agent principal. Migration
`0002_honcho_messages.sql` adds role-specific SELECT and INSERT policies on `memory.messages`:
it can read across agents and insert rows with a non-null `agent_id`, including from its
AUTOCOMMIT read sessions. It cannot UPDATE, DELETE or TRUNCATE shared text and has neither
table ownership nor BYPASSRLS. The `lina_app` policy is unchanged. Keep Honcho's database
credentials server-side; Honcho's API authorization still governs workspace access.

For collection, `0002_honcho_messages.sql` grants `lina_app` only EXECUTE on
`memory.honcho_holds_message(bigint)`, not SELECT on `ltm.message_meta`. The
SECURITY DEFINER function has a fixed `pg_catalog` search path and a qualified metadata
lookup; PUBLIC cannot execute it. A boolean is the smallest required access, and the function
can return false before the `ltm` schema or table exists, since store migrations run before
Honcho's migration job. It does not expose metadata columns or grant writes into `ltm`.

Migration 0005 adds a permissive **SELECT-only** policy on `memory.messages` for `scope = 'agents'`.
These owner-level external rows have NULL `agent_id`: every agent can read them, including a session with
no agent set, but the existing write policy still rejects inserting or changing NULL/another agent's rows.
The extra SELECT policy does not grant INSERT, UPDATE or DELETE access.
## Migrations

Honcho migrates `ltm` itself. Schemas `memory`, `ctx` and `vec` belong to LINA: each
`services/store/migrations/NNNN_<name>.sql` runs once, in its own transaction, and is recorded in
`memory.schema_migrations`. `lina store install` and `lina store upgrade` (also the upgrade rehearsal) run the
pending ones before Honcho's job (`hermes_cli/store_migrations.py`); running them again changes nothing. A new
migration is a new file with the next number; a file that has run is never edited.

## Messages (`memory.messages`)

Every message is one row in `memory.messages`, the one copy every engine reads. `hermes_state_store.py`
and Honcho's `src/message_store.py` write it. Hermes connections log in as `lina_app` and set `lina.agent_id` to the
profile, so row-level security keeps each profile to its rows. Agents find the store through
`LINA_STORE_DSN` when it is set (a secret: it carries the password), else through `store/store.env`
(`127.0.0.1`, `LINA_STORE_PORT`, user `lina_app`).

| Column | What it holds |
| --- | --- |
| `message_id` | identity, the row's place in write order |
| `uid` | `agent:<profile>:<message uid>` for agent turns; external sources use their own prefix (#18). A later text version of the same Hermes message (see below) is `<uid>#<16 hex of its digest>` |
| `source` | `agent` for agent turns (`messages` for external messages, #18) |
| `agent_id` | the profile; row-level security applies through it |
| `session_id`, `seq_in_session` | the Hermes session that first wrote the row and its row id there |
| `conversation_id` | channel: `messages:<platform>:<room_id>` for external messages; agent identity is filled by #20 |
| `author` | nullable author key: `<platform>:<author_id>` for external messages; NULL for agent rows (not a resolved person) |
| `role`, `content`, `tool_name`, `tool_call_id`, `ts` | the message; `content` is plain text (the text parts of multimodal content, NUL removed) |
| `tool_calls` | the tool calls as `jsonb` |
| `observed_at` | when the row was written |
| `scope` | `agent` (also `agents`, `person` for later sources) |
| `native` | Hermes-only fields, below |
| `content_sha256`, `forgotten_at` | digest of `content`; set when a message is forgotten |
| `tsv` | generated `to_tsvector('simple', content)`; with the `pg_trgm` index on `content` it serves search |

`native` holds every Hermes per-message field that is not a column: `reasoning`, `reasoning_content`,
`reasoning_details`, `codex_reasoning_items`, `codex_message_items`, `api_content`, the exact `tool_calls`
text (Hermes compares it byte for byte, and `jsonb` reorders keys), the exact content when it is not plain
text (multimodal parts, NUL or lone surrogates; base64 where JSON cannot carry it), `message_uid`,
`platform_message_id`, `finish_reason`, `token_count`, `effect_disposition`, `observed`, `_compressed_summary`,
`absorbed_message_uids`, `tool_call_uids`, `tool_call_uid`, and `payload_sha256` (the version digest).
None of them is filtered or sorted on in Postgres, so none became a column.

### Honcho messages

Honcho stores its per-message fields in `ltm.message_meta`. Its `message_id` is both the
primary key and a foreign key to `memory.messages(message_id) ON DELETE CASCADE`;
Honcho's integer `Message.id` is that same value. Public nanoids remain unique, and queue,
embedding and conclusion citation references resolve against the metadata row. Reads obtain
content from the shared table; a metadata flush inserts or resolves the immutable shared row
on the same connection and transaction. Honcho cannot rewrite that text.

For Honcho-origin messages, `uid` is `honcho:<workspace>:<public_id>`, `source` defaults to
`agent`, and `agent_id` defaults to the workspace name. Trusted internal ingestion may supply
`uid`, `source` and `agent_id` in `internal_metadata`; ordinary API metadata does not grant
access to another agent's identity. Existing UIDs must match both content digest and agent.
The one-to-one metadata primary key means a cloned Honcho placement gets a new shared row
and UID, rather than reusing the original placement's integer identity.

Revision `l13_message_split` migrates existing Honcho text after the store migrations. It
matches existing shared rows by UID or digest/session/time within the agent boundary,
preserves public IDs, and remaps integer references. Its parent `l19_vec_embeddings` has
already moved vectors to `vec.embeddings`; the split preserves their owner IDs. The upgrade's pre-split physical backup and
pinned Honcho image remain the restoration path described below.

Shared message IDs are identities, not chronological positions. Honcho history, token windows,
queue batches and clone cutoffs use `seq_in_session`. ID-valued API and summary boundaries
resolve to that message's session position before comparison. The split remaps both persisted
short and long summary boundary IDs in the same transaction; context starts after the summarized
message, and a late summary cannot overwrite a chronologically newer one.

Deleting shared text cascades to its Honcho metadata and dependent references. Deleting
Honcho metadata does not delete shared text. Direct `document_sources` edges for the deleted
public message ID are detached; document-to-document reasoning edges are not reinterpreted.

A row is never changed after it is written. Hermes writes one message several times: compaction keeps the
archived copy and inserts a live one, a compression child carries the tail of its parent, a prune keeps the
original beside a shortened copy. Copies with the same text share one row; a copy whose text differs gets its
own version row. Deleting a session deletes its `transcript_rows`, never the text: `lina store gc` (also run by the
state.db auto-maintenance) deletes agent-turn rows (`source = 'agent'`) no transcript row of the profile points at
and no Honcho metadata row holds once nobody wrote or re-used them for `sessions.message_text_grace_days` (30, at least 2), counting references by
`text_uid`, not by session, and leaving `forgotten_at` rows to the forget path. Only agent turns are collected by
transcript references: a state.db holds only the rows it wrote, so other sources (external messages, #18) keep their
own lifecycle (the forget path, a later collection of their own). `hermes backup` ZIPs and quick snapshots carry the rows their
`state.db` references as `memory-messages.jsonl` (uid, content, content_sha256, ...); restoring one puts missing
rows back (checked against content_sha256) before the restored `state.db` is used.

Both state.db `transcript_rows` (by `text_uid`) and Honcho `ltm.message_meta` (by `message_id`)
may hold a text row. The collection DELETE checks Honcho's hold again at deletion time,
including Honcho-origin rows whose workspace equals a profile and agent rows reused by Honcho.
Profile rename changes the agent and UID prefix in place, preserving `message_id` and Honcho's
reference; profile deletion removes local placements, not shared text. Backup restore inserts
missing text and refuses a conflicting UID payload rather than replacing an existing shared row.

### External messages

Configure `memory.external_messages.spool` in the LINA root's `config.yaml` to the collector's
`spool/events.jsonl`. The default is empty: ingestion does nothing and the CLI says so. This is a host job,
not a profile job; there is no source on/off switch here (#23).

```sh
lina memory external-messages ingest
lina memory external-messages ingest --spool /path/to/synthetic/events.jsonl
```

The existing gateway cron ticker checks every five minutes, once outside its profile loop. A host lock and
persisted size/mtime signature avoid duplicate work across gateway processes and unchanged spools. Collection
completion is observed through appended complete lines, not a collector hook; the collector itself is unchanged.
Neither this job nor its CLI reads the collector's derived SQLite database or writes in its directory.

The job opens the spool read-only under a shared `flock`, verifies SHA256 of the consumed byte prefix, and
parses only new newline-terminated events. An incomplete tail waits for the next invocation. A truncated or
rewritten consumed prefix stops ingestion with an error; do not reset the cursor to hide a replaced spool.
Prefix verification necessarily reads old bytes, but never re-parses old events. Each batch of 100 lines commits
message versions, current heads and its byte-offset/hash cursor in one owner transaction. A failed batch rolls
back together; earlier committed batches stay valid and a restart resumes after them. The transaction advisory
lock serializes writers even when separate configured paths contain the same global collector uid.

Rows use `source = 'messages'`, `uid = 'messages:<collector uid>'`, NULL `agent_id`, `scope = 'agents'`, and
`role = 'user'`. `content` is force-redacted by LINA before storage and `content_sha256` hashes that stored text.
Only the listed platform/account/room/native-id/author/reply/thread/edit/delete/ingest-path/attachment metadata
and `event_id` survive in `native` (plus the version digest); metadata strings are redacted too. `raw`,
observations and the collector's `sync_checkpoint` are not persisted. Common fields are author → `author`,
channel → `conversation_id`, scope → `scope`, ref → `uid`, forget → `forgotten_at`.

An existing message never changes. A different sanitized payload, edit/delete mark or metadata writes
`messages:<collector uid>#<first 16 hex of SHA256>`; the first payload keeps the unsuffixed uid. The digest
excludes `event_id`, so identical re-collection writes no message row. `memory.external_message_heads` is the
mutable owner-only projection of which version is current: later spool order wins, except an `archive` head
beats a later `live` observation. Distinct losing versions remain available by their own refs. An identical old
version can become current again without reinserting or changing it. A delete mark is another version, not a
physical delete or a forget operation. Cursor/head tables are not accessible to `lina_app`; a future reader
that needs current-only semantics must use this ordering rule rather than assume the greatest message_id wins.
Transcript garbage collection considers only `source = 'agent'`, never these owner-level rows.

### What stays in each profile's `state.db`

`state.db` keeps operational state only: sessions, locks, gateway routing, workers, usage, and
`transcript_rows`, which says where each message sits and holds no text: session, row id, role, tool call
id and name, timestamp, active/compacted/rewound, display order and identity, display metadata (attachments,
reactions, delivery markers), the message and tool-call uids, and `text_uid` pointing at the row in
`memory.messages`. The flags Hermes filters on in its transcript queries (`active`, `compacted`,
`display_order`, `display_identity`, `role`, `message_uid`, `platform_message_id`) are columns there, not in
`memory.messages`, because they belong to one copy, not to the message. `content_digest` and
`tool_call_count` let those queries test content equality and count tool calls without fetching text.
The SQLite full-text indexes are gone. A `state.db` from before (schema version below 33) keeps its old
`messages` table untouched; moving that data in is #24.

A state.db write that adds text writes it to Postgres first, in a transaction that commits just before
SQLite's; a rollback or retry of the SQLite write rolls it back too. A crash between the two commits
leaves a text row nothing points at, never a placement row without text.

### Search

`session_search` and `SessionDB.search_messages` ask Postgres for the matching rows, then keep the visible
rows of the wanted sessions, roles and sources in `state.db`. A word matches a word of the `simple`
tsvector (`deploy*` any word starting with it); Korean, Chinese and Japanese terms match as substrings
through `pg_trgm`, so a two-character term such as `회의` finds `회의록을`, as the old `cjk_unicode61` bigram
index did. A word query that finds nothing is retried as substrings, then with any of its terms.

### Tests

The tests that touch messages run on a real Postgres with pgvector. `scripts/run_tests.sh` starts one for the
run (system `initdb`/`pg_ctl` when their Postgres has the `vector` extension, else
`docker run pgvector/pgvector:0.8.7-pg15-bookworm`) unless `LINA_TEST_PG_DSN` names one (a superuser DSN of a
throwaway cluster). The cluster gets `services/store/initdb` and the migrations once, each test process its own
database copied from that template, and every test empty tables. Without any Postgres the store-backed tests
skip with one reason; under `CI=true` they fail. The macOS CI job has no Docker: it installs Homebrew `postgresql@17` and `pgvector`
and puts their `bin` on PATH, so the fixture's `initdb` path runs there (the store SQL uses nothing newer than
Postgres 15, which the store image runs).

## Embeddings (`vec`)

Every source is embedded with one model at one width, so one query vector ranks agent turns, external messages
and Honcho's memory together: `qwen3-embedding-8b` at **1024 dimensions**, the first 1024 values of the model's
vector L2-normalised again (Matryoshka). pgvector indexes `vector` only up to 2,000 dimensions, so 1024 also keeps
one HNSW index. `services/store/migrations/0003_vec_embeddings.sql` creates the tables:

| Table | What it holds |
| --- | --- |
| `vec.embeddings` | one vector per item and model: `kind` (`message`, `chunk`, `summary`, `document`), `item_id`, `agent_id`, `model`, `dim`, `embedding vector(1024)`; primary key `(kind, item_id, model)`; HNSW cosine index |
| `vec.chunks` | the embedded text of `memory.messages`: `chunk_id`, `source`, `agent_id`, `message_ids bigint[]`, `text` (one message per chunk today) |
| `vec.reembed_progress` | where `lina store reembed` stopped, per source and model |

`kind` says what `item_id` points at: `chunk` → `vec.chunks.chunk_id`, `message` → Honcho's
`ltm.message_embeddings.id` (a chunk of a Honcho message), `document` → `ltm.documents.vec_id` (Honcho's
conclusions; their own id is text), `summary` → context summaries (#16). Row-level security applies on
`agent_id` as everywhere. Honcho's rows carry the Honcho workspace as `agent_id`; the `honcho` role never sets
`lina.agent_id`, so the migration grants it `USAGE` on `vec`, `SELECT, INSERT, UPDATE, DELETE` on
`vec.embeddings` and the policy `honcho_rows`, which lets it see and write kinds `message` and `document` only.

Honcho has no vector columns of its own any more: its revision `l19_vec_embeddings` copies the old vectors only
when `EMBEDDING_LEGACY_MODEL` in `honcho.env` names the model that wrote them and that is the configured model
(`EMBEDDING_MODEL_CONFIG__MODEL`), and then only those as wide as `vec.embeddings`. Honcho's old columns keep no
per-row model, so a vector's width does not say which model wrote it; without that statement nothing is copied.
It logs how many it left (Honcho's message chunks go back
to pending, so its reconciler embeds them again; conclusions wait for `lina store reembed --source memory`) and
drops `documents.embedding` and `message_embeddings.embedding`. At start Honcho refuses to run when
`EMBEDDING_VECTOR_DIMENSIONS` differs from the width of `vec.embeddings`.

### Configuration

LINA's side reads `embedding:` in `config.yaml`; the key is a secret in `.env` (default name
`LINA_EMBEDDING_API_KEY`, another one through `api_key_env`):

```yaml
embedding:
  base_url: <OpenAI-compatible endpoint ending in /v1>
  model: qwen3-embedding-8b
  dimensions: 1024
  # api_key_env: LINA_EMBEDDING_API_KEY   batch_size: 64   timeout: 60   max_chars: 8000
```

`agent/embedding_client.py` is the one client (`POST {base_url}/embeddings`, `dimensions` sent, a full-width
answer cut and renormalised). `truncate_stored` there cuts a vector stored at full width (4096 or 1536) by the
same model to 1024 and refuses one from another model. Honcho is pointed at the same model and width in
`store/honcho.env`. `lina store install` and `lina store upgrade` write the model and width there for you
(`HONCHO_DEFAULTS` in `hermes_cli/store.py`), each key only when the file does not set it yet:

```sh
EMBEDDING_MODEL_CONFIG__TRANSPORT=openai
EMBEDDING_MODEL_CONFIG__MODEL=qwen3-embedding-8b
EMBEDDING_VECTOR_DIMENSIONS=1024
EMBEDDING_MODEL_CONFIG__DIMENSIONS_MODE=always
```

What you add is the endpoint and its key:

```sh
EMBEDDING_MODEL_CONFIG__OVERRIDES__BASE_URL=<the same endpoint>
EMBEDDING_MODEL_CONFIG__OVERRIDES__API_KEY_ENV=EMBEDDING_CUSTOM_API_KEY
EMBEDDING_CUSTOM_API_KEY=<the key>
# Only for the upgrade that runs l19_vec_embeddings, and only when the vectors already in Honcho were written by
# qwen3-embedding-8b at 1024 dimensions; otherwise leave it out and they are re-embedded.
# EMBEDDING_LEGACY_MODEL=qwen3-embedding-8b
```

A key you set in `honcho.env` wins: edit the written line, and install or upgrade will not change it back.
`EMBEDDING_MODEL_CONFIG__TRANSPORT`, `EMBEDDING_MODEL_CONFIG__MODEL` and
`EMBEDDING_MODEL_CONFIG__DIMENSIONS_MODE` may be overridden (the model together with `embedding.model` in
`config.yaml`); `EMBEDDING_VECTOR_DIMENSIONS` must stay the width of `vec.embeddings` (1024), or Honcho does
not start.

The model name must be the same string on both sides: it is part of every row's key, and searches only read
rows of the configured model.

### Re-embedding and the quality check

```sh
lina store reembed --source agent|messages|memory|all   # default all
lina memory eval-search --questions <file.jsonl> [--honcho-url http://127.0.0.1:8000] [--json out.json]
```

`reembed` runs as the store owner, goes through `memory.messages` (`agent`: roles user and assistant;
`messages`: external messages) and Honcho's conclusions in id order, a batch per transaction (`--batch-size`,
default `embedding.batch_size`), and records the last id of each batch, so a stopped run continues where it
stopped (`--from-start` starts over). It prints a line per batch and the counts per source. Renaming a profile moves its
`vec.chunks` rows and their `chunk` vectors to the new agent with its messages, in the same transaction (and back
when the rename fails); `hermes profile migrate-identity` moves any a rename left behind.

`eval-search` scores the top-5 hit rate per source: today's search of each source (the store's full-text search
for `agent` and `messages`, Honcho's conclusion search for `memory`) against one vector search over all of the
agent's rows in `vec.embeddings`. A line of the questions file is
`{"id", "source", "agent", "question", "expected": [refs], "honcho": {"workspace", "observer", "observed"}}`;
a ref is a `memory.messages` uid or a Honcho conclusion id. A synthetic set (36 questions over a 48-item corpus)
is in `tests/fixtures/memory_eval/`.

## Backup and restore

```sh
lina store backup                 # one generation of the whole cluster
lina store backups                # list them
lina store restore [GENERATION]   # default: the newest
```

A generation is `store/backups/<UTC time>/`: `base.tar.gz` (`pg_basebackup` of the whole cluster,
WAL included) and `manifest.json` (its sha256, the Postgres version, every table's row count and
row digest, and the id of the Honcho image `lina-honcho:local` pointed at). The backup also tags
that image `lina-honcho:id-<first 12 hex of the id>`: Docker drops an image whose last tag moves,
and an upgrade moves `lina-honcho:local`. `docker image rm lina-honcho:id-...` frees one once no
backup you keep needs it. The Honcho api and deriver are stopped while it is taken and started again
afterwards. It is written as `.partial-<time>` and renamed when complete, so an interrupted backup
never appears as a generation; the next backup deletes the leftover.

Restore refuses a generation whose archive does not match its sha256, naming the cause. Otherwise
it unpacks the archive into a new volume, switches `store.env` to it, sets the role passwords to the
ones in `store.env` (a cluster from another machine carries that machine's), and compares every
table with the manifest. If anything differs, the store goes back to its previous volume. Before
the api and deriver start, `lina-honcho:local` is tagged back to the image recorded in the manifest,
so Honcho's code matches the restored schema; if that image is not on this machine (a restore on
another machine), the current one is used and `restore` says so. The volume the store used before
is never deleted; `restore` prints its name.

To move the store to another machine, run `lina store install` there, copy a generation into its
`store/backups/`, and run `lina store restore`.

## Upgrading Honcho

After taking a new Honcho release into `services/honcho`:

```sh
lina store upgrade   # or: lina store install
```

1. **Backup**: a new generation.
2. **Rehearsal migration**: the new image is built (the old one stays as `lina-honcho:previous`),
   the backup is restored into a separate compose project (`lina-store-rehearsal`, its own volume
   and ports), the migration job and the smoke test run there, and the rehearsal is removed. If it
   fails, the store is not touched and the old image is tagged back.
3. **One job**: with the api and deriver stopped, the migration job runs once on the store.
4. **Smoke test**: the api and deriver start on the new image and the smoke test runs.

If step 3 or 4 fails, the message names the backup from step 1; `lina store restore <generation>`
puts the store back: the data, and the Honcho image from before the upgrade.
