# internal/addons/importer/murmur implementation notes

Design authority: [conversation-and-memory.md](../../../../docs/design/conversation-and-memory.md#importing-a-murmur-store),
[Memory importer add-ons](../../../../docs/design/conversation-and-memory.md#memory-importer-add-ons),
[integrations.md](../../../../docs/design/integrations.md#memory-importers) and
[filesystem.md](../../../../docs/design/filesystem.md#importing-another-memory-system).
Research: [messengers.md](../../../../docs/research/messengers.md#murmur-mapped-onto-core).
These notes record how this package meets them; they do not restate the contract.

## Ported shape: Hermes, Honcho, LC M-X and CRW

This adapter ports the *importer shape* of `internal/addons/importer/hermes`,
not its source schemas.

- **Seen** (read from the references on 2026-10-23, read-only):
  - `murmur` at `513b74a`: the spool is the authority and the database is
    derived from it (`src/murmur/storage.py`, `schema.py`). A spool line is one
    event of `version`, `event_id`, `ingested_at`, `messages` and
    `observations`. Message fields are murmur's model (`model.py`): source,
    account, room and native id, `uid`, author, `ts`, `edited_ts`, `deleted`,
    reply and thread uids, text, attachments, raw, `ingest_path`, room name.
  - `murmur.model.uid` is the SHA-256 of the compact JSON array
    `[source, room_id, native_id]`; `murmur.cli manifest` /
    `storage.manifest` publish per room a count and the SHA-256 of each sorted
    UID followed by a newline.
  - murmur's upsert keeps the archive value of one key over the live value, and
    within one path the later observation (`storage.UPSERT`).
  - Discord room ids come from `channel_id` when the export carries it, else the
    JSONL file's stem (`importers._discord_record`).
  - iMessage's own messages are the rows whose `is_from_me` is set; their author
    id is `me` (`imessage.read_snapshot`).
  - Hermes importer shape: a verified snapshot in staging, a schema gate before
    any record read, an immutable plan whose rehearsal is the work that runs,
    legacy references written in the record's transaction, per-room
    reconciliation, and a backup generation at the end.
- **Inferred**: that a murmur home handed over while its collector is stopped has
  a complete spool, so a torn final line is a refusal rather than a partial
  import; that an empty `sources` list in `config.json` still describes the store
  the spool holds; that murmur's business flag maps to LINA's `channel` chat kind
  and every other chat to `group`.
- **Unknown**: whether a real store's spool always carries `attachments` and
  `raw` as JSON arrays and objects; murmur's own privacy classes have no LINA
  counterpart, so they are not imported; the size of a real store.

## What a run reads

Only `spool/events.jsonl` and `config.json`, copied with a SHA-256 check into
the run's staging directory. The copy is verified against the source and the
source's fingerprint must not change while it is copied. The derived database,
vectors and model cache are never opened. Staged copies are removed when the run
ends; the data home is never written, moved or deleted.

## The schema gate

A spool event must have `version: 1` and no field the adapter does not know; each
message must carry exactly murmur's model fields and a `uid` that equals the hash
of its own source, room and native id; each room must have an observation; the
ingest path must be `archive`, `live` or `export`. `config.json` must have
`version: 1`. Any difference refuses the whole store with the byte offset and the
field named, and nothing is imported from it.

## Records, keys and precedence

Each message becomes one messenger record of its source's chat. Its legacy
reference is `(murmur, <data home>, uid, <uid>)`; the platform reference the
messenger record already carries is the live one, so a source LINA connects later
joins the same records. The archive value of one key wins over the live value, as
in murmur; a changed source row without a later `edited_ts` is reported as a
conflict and never overwritten; an edit is a revision that `supersedes` the
previous one; `deleted` rows go through the retraction path and keep their
reference as a tombstone.

Room digests are checked against murmur's own rule, so the count and the SHA-256
of the sorted UIDs of every room match murmur's manifest.

## Speaker, sources and learning

The person's own author id per source is confirmed in the run; iMessage's
`me` author proves it without a map. Every other author is an
`external_participant`. No sender identity exists yet, so no sender is matched to
a people record, and the report says so. Text is masked at admission; LINA keeps
no unmasked copy.

A source the person has not connected is created in the import-only state with
no adapter session. Its mode states what LINA's own connection would be
(`kakaotalk` unofficial, the others official); the terms a person accepted in
murmur are not LINA's, so the connect card asks again when they connect.

Learning is the person's choice in the import card. The card states the count of
their own messages and an estimated call and token budget. With learning off the
import writes no memory item and moves the source's learning watermark past the
imported history, so it stays search-only. With learning on the watermark sits at
the start of that history, so the extraction job of #1075 covers it; this
increment states the request and does not itself learn.
