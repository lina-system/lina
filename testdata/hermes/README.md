# Hermes reader fixtures

All content here is synthetic. `synthetic.sql` and `openapi-synthetic.json`
exercise the schema gate, online backup and HTTP snapshot boundary; they are
**not** accepted Hermes or Honcho schemas and do not claim install compatibility.
`sidecar.txt` contains an intentionally synthetic credential assignment for
byte-preserving copy and sensitivity tests.

The importing contract requires independently verified, schema-only metadata
before adding supported real-source descriptors. No message, account, profile,
credential or operational data from an install belongs here.

Tests build fresh SQLite databases from SQL, keep committed rows in a live WAL,
and compare source bytes around online backup. HTTP tests use an in-process
server, observe every method and request, and verify rejected schemas cause no
data reads. Secret tests preserve original bytes and check that projections
exclude flagged spans.
