# Staged Hermes import reference specification

References are read-only public snapshots at the revisions already credited in
[THIRD-PARTY-NOTICES.md](../../../THIRD-PARTY-NOTICES.md). The named local plugin
paths were unavailable during this implementation. No installation data is used.

- **seen** — LCM-X `host_uid.py:102-130` resolves session lineage through
  compression parents, stopping at forks, missing parents and cycles;
  `tests/test_host_uid_shadow.py:115-124` binds newly stored messages as canonical
  in that lineage. Import namespaces must include the profile and lineage.
- **seen** — LCM-X `host_uid.py:299-334` distinguishes canonical bindings from
  payload-matching versions. `tests/test_host_uid_shadow.py:173-185` retains
  uid-less messages. Import retains all raw rows, not only UID-bearing rows;
  generated engine bindings must not turn into invented message rows.
- **seen** — Honcho `hermes-plugin-honcho/session.py:425-451` writes user-role
  messages under the per-message author when supplied, otherwise the session's
  user peer. `tests/test_turn_author_peers.py:40-48,85-96,119-135` separates bot
  authors from the owner and writes the author's peer. An owner peer alone
  therefore cannot establish person attribution in historical records.
- **inferred** — Map source copies to a canonical event while preserving each
  source row's distinct fields and key. Reuse the existing host/LCM provenance
  classifier after all matches; never derive speaker kind from text.
- **inferred** — LINA's ordered stages, fenced conversation transaction,
  row-level legacy references, typed canonical row hashing and reverse mapper
  implement C-28; these are LINA requirements, not upstream import algorithms.
- **unknown** — The installation's published Honcho API description remains
  unregistered. Live Honcho ingestion must retain the existing refusal. The
  stage engine accepts adapter-prepared records; synthetic tests exercise
  matching and history adoption without registering a fictional server schema.

The Go tests cover canonical/version/engine binding distinctions, repeated IDs
in different profiles, unchanged per-copy source fields, owner/automation
attribution, missing links, rollback, round-trip hashes and idempotent replay.
