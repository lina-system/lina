# G1-11 inbox, lanes, steering and recovery acceptance

Issue [#59](https://github.com/thisisjun786/lina/issues/59) was checked on
2026-10-22 against `dev` commit
`689ff60e3d0218867b9ecc3e0209f1cd14fea1eb`. All of its Done when behaviors
already existed at that commit. This record connects the parent issue to the
implementation and executable evidence; it introduces no runtime behavior.

Contracts: [C-09](../docs/design/go-core.md#conversation),
[conversation loop](../docs/design/runtime.md#conversation-loop), and
[G1-11](../docs/design/go-roadmap.md).

## Requirement evidence

| Condition | Existing proof |
| --- | --- |
| A duplicate key inserts one row. | `TestDuplicateKeyCommitsOneRowAcrossCallersAndRestart` in [inbox tests](../internal/core/inbox/store_test.go) submits 16 concurrent duplicates and reopens SQLite, checking one row, the same id and original bytes. `TestEveryKeyFieldParticipatesInUniqueness` checks each key dimension. The installed conversation scenario also resends an input id with changed text and observes the original acknowledgement without another model call. |
| The acknowledgement follows the commit. | `TestAcknowledgementFollowsCommit` observes absence through a separate reader before commit and presence after the receipt. `TestRollbackAndCancellationNeverAcknowledge` and `TestCommitFailureNeverAcknowledges` check rollback, cancellation and a deferred foreign-key failure at COMMIT, all with an empty receipt and zero rows. These live in the same inbox test file. [Core admission](../internal/core/boot/conversation.go) commits inbox and ledger admission before returning or publishing its acknowledgement. |
| One lane runs one turn per conversation. | `TestConversationSerializesTurnsAndOtherConversationsProceed` in [lane tests](../internal/core/lane/lane_test.go) checks submission order and independent conversations. The cancellation and shutdown tests keep the lane occupied until work returns, including an expired shutdown wait. |
| Input during a turn reaches the next round. | `TestLaneSteeringReachesNextRoundOnly` in [turn control tests](../internal/core/turn/control_test.go) holds the first request, commits steering, and checks its exact bytes in the second request only. [Lane control tests](../internal/core/lane/control_test.go) verify active-turn targeting and caller ownership after queue closure. The installed history-order scenario observes two rounds within one turn for steering, versus two turns for explicitly queued input. |
| Stop ends open calls as aborted. | `TestStopAbortsOpenCallsWithTypedCause` in the turn control tests exercises every interrupt issuer, checking both the active and not-yet-started call results and the stored terminal state. The installed interrupted-reply scenario sends Esc through the TUI and restores the stopped history across client reopen and graceful and forced Core restarts. Already completed or unknown effect receipts retain their outcomes as required by C-08. |
| A mid-turn process kill does not replay input and leaves one plain line. | `TestKillDuringTurnRecordsOneLineWithoutReplay` in [restart tests](../internal/core/turn/restart_test.go) sends SIGKILL after a committed output item, runs recovery twice, checks the aborted open call and exactly one single-line system event, and proves re-claiming the input makes zero model calls. `TestRecoveryNoticeAndTerminalStateCommitTogether` checks rollback and retry of notice persistence. The installed partial-history scenario kills Core during visible streaming, preserves the exact displayed rows, appends one system notice, and observes no request replay. |
| All sub-issues are closed. | GitHub reported [#60](https://github.com/thisisjun786/lina/issues/60) closed at `2026-10-21T01:56:46Z` and [#61](https://github.com/thisisjun786/lina/issues/61) closed at `2026-10-21T05:31:13Z`. |

## Commands and results

The existing tests ran before any documentation edits. No missing runtime
behavior was found, so there is no new regression or manufactured red run.

```sh
go test -count=1 ./internal/core/inbox/... ./internal/core/lane/... ./internal/core/turn/...
```

Passed: inbox (0.316 s), lane (0.405 s), turn (2.319 s).

```sh
go test -count=1 -v ./e2e -run '^(TestM1TUIConversationInstalledCore|TestM1ConversationHistoryOrder|TestM1TUIInterruptedReplySurvivesCoreRestart|TestM1InterruptedPartialHistory)$'
```

Passed: all four tests and their subtests (9.955 s total). Their independent
scenario files are:

- [tui-conversation.json](../e2e/m1/tui-conversation.json): installed Core, real TUI, duplicate input and restart.
- [conversation-history-order.json](../e2e/m1/conversation-history-order.json): queue, steering and final-only output, with equal live/restored row order and identities.
- [tui-interrupted-reply.json](../e2e/m1/tui-interrupted-reply.json): Esc, client reopen and two Core restart modes; one provider call until the explicit follow-up.
- [interrupted-partial-history.json](../e2e/m1/interrupted-partial-history.json): forced interruption while streaming and failed tool arguments; partials remain display-only and the next explicit input makes the only subsequent model call.

These scenarios build and install Core in isolated temporary state and use a
scripted HTTP provider. They establish admission, execution, persistence and
surface behavior, not live-provider quality. This work ports no new Hermes,
Honcho or LCM-X behavior. Historical test results do not replace the required
foundation check for the candidate commit.

## Dependencies

At verification, dependencies #38 and #55 were closed. Parent issue #56 remained
open and is recorded as `Depends on #56` in the pull request. Its ledger and
preparation implementation is already used by the tests above; closing that
separate parent issue is outside #59's two sub-issues.
