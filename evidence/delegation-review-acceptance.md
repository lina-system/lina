# G2-07 delegation review and delivery acceptance

Issue [#142](https://github.com/thisisjun786/lina/issues/142) was checked on
2026-10-23 against `dev` commit
`f20a15c53c68751c8840d990a69fa23ee557cfc8`. Sub-issues #143 and #144 and
dependencies #128, #136 and #139 are closed, and their implementations are
present. This record connects the parent's completion conditions to existing
code and executed tests. It adds no runtime behavior.

The [owner's correction on #142](https://github.com/thisisjun786/lina/issues/142#issuecomment-6307352471)
and [the explicit mapping on #143](https://github.com/thisisjun786/lina/issues/143#issuecomment-6357043019)
apply [#272](https://github.com/thisisjun786/lina/issues/272): the worker owns
CI and review fixes; the manager checks the work definition. The current
[Judgment and delivery contract](../docs/design/work-and-delegation.md#judgment-and-delivery),
[C-58](../docs/design/go-core.md#work-engine) and
[G2-07](../docs/design/go-roadmap.md#2b-delegation) govern acceptance. The
original issue wording does not require classifying CI failures as P1 or
refusing the one check of a corrected head.

## Completion conditions

| Condition under the corrected contract | Executed evidence | Observed behavior |
| --- | --- | --- |
| Refuse a second check of the same delivered head | `TestOneClaimPerDeliveredHeadSurvivesServiceRecreation` in [review store tests](../internal/core/review/store_test.go); `TestConformanceMechanicalDecisionsSkipManagerAndRejectDuplicate` and `TestConformanceFixGenerationChecksNewHeadWithoutReusingVerdict` in [Core tests](../internal/core/boot/conformance_test.go) | Concurrent claims admit one request. Recreating the service does not admit another. A correction with a new head gets one check without inheriting or overwriting the previous judgment. |
| Return only P0/P1 finding ids once | `TestReturnKeepsOnlyBlockingFindingIDs` in [return tests](../internal/core/review/return_test.go); `TestM2DeliveryInstalledCore` in [delivery scenarios](../e2e/m1/delivery_test.go) | The unit test retains P0 and P1 and excludes P2 and P3 by id. The installed worker's correction packet contains `major`, excludes `minor` and `nit`, and leaves all three original findings stored. Exactly two worker generations run for a correction. |
| Compute failed-check and write-area decisions in Core | `TestFailedCheckIsPreconditionAndScopeMissIsNotModelFinding` in [precondition tests](../internal/core/review/preconditions_test.go); `TestM2ConformanceInstalledCore` in [conformance scenarios](../e2e/m1/conformance_test.go) | A failed required check blocks with `merge_precondition`, no verdict and no findings. An out-of-area path records `needs_changes` with scope evidence and no findings. Neither calls the manager model. Unavailable or oversized evidence blocks before a scope verdict. |
| Close a residual failure with its reason and leave the task blocked without verification | `TestM2DeliveryInstalledCore`; `TestCodingUnchangedCorrectionCloses`, `TestCodingInterruptedCloseObservesWithoutRepeatingWrite` and `TestCodingFreshFailureOverridesSavedVerifiedJudgment` in [Core delivery tests](../internal/core/boot/coding_delivery_test.go) | A residual P1, an unchanged correction, a failed required check and an unresolved GitHub P1 each close with the saved reason. The work card is `input_needed`, has no verdict and is not applied. Lower-priority ids are absent from the residual close reason. Restart preserves closure without another worker generation or manager call. |

The P0/P1 return filter feeds the same residual-close path in
[Core delivery](../internal/core/boot/coding_delivery.go). The installed
residual scenarios use P1; the return unit test exercises both priorities.

## Running Core evidence

[Conformance admission](../internal/core/boot/conformance.go) claims the
repository/PR/head before the manager call, evaluates mechanical gates and
commits the review with the work verdict. The installed scenario also checks
that a prose-only reply records neither findings nor a verdict.

[Delivery](../internal/core/boot/coding_delivery.go) reads the saved judgment,
returns one missed definition through the worker dispatcher, and uses the
existing GitHub close and merge services. A corrected result merges the
observed head and becomes applied only after confirmation. A remaining gap
closes with the finding identities and blocks the task. The focused Core tests
also cover stale-head refusal, interrupted-close observation, retained merge
recovery and goal-owned delivery leaving the merge to the goal.

The [conformance fixture](../e2e/m2/conformance.json) and
[delivery fixture](../e2e/m2/delivery.json) use the existing
[installed-Core harness](../e2e/m1/antigravity_test.go). The tests launch the
built Core and worker subprocesses, create real temporary Git worktrees,
produce and inspect `result.txt`, observe commits, inspect durable records,
then restart Core and inspect the result again. Protected base refs remain
unchanged. Model responses, the worker executable and GitHub are fixtures;
these runs do not establish live-provider quality or real GitHub writes.

## Observed runs

All commands ran uncached with Go 1.27.3 on Linux amd64. The e2e command ran
through `dv heavy`.

```sh
go test ./internal/core/review -count=1
go test ./internal/core/boot -run 'Test(Conformance|CodingCloseIntent|CodingInterruptedClose|CodingUnchangedCorrection|CodingFreshFailure|CodingRetainedMerge|GoalDelivery)' -count=1
dv heavy -- go test ./e2e/m1 -run '^TestM2(Conformance|Delivery)InstalledCore$' -count=1 -v -timeout=6m
```

| Run | Result |
| --- | --- |
| Review package | PASS, 0.753 s |
| Focused Core tests | PASS, 46.288 s |
| Installed Core conformance and delivery | PASS, 23.882 s; all 11 subscenarios |

The conformance cases are `success`, `scope`, `scope-oversized`,
`failed-check`, `prose` and `findings`. The delivery cases are `corrected`,
`unchanged`, `residual`, `failed-check` and `residual-review`.

No behavior fix or artificial failing test was needed: these checks passed
on the existing implementation. No Hermes, Honcho, LCM-X or CRW behavior is
ported here. This acceptance run did not execute the full suite; the change
still requires the candidate's foundation gate and current-head Codex review
before handoff.
