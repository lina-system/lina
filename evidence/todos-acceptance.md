# G2-23 to-do acceptance

Issue [#154](https://github.com/thisisjun786/lina/issues/154) was checked on
2026-10-23 against `dev` commit
`2e70540cd8a20ac198e0978b11c83a1ae698a03a`. Its sub-issues #155 and #156 and
dependencies #129 and #151 were closed, with their implementations present at
that commit. This parent acceptance record maps the five completion conditions
to existing tests and installed Core scenarios. It adds no runtime behavior.

Contracts: [C-54](../docs/design/go-core.md#records-and-memory),
[the to-do record](../docs/design/work-and-delegation.md#the-to-do-record), and
[G2-23](../docs/design/go-roadmap.md#2f-records-and-to-dos).

## Requirement evidence

| Completion condition | Executable evidence | What it checks |
| --- | --- | --- |
| The today rule passes its fixture | `TestTodoTodayFixtureAndViews`, `TestTodoTodayDSTBounds` in [to-do tests](../internal/core/records/todo_test.go), using the [today fixture](../internal/core/records/testdata/today.json) | Starts today or earlier, deadlines today and current recurring occurrences appear; undated tasks remain in anytime. Terminal states, local midnight, DST boundaries, other views, owner isolation and index rebuild are covered. |
| A recurring item advances only to a future occurrence | `TestRecurringCompletionAdvancesFuture`, `TestRecurrenceCompletionModesAndExhaustion`, `TestRecurrenceKeepsTodayAndOriginalAnchor` in [recurrence tests](../internal/core/records/recurrence_test.go), plus `TestTodoRecurrenceNextCorpus` in [timer tests](../internal/core/timers/recurrence_test.go) | Completion advances the same record strictly beyond the current time; missed occurrences do not accumulate. Tests check completion-relative scheduling, exhaustion, preserved anchors, month ends, leap years and DST gaps/folds. |
| A dependency cycle is rejected | `TestTodoDependencyCycleDoesNotPublish`, `TestTodoConcurrentCyclesSerialize` in [to-do tests](../internal/core/records/todo_test.go) | Self-edges and transitive cycles fail without changing history. Concurrent writes serialize so only the noncyclic change succeeds. |
| An ambiguous date is never saved silently | `TestTodoAmbiguousDateDoesNotPublish` in [to-do tests](../internal/core/records/todo_test.go), and [timestamp refusal tests](../internal/core/records/todo_timestamp_test.go) | Ambiguous English/Korean dates, ambiguous numeric dates, impossible dates and times without offsets fail before publication. Timestamp checks cover each date field and leave files, history and index unchanged. |
| A candidate is added on its own only when the deterministic test passes | `TestCandidateDeterministicGate` and related [candidate tests](../internal/core/records/candidates_test.go) | Retained owner requests/promises with matching actions can be added. Foreign owners, agent/automation claims, forged quotes, unrelated actions, negation and quoted fragments cannot. Missing/retracted evidence, open equivalents, replay after undo, rejection fallback and interrupted publication are covered. |

## Running Core evidence

The [to-do scenario](../e2e/m2/todos.json), executed by
[`TestM2TodosInstalledCore`](../e2e/m1/todos_test.go), saves tasks through the
installed conversation tools, receives `ambiguous_date` and `dependency_cycle`,
checks today/upcoming/anytime results and reschedules a start without changing
the deadline. It restarts Core after deleting the disposable to-do projection
and verifies the rebuilt results and preserved revisions.

The [recurrence and promotion scenario](../e2e/m2/record-recurrence-promotion.json),
executed by [`TestM2RecordRecurrencePromotionInstalledCore`](../e2e/m1/record_recurrence_promotion_test.go),
promotes and undoes a memo, completes a recurring task and advances an overdue
task at restart. The ids stay stable, no extra task instances appear, and the
work ledger records one completion and one overdue advancement. The provider
call count stays unchanged during startup advancement. Repository
[promotion tests](../internal/core/records/promotion_test.go) also check stable
paths, stale-write refusal, idempotent undo and recovery with the undo receipt.

The [candidate scenario](../e2e/m2/record-candidates.json), executed by
[`TestM2RecordCandidatesInstalledCore`](../e2e/m1/record_candidates_test.go),
adds one verified commitment with an undo handle and leaves forged, missing and
duplicate evidence pending. It checks addition/undo notices, retained large
source material, canceled record state and pending/rejected candidates after
restart.

Core [registers record and candidate tools](../internal/core/boot/conversation_records.go)
through its authority and receipt path. Its [timer runtime](../internal/core/boot/timers.go)
advances recurrences at startup and each minute. These scenarios use temporary
synthetic state and a scripted HTTP provider; the Core process, tools, SQLite,
versioned files and restarts are real. They establish the requested record
workflow, not live-model interpretation or delegated worker execution.

## Observed runs

The following existing checks ran uncached with Go 1.27.3 on Linux amd64:

| Command | Result |
| --- | --- |
| `go test ./internal/core/records -run '^Test(Todo\|Repeat\|Recurring\|Recurrence\|CompletionRelative\|Candidate\|MemoPromotion)' -count=1 -v` | PASS, 28.375 s |
| `go test ./internal/core/timers -run '^TestTodoRecurrenceNextCorpus$' -count=1 -v` | PASS, 0.002 s |
| `go test ./internal/core/exec -run '^Test(Todo.*\|CandidateSchemaCannotSupplyTrustedFacts)$' -count=1 -v` | PASS, 0.004 s |
| `dv heavy -- go test ./e2e/m1 -run '^TestM2(Todos\|RecordRecurrencePromotion\|RecordCandidates)InstalledCore$' -count=1 -v` | PASS, all three scenarios, 8.041 s |

No behavior was missing from the checked `dev` snapshot, so there is no new
pre-fix failure or production fix. Existing negative cases verify refusals;
they are passing checks, not a claimed red/green change. No new Hermes, Honcho,
LCM-X or CRW behavior is ported here. These observations do not replace the
candidate's full foundation gate or current-head Codex review.
