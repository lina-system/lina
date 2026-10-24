# G2-07 delegation review and delivery acceptance

Issue [#142](https://github.com/thisisjun786/lina/issues/142) was reopened by
the merged-PR audit because three done-when items were not met: a failing
required check and a change outside the write area were not recorded as P1
findings, and a second review request was refused only for the same delivered
head. This change implements them. This record connects the parent's completion
conditions to code and executed tests.

The [owner's correction on #142](https://github.com/thisisjun786/lina/issues/142#issuecomment-6307352471)
and [#272](https://github.com/thisisjun786/lina/issues/272) still govern who
owns CI and review fixes: the worker owns them inside its turn, the manager
checks the work definition, and Core computes the mechanical evidence. The
current [Judgment and delivery contract](../docs/design/work-and-delegation.md#judgment-and-delivery),
[C-58](../docs/design/go-core.md#work-engine) and
[G2-07](../docs/design/go-roadmap.md#2b-delegation) were updated in the same
change.

## Completion conditions

| Condition | Executed evidence | Observed behavior |
| --- | --- | --- |
| Refuse a second review request for the pull request | `TestSecondReviewRequestForTheSamePullRequestIsRefused` and `TestVerifiedPullRequestIsNotReviewedAgain` in [review store tests](../internal/core/review/store_test.go); `TestConformanceMechanicalDecisionsSkipManagerAndRejectDuplicate` and `TestConformanceFixGenerationChecksNewHeadWithoutReusingVerdict` in [Core tests](../internal/core/boot/conformance_test.go) | Concurrent claims admit one request. A changed head and another task are refused, before and after a judgment. Only the head produced by the one return, in the same task's next generation, continues the review once; a third request is refused. |
| Return only P0/P1 finding ids once | `TestReturnKeepsOnlyBlockingFindingIDs` in [return tests](../internal/core/review/return_test.go); `TestM2DeliveryInstalledCore` in [delivery scenarios](../e2e/m1/delivery_test.go) | The unit test retains P0 and P1 and excludes P2 and P3 by id. The installed worker's correction packet contains `major`, excludes `minor` and `nit`, and leaves all three original findings stored. Exactly two worker generations run for a correction. |
| Record a failed required check and a write-area miss as P1 findings | `TestFailedCheckAndScopeMissAreCoreComputedP1Findings` and `TestWaitsAndIdentityMismatchesStayFindingsFree` in [precondition tests](../internal/core/review/preconditions_test.go); `TestM2ConformanceInstalledCore` in [conformance scenarios](../e2e/m1/conformance_test.go) | A failed required check blocks with `merge_precondition` and one `core-check-1` P1 finding naming the check. An out-of-area path records `needs_changes` with one `core-scope-1` P1 finding per path. Both are schema-valid and call no model. Pending, missing or mismatched evidence records no finding, and unavailable or oversized evidence blocks before a scope verdict. |
| Close a residual failure with its reason and leave the task blocked without verification | `TestM2DeliveryInstalledCore`; `TestCodingUnchangedCorrectionCloses`, `TestCodingInterruptedCloseObservesWithoutRepeatingWrite` and `TestCodingFreshFailureOverridesSavedVerifiedJudgment` in [Core delivery tests](../internal/core/boot/coding_delivery_test.go) | A residual P1, an unchanged correction, a failed required check and an unresolved GitHub P1 each close with the saved reason. Core findings are named by id in that reason. The work card is `input_needed`, has no verdict and is not applied. Restart preserves closure without another worker generation or manager call. |

## Running Core evidence

[Conformance admission](../internal/core/boot/conformance.go) claims the
pull request before the manager call, evaluates mechanical gates, records
Core-computed findings for failed checks and out-of-area paths, and commits the
review with the work verdict. The installed scenario also checks that a
prose-only reply records neither findings nor a verdict.

[Delivery](../internal/core/boot/coding_delivery.go) reads the saved judgment,
treats a refused second review as no verdict, returns one missed definition
through the worker dispatcher, and uses the existing GitHub close and merge
services. A corrected result merges the observed head and becomes applied only
after confirmation. A remaining gap closes with the finding identities and
blocks the task. The focused Core tests also cover stale-head refusal,
interrupted-close observation, retained merge recovery and goal-owned delivery
leaving the merge to the goal.

The [conformance fixture](../e2e/m2/conformance.json) and
[delivery fixture](../e2e/m2/delivery.json) use the existing
[installed-Core harness](../e2e/m1/antigravity_test.go). The tests launch the
built Core and worker subprocesses, create real temporary Git worktrees,
produce and inspect `result.txt`, observe commits, inspect durable records,
then restart Core and inspect the result again. Protected base refs remain
unchanged. Model responses, the worker executable and GitHub are fixtures;
these runs do not establish live-provider quality or real GitHub writes.

## Observed runs

All commands ran uncached with Go 1.27.3 on Linux amd64, on the branch merged
with `origin/dev`. The e2e command ran through `dv heavy`.

```sh
go test ./internal/core/review/... ./internal/core/worker/antigravity/... -count=1
go test ./internal/core/boot -run 'Test(Conformance|Coding|GoalDelivery)' -count=1
dv heavy -- go test ./e2e/m1 -run '^TestM2(Conformance|Delivery)InstalledCore$' -count=1 -v -timeout=10m
```

| Run | Result |
| --- | --- |
| Review and adapter packages | PASS, 1.099 s and 0.250 s |
| Focused Core tests | PASS, 41.147 s |
| Installed Core conformance and delivery | PASS, 16.816 s; all 11 subscenarios |

The conformance cases are `success`, `scope`, `scope-oversized`,
`failed-check`, `prose` and `findings`. The delivery cases are `corrected`,
`unchanged`, `residual`, `failed-check` and `residual-review`.

No Hermes, Honcho, LCM-X or CRW behavior is ported here. This acceptance run
did not execute the full suite; the change still requires the candidate's
foundation gate and current-head Codex review before handoff.
