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
| `lina_app` | agents | read and write its agent's rows in `memory`, `ctx` and `vec`; also read shared `memory.messages` rows with `scope = 'agents'` and their `vec` chunks (owner `*`) |
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

Migration 0009 does the same for their vectors. `lina store reembed --source messages` writes the `vec.chunks` row
and `chunk` vector of such a row with the shared owner `*` as `agent_id` (`hermes_state_store.SHARED_VECTOR_OWNER`),
which is not a profile name, so no agent's `lina_agent_rows` matches it for a write. The SELECT-only policies
`lina_shared_chunks` (`vec.chunks`) and `lina_shared_chunk_vectors` (`vec.embeddings`, kind `chunk`) let every
agent read rows with that owner, so an agent's vector search finds the shared external messages beside its own rows.
The migration also gives that owner to such vectors written before it with the owner `'None'`.
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
| `conversation_id` | the conversation: from the identity registry for agent turns (#20, [memory-layer.md](memory-layer.md)), null until `lina-memory` resolves the turn; `messages:<platform>:<room_id>` for external messages |
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

A row is never changed after it is written, except `conversation_id`, filled once while it is null (found by base
uid through the partial index `messages_unfilled_base`, which holds only rows still waiting for it). Hermes writes one message several times: compaction keeps the
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
Neither this job nor its CLI reads the collector's derived SQLite database or writes in its directory; only
`lina store reembed --from-index` reads that index, read-only ("Importing the collector's vectors" below).

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

### Copying legacy memory into a new agent

```sh
LINA_HOME=/path/to/new-lina HERMES_HOME=/path/to/new-lina \
  lina migrate memory --from /path/to/old-home --source-profile alpha --as alpha-copy \
  --work-dir /path/to/migration-copy --honcho-dump /path/to/honcho.dump \
  --spool /path/to/spool --work-db /path/to/work.db
```

`--source-profile` selects the old profile; global `--profile` still selects the profile running LINA.
The destination is the running LINA root's `profiles/<new name>`, with an independent agent identity.
`--home` defaults to that root; an explicitly different root is refused before copying or writing. Run the
command from the new agent's destination installation, whose store must already be installed and reachable.
Global `--profile` may select a runtime profile within that root; it does not change the source selection.
The command uses SQLite's online backup API, never opens an original
database for writing, and reads only retained snapshots after the copy step. It copies skills, persona
and memory files; credentials are excluded and config is limited to non-secret memory settings.
A symlink inside `skills/`, `memories/`, `memory/` or `personas/` (such as a shared skill linked into a
named profile) is skipped, not followed: its target is neither read nor checked, nothing behind it is
copied, and the report lists it under `skipped_symlinks` as a path relative to the profile. A symlinked
profile directory, `state.db`, `lcm.db`, one of those four directories itself or a named file such as
`SOUL.md` still refuses the profile.
Each database snapshot is consistent on its own, not a common snapshot instant across databases.

The copied Honcho input is a pre-split logical `pg_dump` archive, not a physical store backup. The selected
profile's `honcho.json` supplies its host (`hermes` for default, `hermes_<profile>` for a named profile):
workspace, AI peer, person peer and observation mode. A shared workspace is not imported wholesale. The
migration selects the profile's recall-visible observer/target collections and sessions containing its AI
peer, maps that AI peer to the new agent, and reports excluded other-agent and other-workspace rows.
References from selected rows also retain their necessary session, peer, collection, document or message
ancestors, listed as `dependencies`. This follows references outward; it does not import the ancestor's
unrelated session participants, messages or private recall siblings.
Source transport URLs and keys are not published into the new home; its Honcho config points to the
destination store's local Honcho port.

The new agent's config also carries who its owner is and where its tools run. The source host's `userPeerAliases`
and `pinUserPeer` are copied, and each `--owner-id ID` (repeatable, needs `--honcho-dump`) adds the owner's runtime
id on a platform the agent is reached by (a messenger user id, `uid-<n>` for the CLI, `api` for the API server) as
an alias to `peerName`. Honcho imports the owner's `MEMORY.md`/`USER.md`/`SOUL.md` only into a session whose peer is
`peerName`, so without an alias every messenger session logs `is not the declared owner` and skips them, and any
other id still skips. An `--owner-id` the source aliases to another peer is refused. The config sets
`terminal.cwd` to the agent's own profile directory, `terminal.home_mode: profile` with `<profile>/home` created, and
`terminal.auto_source_bashrc: false`, so the copy's terminal does not start in the host user's home or source that
user's shell profile (lina#706). Only the new agent's files are written; the source profile is never changed. A
multiplexed gateway applies `terminal.cwd` per profile but not yet `home_mode`, which takes effect when the agent's
own gateway runs it.

Several source profiles may share one Honcho workspace. Each destination agent receives deterministic
21-character text IDs derived from its name and the source identity; scoped session and ordinary peer names
are preserved. Integer IDs use each destination table's own identity sequence, allocated in source timestamp
order with source ID as the tie-break. The receipt retains these allocated IDs for exact replay, alongside
original IDs and explicit source UIDs. Migrated history lands with IDs after existing destination rows;
the intended first use is a fresh agent store (lina#41), since ID order is not cross-import timestamp order.
Pre-existing rows are never rekeyed. Foreign keys,
citations and typed JSON identity fields use those maps, so importing a second profile cannot link it to the
first agent's rows. Existing matching destination-agent text may still be reused after content verification.

The archive is restored into a disposable database and upgraded with Honcho's actual Alembic chain in
the installed store's Honcho image. No separate Python environment or `--honcho-python` is needed;
that option is only an explicit interpreter override. The API and deriver are never started. Existing
unrelated destination workspaces remain intact. Legacy vectors, including 1536-wide ones, are explicitly
left for `lina store reembed`; migration never calls an embedding endpoint.

Search over the migrated memory is therefore partial until `lina store reembed` finishes: Honcho's searches and
the vector leg of `memory_search` only reach rows that have a vector of the configured model. While the agent's
Honcho workspace has conclusions without one, `honcho_search` (its `partial` field) and the recall prefetch say
`N conclusions are not embedded yet; search is partial ...` instead of a bare empty answer, and `lina doctor` lists
the pending conclusions per Honcho workspace under Memory Vectors. After the migration, in this order:

```sh
# 1. the embedding: section in the new home's config.yaml ("Configuration" below), and its key in .env
lina store reembed --source memory   # 2. the copied Honcho conclusions (and LCM summaries)
lina store reembed --source agent    # 3. the copied agent turns
```

The copied `lcm.db` is imported through LCM-X's existing `PgConnection` route after agent messages and before
Honcho. Host text is matched by session, role, observed time (or legacy timestamp), content digest and
`MessageStore.text_uids_with_digest`; unmatched text uses the same shared writer. The copied work ledger imports the selected profile's
records and relations, renaming its owner to the new agent. Required area/project/goal/run/to-do ancestors
and referenced parent events retain their original owners and are listed separately. This includes a
to-do's closing run and both endpoints of its run relations. The destination work store keeps one migration
profile-to-agent map: an all-profile run registers every name before importing; a single run registers only
its arbitrary `--as` name and refuses a conflicting remap. Mapped owners apply equally to selected rows
and ancestors; unmapped owners remain unchanged. A later owner import promotes only previously receipted
foreign ancestors whose source and stored content still match, in the same work transaction. Other existing
rows are never promoted. Profiles commit separately; an interrupted batch resumes by matching completed
profiles on an identical retry. Counts and manifests compare only the selected subset, not unrelated rows.

The command prints JSON and writes the same result to `--work-dir/migration-report.json`. It includes copy
receipts, per-source accounting and bidirectional manifests, actual duplicate queries, and reference checks.
Agent manifests hash the full immutable text payload, including tool/API fields, not just visible content.
Large agent imports keep hash-only manifests in JSONL sidecars beside the destination state database and
include their paths, counts and file SHA256 values in the report; text is read and verified in bounded batches.
Raw native identities can legitimately have several immutable versions; the report discloses them separately
from duplicate version identities. `ok` means the included migration checks passed; `complete` requires every
step to be counted and ok, including explicit absence and deferred-vector accounting. It does not mean an
embedding endpoint was called. An accounting mismatch exits nonzero.

Repeating the same command verifies and reuses the same snapshots rather than capturing new source data;
it inserts nothing and fails if copied destination messages were lost or changed. Use a new work directory
to capture new source data. External ingestion keeps its cursor in the destination store under the original
spool path, so subsequent live ingestion continues from the copied prefix. If that cursor has advanced,
migration verifies the copy against the original spool's committed prefix and checks every copied message
version without moving the cursor or changing live heads. Later versions are not part of the copied subset.
A changed prefix is rejected; an unavailable original spool cannot prove an advanced cursor's byte prefix.
Honcho reruns verify the imported durable subset rather than demanding that the whole live workspace
remain frozen. Queue consumption and retries are accounted for from the original dump receipt, with
current queue counts reported separately; reruns never recreate consumed jobs. New live rows are outside
the copied subset, while missing imported identities or changed durable content still fail verification.

After checking one profile, `--all-source-profiles --as copied` processes profiles sequentially as
`copied-default`, `copied-alpha`, and so on, retaining each profile's report under the work directory.

### Copying legacy cron jobs

```sh
lina migrate cron --from /path/to/old-home --source-profile alpha --as alpha-copy --work-dir /path/to/migration-copy
```

Run it after `migrate memory`, into the same agent. It reads the old profile's `cron/jobs.json` once into
`--work-dir/cron-snapshot/` (sha256 receipt, symlinks refused; the original is never written) and imports
the copy into the agent's job store. Every job keeps its old id and lands paused (`paused_reason: migrated
copy`) whatever its old state. `deliver` becomes `local`; the old `deliver`, `failure_deliver`, `origin` and
`interpreter` are not carried and are kept as text in the job's `notes`, so a copied chat id is never live.
A rerun finds the ids already present and changes nothing, including edits made to the copy since. The JSON
report (also `--work-dir/cron-migration-report.json`) lists per job its kind, schedule, old `deliver`,
whether its script and workdir resolve in the new home, and what was left out. Scripts travel with the
jobs: each script a job names (`script`, `monitor_script`) is read from the old profile's `scripts/` into
the snapshot (sha256 receipt, symlinks refused) and copied into the agent's `scripts/`, never run; the
report says per job whether it was `copied`, `already_present`, `missing` in the old profile, `refused`,
or `kept_existing` (a different file is already there and is not overwritten). Credentials do not
travel: a script's keys and environment are set up in the new agent by hand. Check each job with
`lina -p alpha-copy cron run <id> --dry-run` before resuming it.

### Migration identity and reference inventory

This inventory follows `services/honcho/src/models.py`, its typed payload consumers, and all 33 Alembic
revisions from `a1b2c3d4e5f6` through `l13_message_split` (whose parent is `l19_vec_embeddings`, not l12).
The identity-changing steps are `d429de0e5338` (peer paradigm/text PKs), `917195d9b5e9` (embedding IDs),
`ec8f94139b02` (queue message/workspace columns), `f1a2b3c4d5e6` (legacy source arrays),
`a7c3e9f1b2d4` (DDL-only normalized document edges), `c4d8e2f6a1b3` (citations), l19 (vector owner IDs)
and l13 (shared text IDs). Other revisions preserve these identities while changing indexes, scoped names,
configuration, counters, tombstones or constraints. The archive is upgraded through that ancestry before
selection; counts describe the upgraded retained graph, not obsolete pre-upgrade tables.

| Id-bearing columns | Destination identity | References and preserved distinctions |
| --- | --- | --- |
| `workspaces.id`, `peers.id`, `sessions.id`, `collections.id` | Deterministic 21-character agent/source IDs | Workspace FKs use `name`, not workspace ID. Queue `session_id` uses the session PK; other session fields use scoped names. Documents refer to collection observer/observed/workspace names, not collection ID. |
| `documents.id` | Deterministic text ID | `document_sources.derived_id`, citation `conclusion_id`, `copied_from`, legacy source arrays and observation-deletion resource/key. |
| `message_meta.public_id` | Deterministic text ID | Embedding `message_id`, citation `message_id`, polymorphic source edges/arrays, summary `message_public_id`, queue `payload.message_public_id`. These are not shared bigint IDs. |
| `memory.messages.message_id`, `message_meta.message_id` | Validated existing text reuse or destination `memory.messages` identity allocation | Queue `message_id`, legacy queue payload integer, both summary integer boundaries, nested document metadata `message_ids`. Receipt retains original pre-l13 integer/public ID and staging-to-destination map. |
| `documents.vec_id` | Destination documents identity sequence; receipt map | `vec.embeddings(kind='document',item_id)` after reembedding; old vectors are deferred. |
| `message_embeddings.id` | Destination embedding identity sequence; receipt map | `vec.embeddings(kind='message',item_id)` for Honcho chunks; its own `message_id` is a text public ID. |
| `queue.id` | Destination queue identity sequence; receipt map | Worker job identity, not message identity. Consumed jobs are not recreated on replay. |
| `session_peers` composite PK | Workspace and selected-AI name mapping | Preserve session name and ordinary peer name; composite name FKs remain within the destination workspace. |
| `document_sources` composite PK | Mapped document/public endpoints | Source may be a document or message public ID. An edge whose source is neither any more (the conclusion outlived its message) is dropped and counted as `source_gone` in `counts.document_sources.excluded` and `excluded`; the conclusion is kept. Keep edge position. |
| `conclusion_source_messages` composite PK | Mapped document and public-message endpoints | Both endpoints are text identities; preserve citation position. |
| `active_queue_sessions.id`, `webhook_endpoints.id` | Excluded from selected imports | Worker leases and webhook endpoints are not migrated memory; zero retained counts do not mean their source tables were empty. |
| Shared text `uid`, registry identities | Destination-qualified Honcho UID or verified existing UID; registry alias resolution | Top-level message metadata UID/agent bindings follow chosen text. Registry IDs are not Honcho nanoids. |

Typed JSON references include both short/long summary boundaries; nested integer `message_ids`; all three
legacy document arrays (`source_ids` column, metadata `source_ids`, metadata `premise_ids`); and document-only
`copied_from`. Valid legacy IDs enter dependency closure before remapping, even in lower-precedence arrays.
Malformed elements, prose `premises`/`sources`, arbitrary nested metadata and URLs are not rewritten.
The reconciler has not necessarily drained legacy arrays: a7c3 is DDL-only.
Cycles (including self-loops) across normalized and retained legacy document references are refused before destination publication; shared-ancestor DAGs remain valid.

Queue payloads distinguish integer message IDs, public message IDs, scoped peer/session names, and deletion
resources: observation deletion uses a document ID (including an already-deleted target); session deletion
uses a session **name**. Encoded work-unit keys remap their workspace/peer slots and observation resource,
not session names, dream types or global reconciler keys. Known webhook payload workspace/peer fields are
typed names; arbitrary event data is opaque. Peer-card metadata changes the selected AI's `<peer>_peer_card`
key, not the card text. Session sequence numbers, edge positions and runtime counters are not identities.

The Honcho JSON report includes `reference_inventory`: class name to occurrence count, including zero-count
classes, for the retained source graph after closure. Counts include identity columns, relational references,
typed JSON references and scoped-name fields inspected/preserved by the remapping contract. Each repeated
array member counts separately; a work-unit key counts once, not once per encoded slot. Generated IDs count
their owner rows; shared-message references exclude separately reported text-row allocations. Legacy raw
array counts are separate from normalized edge counts. These counts are not row totals or vector totals.
The regression runs the real remappers and requires every changed identity field to be covered by the
inventory, so a newly remapped field cannot silently disappear from reporting.

Per-source policies:

- **Agent transcripts:** local placement IDs stay in each destination profile's SQLite store. Shared text
  uses agent-qualified UID/version identity; copied immutable payloads are verified without rejecting later live rows.
- **External messages:** platform/room/native identity plus immutable payload version is globally shared;
  do not rename or duplicate it per profile. Retain the original spool cursor and verify copied versions.
- **Work:** preserve native work IDs. Store-owned profile mappings apply to selected rows and ancestors;
  only receipted unchanged foreign ancestors can later be promoted. Other content collisions fail.
- **Honcho:** each destination agent owns a separate deterministic text-ID graph and receipt-backed positive
  integer allocations. Shared source people/sessions do not cause a global rename. Person registry aliases
  are agent-qualified; equal display names alone do not merge people. Preserve verified conversation aliases.
- **Files and LCM:** file payloads and arbitrary links are not identity-remapped. LCM native IDs are scoped by
  destination agent and remain intact; global surrogate/vector IDs are allocated by the destination and recorded
  in its import receipt. Legacy vectors require compatible model/1024-wide float32 provenance or reembedding.

### Native LCM copies

The source `lcm.db` is opened read-only, without running schema migrations on it. The destination path is routed
with `lcm_x_store.route(path, agent)`; SQLite-shaped inserts pass through its translator and message interceptor,
not a second PostgreSQL text writer. High-level `append` cannot copy history losslessly because it applies ingest
protection and replaces ingestion timestamps. The routed SQL preserves historical placement columns.

The import retains messages, summary nodes and provenance, lifecycle/migration metadata, assertion sources and
relations, prior importer receipts, rollups and their sources/state/invalidations, entities/aliases/keys/notes/events,
host-file revision records, Teams policy/audit records, message relations/host UID bindings, and trajectory corpus,
source/state/asset/transition records and profile metadata. Per-agent native IDs remain unchanged; global rowids
are omitted on insert and mapped in `ctx.metadata`'s `lina_memory_import:v1` receipt. Registry `lcm_conversation`
aliases resolve to the same conversation identity as host transcript sessions.

Query corpus generations/views are caches rebuilt from the imported corpus. FTS tables and their shadow tables
are rebuilt by store indexes. Binary vector accelerators and trajectory vector indexes are rebuilt from retained
text; source backfill leases are not resumed. Unknown populated tables are refused rather than silently dropped.
Every source table receives accounting, including skipped cache rows and their reasons.

LCM-X records the uids it mints for its own rows in `host_uid_bindings` as `kind='engine'`, `store_id=0`. Such a
row names no message and is copied as it is (`engine_uid_bindings` in the report); any other `store_id` that is
no source message still fails.

The SQLite-only fork quarantine (`lcm_fork_quarantined_messages`, written by the fork's repair scripts) is left
out, not copied: the destination has no such table, and the rows it records are raw copies of LCM's own summary
heads or of earlier rows, so their text is held elsewhere and only `restore()` would be lost. An unrestored
quarantined message in its `::fork-quarantine` session that nothing cites (summary nodes, message relations,
assertion sources/assertions/relations, importer receipts, memory note `cites`/`retracts_store_ids`, entity
alias/key `source_store_ids`, and those arrays and override `scope_store_ids` in memory event payloads) is not
copied, nor are its chunk metadata, chunk vectors and host UID bindings;
each table counts them as `skipped` with reason `fork_quarantine_uncited`. A quarantined message something cites
is copied as an ordinary message in its `::fork-quarantine` session. The quarantine records are counted as skipped
(`fork_quarantine_records_left_out`), and `fork_quarantine` in the report gives the record, left-out and kept-cited
message counts.

An LCM tool row whose `tool_name` is NULL while the matched host text has one is copied with the host's name
(`tool_name_from_host` in the report): the host kept a name LCM lost. Two different non-NULL names still fail.

Compatible summary/chunk vectors use the existing 0008 views; other model/width/dtype vectors are counted as
deferred. `lina store reembed --source memory` (also `all`) includes LCM summaries, with separate per-agent progress
and global vector keys. Migration itself never calls an embedding endpoint. The new profile selects LCM-X with
`lcm.store_backend: postgres`; it does not receive an active local `lcm.db` copy.

Reruns compare only receipted copied rows and their native-ID/hash manifests. Newly appended live rows are outside
that subset; missing or modified copied durable rows fail. Text commits before placements through the existing
host writer: a failed placement transaction can leave unreferenced text, but deterministic native UIDs reuse it
on retry and no dangling placement is published. `held_by_context` keeps migrated placements' text alive during GC.

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

A vector search filters its rows (owner, kind, model, the joined chunk or conclusion still there) after the
HNSW index has picked candidates. With pgvector's defaults (`hnsw.ef_search` 40, `hnsw.iterative_scan` off) the
index returns the 40 nearest rows of the whole table, the filter drops most of them, and the search returns
fewer rows than asked for, or none, and misses rows an exact scan finds (#387: on the #148 store of 436k vectors,
recall@5 against an exact scan was 0.875). `MessageStore.search_memory_vectors` and `eval-search` therefore run
their query with `HNSW_FILTERED_SEARCH` (`hermes_state_store.py`): `hnsw.iterative_scan = strict_order`, which
keeps reading the index until the filter lets the limit through, nearest first, and `hnsw.ef_search = 400`. On
that store this gave recall@5 0.975 at about 20 ms a query, against about 1.6 s for an exact scan. Both are
`SET LOCAL`, scoped to the query's own transaction (a savepoint inside a write).

Honcho has no vector columns of its own any more: its revision `l19_vec_embeddings` copies the old vectors only
when `EMBEDDING_LEGACY_MODEL` in `honcho.env` names the model that wrote them and that is the configured model
(`EMBEDDING_MODEL_CONFIG__MODEL`), and then only those as wide as `vec.embeddings`. Honcho's old columns keep no
per-row model, so a vector's width does not say which model wrote it; without that statement nothing is copied.
It logs how many it left (Honcho's message chunks go back
to pending, so its reconciler embeds them again; conclusions wait for `lina store reembed --source memory`) and
drops `documents.embedding` and `message_embeddings.embedding`. At start Honcho refuses to run when
`EMBEDDING_VECTOR_DIMENSIONS` differs from the width of `vec.embeddings`.

### Configuration

The store's embedding model is a setting of the home, not of a profile: `embedding.model` in the root
`config.yaml` (the home's, beside `profiles/`) is the model every agent embeds its queries with and `lina store
reembed` writes, whichever profile runs them, and `lina doctor` counts pending vectors of it. A profile may not
differ: its own `embedding.model` is not read, and `lina doctor` names each profile that sets another one.
`agent/embedding_client.py::home_model` is the one place the name is read. The other `embedding:` keys (endpoint,
key, width) stay per `config.yaml`.

LINA's side reads `embedding:` in `config.yaml`; the key is a secret in `.env` (default name
`LINA_EMBEDDING_API_KEY`, another one through `api_key_env`):

```yaml
embedding:
  base_url: <OpenAI-compatible endpoint ending in /v1>
  model: qwen3-embedding-8b   # through magpie: hub/nous/qwen/qwen3-embedding-8b, as GET <base_url>/models lists it
  dimensions: 1024
  # api_key_env: LINA_EMBEDDING_API_KEY   batch_size: 64   timeout: 60   max_chars: 8000   max_retries: 8
```

The default `qwen3-embedding-8b` is the bare model id an OpenAI-compatible provider takes. A node whose models come
through magpie (`base_url: http://127.0.0.1:3425/v1`) names a model in magpie's catalog form
`<remote>/<provider>/<id>` (`hub/nous/qwen/qwen3-embedding-8b`) and refuses the bare id; read the name from the
`id` field of `GET <base_url>/models` and set it as `embedding.model` (and as `EMBEDDING_MODEL_CONFIG__MODEL` in
`honcho.env`) before the first `lina store reembed`. The string is part of each row's key in `vec.embeddings`
and `vec.reembed_progress`, so changing it later means re-embedding everything under the new string. When magpie
answers that it knows no model, `reembed` and every other embedding call stop with one line that names
`embedding.model`, the endpoint and the closest embedding model it lists.

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

### Reaching models from Honcho

Honcho's api and deriver run in containers, so `127.0.0.1` in `honcho.env` is the container itself, not the
node. A node's magpie listens on `127.0.0.1:3425` only, and a host firewall with INPUT policy DROP also drops
traffic from containers to the host. Point Honcho at one of:

- **The hub's magpie**: an address of the hub's magpie that this node can reach, such as its tailnet name.
- **This node's magpie through the bridge address**: the gateway of the store's compose network, read with
  `docker network inspect lina-store_default --format '{{(index .IPAM.Config 0).Gateway}}'` (for example
  `172.18.0.1`). Magpie (or a forwarder in front of it) must listen on that address, and the firewall must let
  the containers in: `iptables -I INPUT -i br-<first 12 characters of the network id> -p tcp --dport 3425 -j
  ACCEPT` (the id from `docker network inspect lina-store_default --format '{{.Id}}'`).

Then in `store/honcho.env`, with `<address>` one of the two:

```sh
# The LLM callers: deriver, summary, dream and the five dialectic levels (transport openai by default)
LLM_OPENAI_BASE_URL=http://<address>:3425/v1
LLM_OPENAI_API_KEY=<the key magpie takes>
DERIVER_MODEL_CONFIG__MODEL=<a chat model as GET /v1/models lists it>
SUMMARY_MODEL_CONFIG__MODEL=<the same or another>
DREAM_DEDUCTION_MODEL_CONFIG__MODEL=<...>
DREAM_INDUCTION_MODEL_CONFIG__MODEL=<...>
# and DIALECTIC_LEVELS__<MINIMAL|LOW|MEDIUM|HIGH|MAX>__MODEL_CONFIG__MODEL=<...> for each level
# Embeddings: the model is embedding.model in config.yaml, in magpie's catalog form
EMBEDDING_MODEL_CONFIG__OVERRIDES__BASE_URL=http://<address>:3425/v1
EMBEDDING_MODEL_CONFIG__MODEL=<embedding.model, e.g. hub/nous/qwen/qwen3-embedding-8b>
```

Each caller can also take its own address and key (`<CALLER>_MODEL_CONFIG__OVERRIDES__BASE_URL`,
`..._API_KEY` or `..._API_KEY_ENV`); without a key of its own it uses `LLM_<TRANSPORT>_API_KEY` and
`LLM_<TRANSPORT>_BASE_URL`, and the embedding client uses its own `BASE_URL` override only.

`lina store install` and `lina store upgrade` end with a check of this, and `lina doctor` shows it for a
running store (section Memory Store): a key a caller needs and `honcho.env` does not set is named with the
callers that need it, and one `exec` in the api container asks each `*_BASE_URL` it uses for `<base>/models`.
An HTTP answer of any status counts as reached (no key is sent); each address that gives none is one line,
such as `Honcho cannot reach EMBEDDING_MODEL_CONFIG__OVERRIDES__BASE_URL (172.18.0.1:3425/v1/models: [Errno
111] Connection refused)`. The check only warns: the store is installed either way.

### Re-embedding and the quality check

```sh
lina store reembed --source agent|messages|memory|all [--max-rate N]   # default all
lina memory eval-search --questions <file.jsonl> [--honcho-url http://127.0.0.1:8000] [--json out.json]
```

`reembed` runs as the store owner, goes through `memory.messages` (`agent`: roles user and assistant;
`messages`: external messages, whose vectors take the shared owner `*`) and Honcho's conclusions in id order, a batch per transaction (`--batch-size`,
default `embedding.batch_size`), and records the last id of each batch, so a stopped run continues where it
stopped (`--from-start` starts over). It prints a line per batch and, per source, the rows done and the rows left. Renaming a profile moves its
`vec.chunks` rows and their `chunk` vectors to the new agent with its messages, in the same transaction (and back
when the rename fails); `hermes profile migrate-identity` moves any a rename left behind.

An answer of 429 (rate limited) or 5xx is not the end of a run: the client waits for the server's `Retry-After`, or
otherwise 1 s doubling up to 60 s, and sends the same batch again, up to `embedding.max_retries` times in a row
(default 8; a success starts the count over). When the last retry fails too, `reembed` stops with the batches done so
far saved and one line giving the rows left per source and the command that continues the run. `--max-rate N` keeps
the run under N requests per second, so a long run leaves room under a provider limit it shares with other agents
(default: no cap). An agent's own search does not retry: a search inside a turn fails at once instead of waiting.

`eval-search` scores the top-5 hit rate per source: today's search of each source (the store's full-text search
for `agent` and `messages`, Honcho's conclusion search for `memory`) against one vector search over all of the
agent's rows in `vec.embeddings`, its Honcho workspace's and the shared external messages' (owner `*`). A line of the questions file is
`{"id", "source", "agent", "question", "expected": [refs], "honcho": {"workspace", "observer", "observed"}}`;
a ref is a `memory.messages` uid or a Honcho conclusion id. A synthetic set (36 questions over a 48-item corpus)
is in `tests/fixtures/memory_eval/`.

### Importing the collector's vectors

```sh
lina store reembed --source messages --from-index <collector index.db> [--batch-size 500]
```

The message collector keeps its own derived SQLite index: one full-width vector per chunk of up to ten consecutive
messages of one room. Where `meta.vector_model` is `embedding.model`, `--from-index` cuts those vectors to the
width of `vec.embeddings` with `truncate_stored` (`agent/embedding_client.py`) instead of embedding the text
again. It opens the index read-only (SQLite `mode=ro`), never uses the collector's text, and needs
`embedding.model` and `embedding.dimensions` but not the endpoint. It reads the collector's tables:

| Table | Columns used |
| --- | --- |
| `meta(key, value)` | `vector_model` (e.g. `qwen/qwen3-embedding-8b`) and `vector_dimension` (e.g. `1536`), once for the whole index |
| `chunks(id, source, room_id, start_ts, end_ts, text)` | `id` |
| `chunk_messages(chunk_id, uid, ordinal)` | the chunk's collector uids, in `ordinal` order |
| `chunk_vectors` | sqlite-vec `vec0(embedding float[<dim>] distance_metric=cosine)`, `rowid = chunks.id` |

LINA does not depend on sqlite-vec, so the vectors are read from the shadow tables `vec0` 0.1.x creates:
`chunk_vectors_rowids(rowid, id, chunk_id, chunk_offset)` places a chunk's vector in a block,
`chunk_vectors_vector_chunks00(rowid, vectors)` holds a block's float32 little-endian vectors back to back (one is
`vectors[offset*dim*4 : (offset+1)*dim*4]`), and `chunk_vectors_chunks(chunk_id, size, validity, rowids)` gives
the block's size and a validity bitmap whose cleared bits are deleted cells, which are skipped. The import
refuses an index whose `chunk_vectors_info` `CREATE_VERSION` does not start with `v0.1.`, since another version may
store vectors differently.

