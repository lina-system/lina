# G2-13 progress and waiting acceptance

Issue [#157](https://github.com/thisisjun786/lina/issues/157) was checked on
2026-10-23 against `dev` commit
`6b4fbb39057ffd9b5292f47a48457923013b1f55`. Sub-issues #158 and #159 are
closed, and their implementations are present. The subsequent `dev` commit
`f760a654043dd11072b299beebf9315bc6b335a5` has the same tree.
This record connects the parent's five completion conditions to existing
implementation and executable evidence. It adds no runtime behavior.

Contracts: [C-55](../docs/design/go-core.md#timers-progress-and-waiting),
[Progress at a glance](../docs/design/surfaces.md#progress-at-a-glance), and
[G2-13](../docs/design/go-roadmap.md#2d-owner-views).

## Completion conditions

| Condition | Evidence | Observed behavior |
| --- | --- | --- |
| A progress line contains no id or pull request; unobserved values are unknown | `TestComputeUnknownHierarchyAndSeenCompletion` in [progress tests](../internal/core/progress/compute_test.go), `TestProgressPagerUsesCurrentRowsWithoutIdentity` in [TUI tests](../internal/tui/ui/progress_test.go), and `TestM2ProgressWaitingInstalledCore` | Display data is separate from navigation references. Unobserved stage, next and just-finished values remain unknown; the percentage is absent. The running worker's thread and turn references do not appear in its display line. |
| The digest has at most five lines and a count | `TestDigestBoundsCountsAndKeepsWaitingFirst` in [digest tests](../internal/core/progress/digest_test.go), `TestCoreDigestCountsFullWaitingAndRereadsSources` in [Core tests](../internal/core/boot/owner_controls_test.go), and `TestM2ProgressDigestWorker` | Zero, two, five and eight waiting rows exercise the boundary. Seven live approvals produce five lines, total seven and two omitted; resolved approvals disappear. Worker approval and question phases contribute to the installed Core digest. |
| A waiting row is dropped when its source is done | `TestRoundSourceRereadsCompletion` in [producer tests](../internal/core/waiting/producers_test.go) and `TestM2ProgressWaitingInstalledCore` | Completing the source removes its rows. Installed Core removes answered approvals and questions; an outside-change row appears on an open subscription and disappears after restoration, without another conversation turn. |
| The order switch is asked of Core | `TestOwnerCommandsAskCoreAndRetainWaitingOrder` in [TUI tests](../internal/tui/ui/progress_test.go), `TestPriorityAndLightestFixture` in [waiting tests](../internal/core/waiting/waiting_test.go), and the installed waiting and control scenarios | The TUI sends `waiting_get` for both the command and the `o` switch and retains the chosen order on refresh. Core ranks the fixture and applies the Home limit after counting. Eight worker questions retain their arrival order across both requests. |
| Stop, stop all and follow-up have the same effect from the TUI and a channel | `TestM2ProgressControlsSurfaces` and `TestOwnerControlsReachLiveWorker` in [Core tests](../internal/core/boot/owner_controls_test.go) | Both surfaces persist the same follow-up and cancel the selected run. Both stop-all variants cancel the owner's runs and preserve another person's run. Core also delivers a note and interrupt to the worker adapter. Channel commands and replies are published live and retain their order after restart. |

## Running Core evidence

[Core composition](../internal/core/boot/owner_views.go) registers the overview,
waiting query and progress topic. Its producers reread authoritative sources;
[Core controls](../internal/core/boot/owner_controls.go) compute the digest and
apply run or worker controls. [Channel controls](../internal/core/boot/channel_controls.go)
use those same operations after checking the binding and sender.

The headless scenarios use the existing M1 harness and M2 fixtures:

- [progress-waiting.json](../e2e/m2/progress-waiting.json), exercised by
  [the installed waiting test](../e2e/m1/progress_waiting_test.go), drives a
  stdio child through task projection, approval, questions and an artifact write.
  The harness reads the artifact and checks its contents, then restarts Core.
- [progress-controls.json](../e2e/m2/progress-controls.json), exercised by
  [the digest and controls tests](../e2e/m1/progress_controls_test.go), covers
  the worker digest, built TUI, paired gateway and canonical run records.
- [progress-seen-review.json](../e2e/m2/progress-seen-review.json), exercised by
  [the acknowledgement test](../e2e/m1/progress_seen_review_test.go), verifies
  that opening or dismissing landed work retires its progress line, including
  after restart, while preserving the work verdict and artifact.

Core, storage, authority, subprocess transport, TUI and local artifact writes
are real. Model responses, the Codex executable's protocol behavior and the
channel HTTP service are fixtures. These runs establish local integration,
not live-provider quality or delivery through an external channel service.

## Observed runs

All checks ran uncached with Go 1.27.3 on Linux amd64. The commands below were
executed inside `dv heavy`; the Core, TUI and e2e run used `TMPDIR=/tmp`.

| Command | Result |
| --- | --- |
| `go test -count=1 ./internal/core/progress ./internal/core/waiting ./internal/core/gateway/... ./internal/core/clientsrv` | PASS, all six packages; 1.619–12.368 s per package |
| `go test -count=1 ./internal/core/boot -run 'Test(Owner\|CoreDigest\|ForwardedOwnerCommand\|ChannelControlRecovers)'` | PASS, 1.845 s |
| `go test -count=1 ./internal/tui/ui -run '^Test(ProgressPagerUsesCurrentRowsWithoutIdentity\|OwnerCommandsAskCoreAndRetainWaitingOrder)$'` | PASS, 0.017 s |
| `go test -count=1 -timeout=6m ./e2e/m1 -run '^TestM2Progress(WaitingInstalledCore\|DigestWorker\|ControlsSurfaces\|SeenReviewInstalledCore)$' -v` | PASS, all four tests and both control-surface subtests; 16.234 s |

The first Core run failed before its assertion because the inherited temporary
directory made a Unix socket path too long (`bind: invalid argument`). Using a
short temporary directory passed with identical source; no timeout or assertion
was changed. No new runtime fix or artificial failing test was needed.

Dependency #154's sub-issues #155 and #156 are closed and their code is present;
the parent remains open. C-55 separately defers the to-do waiting producer and
plan-derived stage/bar to that work. This evidence establishes #157's five
conditions and does not claim those deferred features. No Hermes, Honcho,
LCM-X or CRW behavior is ported here. The candidate still requires its full
foundation run and current-head Codex review before handoff.
