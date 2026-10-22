# G2-22 records repository acceptance

Issue [#151](https://github.com/thisisjun786/lina/issues/151) was checked on
2026-10-22 against `dev` commit
`0891d078643b1cbcfc8c7a76eb70d9263eae9cb1`. Its sub-issues #152 and #153 are
closed, and their implementations are present at that commit. This record
connects the parent issue's five completion conditions to the existing code
and tests. It adds no runtime behavior.

Contracts: [C-53](../docs/design/go-core.md#records-and-memory),
[records repository](../docs/design/filesystem.md#records-repository), and
[G2-22](../docs/design/go-roadmap.md#2f-records-and-to-dos).

## Requirement evidence

| Completion condition | Executable evidence | What it checks |
| --- | --- | --- |
| A rebuilt index equals the live index | `TestIndexQueryMoveUpdateAndRebuildEquality`, `TestRecordsThousands` in [records tests](../internal/core/records/index_test.go) and [scale test](../internal/core/records/scale_test.go) | Compares every locator, text, FTS, Korean FTS, Hangul bigram, relation edge and index-head value after edits and a move. |
| Outside changes are detected, shown and not merged | `TestOutsideChangesAreValidatedShownAndNeverAdopted` and [installed Core index scenario](../e2e/m1/records_index_test.go) | Nine outside-change cases refuse operations and preserve the published index/history. Core tool results expose bounded change notices, preserve outside bytes and continue refusing them after restart. |
| A stale revision is refused | `TestRevisionMoveAndOneCommitPerWrite`, `TestConcurrentRevisionAndOutsideEdit` in [store tests](../internal/core/records/store_test.go), and [installed Core write scenario](../e2e/m1/records_test.go) | An old base and a losing concurrent writer fail; the installed tool reports `stale_revision` and leaves the current record unchanged. |
| Moving a record keeps its id | `TestRevisionMoveAndOneCommitPerWrite`, `TestIndexQueryMoveUpdateAndRebuildEquality`, `TestRecordsThousands` | Reads the moved record by the original id, checks its new revision and removal of the old path, and preserves that revision through rebuild. |
| Behavior with thousands of files is recorded | `TestRecordsThousands` and [measurement limits](../internal/core/records/MEASUREMENTS.md) | Exercises 3,000 files and four commits, compares full indexes, pages all records, searches and refuses an outside edit. |

## Runtime path

Core opens `records.Store` during conversation setup and registers `read_record`,
`write_record` and `query_records` through the ordinary authority and receipt
registry. The [tool test](../internal/core/exec/records_test.go) checks ownership,
refused identity/path injection, durable replay and stale writes. The
[result-save tests](../internal/core/results/records_test.go) check that saving a
result uses the same repository, produces one version, and refuses outside
changes and path escape.

The [write fixture](../e2e/m2/records.json) and
[index fixture](../e2e/m2/records-index.json) run through an installed Core in
temporary state. The HTTP provider is scripted; the tools, authority checks,
SQLite writer, Git files and restart are real. These scenarios establish the
record workflow, not live-model quality or remote worker execution. Explicit
path moves are tested through the repository interface; conversation tools do
not expose filesystem paths.

## Observed runs

The following checks ran uncached with Go 1.27.3 on Linux amd64:

| Command | Result |
| --- | --- |
| `go test ./internal/core/records -count=1 -v` | PASS, 7.970 s |
| `go test ./internal/core/exec -run '^TestRecordToolsScopeAndDurableReplay$' -count=1 -v` | PASS, 0.343 s |
| `go test ./internal/core/results -run '^Test(SaveCreatesOneVersionedRecord\|RecordsDetectExternalChangeAndRejectPathEscape)$' -count=1 -v` | PASS, 0.336 s |
| `dv heavy -- go test ./e2e/m1 -run '^TestM2Records(Index)?InstalledCore$' -count=1 -v` | PASS, both scenarios, 3.796 s |

The 3,000-file test took 5.43 s. Its individual observations were: seed
309.674 ms, initial rebuild 2.093 s, read 94.515 ms, edit 100.711 ms, rebuild
after edit/move/unrelated edit 1.288 s, search 34.078 ms and outside-change
detection 31.341 ms. These are one run on a temporary local filesystem, not
latency guarantees. Seeding bypasses 3,000 separate publications; long histories,
packed objects and remote repositories are not established by this fixture.

No new Hermes, Honcho, LCM-X or CRW behavior is ported here. Dependencies #38,
#42 and #43 were closed at verification; #123 remains the separate M1 acceptance
issue. These observations do not replace the candidate's required full
foundation check or current-head Codex review.
