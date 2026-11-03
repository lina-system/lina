# The memory store

All memory lives in one Postgres with pgvector, run in Docker and set up by LINA. The Honcho server
(api and deriver), built from `services/honcho`, runs beside it and keeps its tables in schema `ltm`.

| Schema | Holds |
| --- | --- |
| `memory` | messages, the identity registry (`identities`, `identity_keys`) |
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
`initdb/10-lina-store.sh`, which runs once on an empty cluster. One store serves every profile, so
its settings are under the LINA root, not a profile home, in `store/`:

| File | What it is |
| --- | --- |
| `store.env` | Generated on first install (mode 0600): the three role passwords, the ports (Postgres `54320`, Honcho `8000`, both on 127.0.0.1) and the data volume's name |
| `honcho.env` | Honcho settings such as LLM and embedding keys (`LLM_*`, `EMBEDDING_*`); read by the api, deriver and migration job |
| `backups/` | Backup generations |

## Roles and row-level security

| Role | Used by | Can |
| --- | --- | --- |
| `lina` | `lina store` (over the container's local socket) | everything (cluster superuser) |
| `lina_app` | agents | read and write the tables in `memory`, `ctx` and `vec`; only the rows of its agent |
| `honcho` | the Honcho server | own schema `ltm` and migrate it |

Every table in `memory`, `ctx`, `ltm` or `vec` that has an `agent_id` column gets row-level
security when it is created (or gains the column): an event trigger enables and forces it and adds
the policy `lina_agent_rows`. A session says which agent it acts for with
`SET lina.agent_id = '<agent>'`; it then sees and writes only that agent's rows, and with no agent
set it sees none. The superuser `lina` is not limited by the policy.

## Migrations

Honcho migrates `ltm` itself. Schemas `memory`, `ctx` and `vec` belong to LINA: each
`services/store/migrations/NNNN_<name>.sql` runs once, in its own transaction, and is recorded in
`memory.schema_migrations`. `lina store install` and `lina store upgrade` (also the upgrade rehearsal) run the
pending ones before Honcho's job (`hermes_cli/store_migrations.py`); running them again changes nothing. A new
migration is a new file with the next number; a file that has run is never edited.

## Messages (`memory.messages`)

Every message is one row in `memory.messages`, the one copy every engine reads. `hermes_state_store.py` is the
only code that runs SQL against it. Its connections log in as `lina_app` and set `lina.agent_id` to the
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
| `conversation_id` | the conversation in the identity registry (#20, [memory-layer.md](memory-layer.md)); null until `lina-memory` resolves the turn |
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

A row is never changed after it is written, except `conversation_id`, filled once while it is null (found by base
uid through the partial index `messages_unfilled_base`, which holds only rows still waiting for it). Hermes writes one message several times: compaction keeps the
archived copy and inserts a live one, a compression child carries the tail of its parent, a prune keeps the
original beside a shortened copy. Copies with the same text share one row; a copy whose text differs gets its
own version row. Deleting a session deletes its `transcript_rows`, never the text: `lina store gc` (also run by the
state.db auto-maintenance) deletes rows no transcript row of the profile points at once nobody wrote or re-used
them for `sessions.message_text_grace_days` (30, at least 2), counting references by `text_uid`, not by session,
and leaving `forgotten_at` rows to the forget path. `hermes backup` ZIPs and quick snapshots carry the rows their
`state.db` references as `memory-messages.jsonl` (uid, content, content_sha256, ...); restoring one puts missing
rows back (checked against content_sha256) before the restored `state.db` is used.

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