A chunk becomes one `vec.chunks` row (`source = 'messages'`, `source_ref = 'collector'`, `message_ids` = the
`memory.messages` ids of `messages:<chunk_messages.uid>` in `ordinal` order, `text` = their LINA text joined by
newlines) and one `chunk` vector of `embedding.model`, both with the shared owner `*`, as `reembed` writes shared
external messages. A collector uid is the message's first version: a message edited later (`#<digest>` versions)
gets the vector under its unsuffixed uid. Left for `lina store reembed --source messages` and counted on the last
line:

- chunks with a message that is not a shared, unforgotten row of `memory.messages` (not ingested yet, forgotten,
  or not `scope = 'agents'`);
- empty chunks (no messages, or only empty text);
- every chunk of an index whose `vector_model` is another model;
- unusable vectors: no live vector for the chunk, a block that is not `size` vectors of `vector_dimension`
  values, fewer values than the table's width, or a zero vector.

The import keeps no cursor and leaves `vec.reembed_progress` alone. A chunk is keyed by its message ids, so a rerun
goes through the index again and inserts nothing it inserted before (`N already in the store`). `reembed --source
messages` skips a message that an imported chunk with a vector of its model covers and counts it (`N covered by
imported collector chunks`), so a same-model message is cut once and never embedded again; only the rest are
embedded.

Model names are compared by the part after the last `/`, exactly otherwise: the collector's
`qwen/qwen3-embedding-8b` (the provider prefix its endpoint uses) is LINA's `qwen3-embedding-8b`, and
`text-embedding-3-small` is another model. Rows are written with the configured `embedding.model` string, the one
searches read.

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
