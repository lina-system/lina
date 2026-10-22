# Records index measurement

The synthetic `TestRecordsThousands` acceptance fixture uses 3,000 Markdown
files, each with an id, kind, state, person, title and note. It seeds one known
published generation, then uses the production write path for an edit, a move
and an unrelated edit. All four commits remain available. The seed bypasses
single-record publication only to prepare the fixture; it does not measure
creating 3,000 separate saved versions.

Observed on 2026-10-22 with Go 1.27.3, a temporary local filesystem and the
pure-Go Git adapter, using:

```sh
go test ./internal/core/records -run TestRecordsThousands -count=1 -v
```

| Operation | Observed duration |
| --- | ---: |
| Seed 3,000 files and their Git objects | 11.501 s |
| Rebuild after seeding | 656 ms |
| Read one record | 53 ms |
| Commit one edit | 87 ms |
| Rebuild after edit, move and unrelated edit | 346 ms |
| Search title/note with one matching result | 21 ms |
| Detect one outside edit | 24 ms |
| Whole acceptance test, including all pages and comparisons | 13.92 s |

These are observations from one run, not latency guarantees or a backend
comparison. Every query validates current files, so it scans the repository
before using SQLite FTS. The test checks all 3,000 ids through pagination,
compares all locator, text, FTS, edge and index-head values before and after
rebuild, and refuses an outside edit. No timing threshold decides success.
Revision reconstruction walks history once for the pending files; long histories
with many distinct trees remain an additional cost not established by this
four-commit measurement. This fixture does not establish behavior for packed
objects, remote Git repositories, or millions of records.
