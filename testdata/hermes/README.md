# Hermes reader fixtures

All rows are synthetic. `lcm.sql` and `state.sql` independently reconstruct the
column layouts confirmed from schema-only acceptance metadata. They are not
copies of the source exports. The registered layouts both use `user_version = 0`:

- `hermes-lcm`: 35 tables, including FTS virtual and shadow tables.
- `hermes-state`: 23 tables and the `messages_fts_src` and
  `messages_fts_trigram_src` views, including FTS virtual and shadow tables.

The fixtures reproduce ordered column names, declared types, nullability,
defaults, primary-key positions and hidden columns. Source trigger bodies,
secondary indexes and unrelated constraints are not needed by this metadata
check and are not copied. Synthetic FTS rows are populated explicitly. The
host's `schema_version` table is present, but no value for its unobserved row is
guessed; the registered version selector is the observed SQLite `user_version`.

Tests take online snapshots from live WAL databases and reconcile every table
and view against the snapshot, including preserved tool-call and reasoning
fields. They compare source database/WAL bytes and refuse unknown source kinds,
versions, columns and views.

`synthetic.sql` remains a minimal mechanics fixture. `openapi-synthetic.json`
tests the internal HTTP reader only: it is not a published Honcho description
and is never registered in the public reader. Honcho is refused until the owner
supplies its address and read credential at import time and a published API
description is obtained and registered. HTTP tests observe methods, credentials,
redirect refusal and exact response bytes using in-process servers.

`sidecar.txt` contains an intentionally synthetic credential assignment for
byte-preserving copy and sensitivity tests. No source rows, account details,
credentials or operational state from an install belong in these fixtures.
