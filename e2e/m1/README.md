# LINA 1 headless acceptance

Run `go test -count=1 ./e2e/m1/...`. The #34 harness, previously in the parent
package, lives beside its fixtures here. Foundation's `./e2e/...` package
selection runs it once; there is no recursive test runner. Builds are shared,
state roots are private, children are joined and temporary state is removed.
Existing persona-growth extensions still use this harness and `../m2/` fixtures.

This map follows the nine acceptance checks and failure injections in
[LINA 1](../../docs/design/go-roadmap.md#lina-1). External providers and Telegram
are synthetic wires; the production stores, operations, Core and TUI are real.
Store-boundary fault scenarios use the production services in the isolated
harness, because the product CLI deliberately exposes no fault-injection flags.
The person's remaining product assessment is visual layout, readability,
physical input and UX. Passing this suite alone does not declare the phase done.

| Acceptance | Headless observations |
| --- | --- |
| 1. Install, crash, corruption, doctor | `TestM1Install`; `TestM1StartupCrashRecovery` SIGKILLs all 13 acknowledged startup boundaries and starts installed Core at a higher epoch with the same identity; `TestM1Doctor`; `TestM1CanonFaultAdmissionAndDoctor` checks refused receipts, rollback, recoverable FULL/write errors and corruption's durable write halt. |
| 2. No account, sign-in, streaming, immutable owner | `TestM1TUIConversationInstalledCore`, `TestM1TUIAccountsInstalledCore`, `TestM1ChatGPTSignInOwnerBinding`: unconfigured preparation, sign-in controls, recorded issuer responses, streamed/persisted conversation and second-account owner binding. |
| 3. Reply card, hard budget, boundary notice | `TestM1ReplyShapeLONGQA` and final-item/large-frame cases check sentence cuts and retained ledger data; `TestM1TurnBudgetHandoffInstalledCore` checks gathered state, one run, running UI and no notice inside an active turn; `TestM1RunLandingNotice` and `TestM1RunQuestionNotice` check durable landing. |
| 4. Forget, restore, Korean, exact expansion | `TestM1MemoryCorrectionForgetRestore` backs up a readable summary before correcting/forgetting its source, restores the old generation into a different root and checks unavailable summaries, empty forgotten recall and absent provider context with a retained-memory positive control. `TestM1KoreanMemoryByID` runs every query in the Korean corpus through installed Core's memory protocol. `TestM1PreparedSummaries` runs the actual worker, checks three depths and exact raw fact bytes. `TestM1MemoryJobs` checks sourced conclusions and excludes automated/replayed person attribution. |
| 5. Skill learning and undo | `TestM1SkillsInstalledCore` checks instruction-only adoption, its notice, next-turn use and one-action undo, and refuses executable-code adoption. |
| 6. Hermes import | `TestM1ImportReconciliation`, `TestM1ImportRehearsalPromotion` and `TestM1StagedImportWorker` check table/row reconciliation, reverse hashes, unchanged source, repeat-import zero growth and promotion boundaries. |
| 7. Telegram and agent hand-off | `TestM1TUIChannelPairing` checks installed pairing/binding controls; `TestM1TelegramCommitCrash` kills the real adapter/gateway before and after the inbox/cursor transaction, reopens canon and replays the update without duplicate delivery; `TestM1TelegramOwnerButton` checks an outsider's no-op before the owner's authenticated button. `TestM1TUITalkLogInstalledCore` now runs a typed hand-off through the background supervisor, checks its cited answer and one mailbox log row, then displays and expands that row through installed Core and TUI. |
| 8. Sandbox, tool evidence, idle restart | `TestM1SandboxUnavailable`, `TestM1ClientAdmissionSandbox`, `TestM1ToolEvidenceCannotApprove`, `TestM1RestartWaitsForEffect` and `TestPlannedRestart`: probe/fail-closed causes, protected socket, planted tool-result text with a real authority gate, and protocol-visible restart held by a blocked effect until it finishes. |
| 9. Waiting, running, cards, unchanged output, spikes | `TestM1WaitingRunningNotices`, `TestM1TUICards`, `TestM1TUIApprovalsInstalledCore`, `TestM1TurnBudgetHandoffInstalledCore` and the TUI stream/recovery/resize fixtures cover ordering, counts and cards. Foundation also runs the recording-writer checks `TestUnchangedConnectionWritesNothing`, `TestConversationDoneReplacesStreamAndUnchangedWritesNothing` and `TestConversationMarkdownProfilesAndUnchangedOutput` in `internal/tui/ui`. They compare exact byte counts synchronously, without idle sleeps. Physical spike evidence remains the prerequisite below. |

Failure coverage also includes:

- `TestM1TurnBudgetHandoffInstalledCore` keeps the complete long task name
  visible across wrapped rows in the 120-column terminal's running panel.
  Its assertion reads that panel from one frame, excluding the conversation's
  input echo, before checking result delivery and restart recovery.

- `TestM2FileLanding`: repeated file reports and stale-snapshot refusals run
  through installed Core's tools until history compacts. The synthetic model
  answers the auxiliary summary request separately from work calls. Both partial
  and committed copy recovery retain a published summary and its source coverage
  through restart, then verify destination hashes and duplicate-free delivery.
  A manual request waits for its own durable turn and reports a failed or aborted
  turn immediately. This exercises [file delivery](../../docs/design/work-and-delegation.md#evidence-before-close)
  alongside [conversation compaction](../../docs/design/conversation-and-memory.md#compaction-and-resume).

- `TestM2ConversationWorkerRunningInstalledCore`: a fixture worker started by
  `worker_start` remains visible in Ctrl+R after the conversation turn ends,
  with its owner agent before Codex. Explicitly finishing the worker removes
  its row from the open pager without relying on a short execution duration.
- `TestM2ConversationWorkerProgressInstalledCore`: the same installed worker
  stays visible in the Ctrl+P [work panel](../../docs/design/tui.md#work-panel-and-stage-list-t-16)
  after its starting conversation ends. The open panel removes its card when
  the worker becomes idle, retains a failed card, and clears it after dismissal
  in Ctrl+R, following the shared [progress acknowledgement](../../docs/design/surfaces.md#progress-at-a-glance).

- `TestM1TurnCrashBoundaries`: acknowledged SIGKILL after claim, preparation,
  model item, tool intent, applied effect, tool receipt and completed turn;
  repeated recovery creates one interruption, retains committed results and
  never reclaims accepted input or repeats the effect.
- `TestM1BackgroundRunSIGKILL`: installed Core dies while a background provider
  request is held; restart advances the epoch, retains the run identity and
  consumes exactly one persisted retry while the foreground is never replayed.
- `TestM1ProviderFailureHistory`: unavailable provider, expired token, usage cap
  and truncated SSE, each with durable outcome/history and no automatic retry.
  Failed partials stay outside the next completed conversation context.
- `TestM1InterruptedPartialHistory`, `TestM1TUIInterruptedReplySurvivesCoreRestart`
  and `TestM1TUIStoppedNoticeOrder`: active-stream death, explicit stop,
  graceful/crash restart, identity/order retention and no accepted-input replay.
- `TestM1ClientListener`, `TestM1TopicStreams`, `TestM1TUIStream` and
  `TestM1TUIRecovery`: bounded slow consumers, dropped/replayed frames and resync.
- `TestM1BackupInstalledConversation` and `TestM1RestoreInstalledConversation`:
  whole generations, a different restore root, epoch fencing and schema upgrade.

## Reference observations before extending the memory checks

- **Seen:** [LCM-X compaction.py][lcm] selects eligible old raw chunks outside the
  protected fresh tail, preserves tool groups and references the raw store and
  summary DAG separately. [Honcho conclusions.py][honcho] retains observer,
  observed peer, reasoning level and source ids, and checks the peer pair for
  scoped lookup.
- **Inferred:** LINA acceptance should resolve the exact original event after
  multiple summary levels, retain a conclusion's citations, and learn a person's
  facts only from that person's admitted events. LINA's deletion, masking and
  holder contracts still govern the projection.
- **Unknown:** these modules and synthetic wires do not establish live model
  quality or every upstream privacy behavior. No Python implementation is copied
  or executed by this suite.

## External acceptance prerequisites

Depends on #88, #93, #96 and #110 for completion of their remaining phase work;
their already-landed paths are exercised here. This suite does not wait for or
claim to finish those issues.

Depends on #371 for the confirmed physical-terminal matrix and S1–S11 run
records. The current repository contains a measured headless S8 record, not a
complete physical-terminal set. [The spike runbook](../../evidence/tui-spike/runbook.md)
and its strict `spike.ReadRecord` validator govern those records. Missing records,
unconfirmed terminals and unmeasured outcomes must remain incomplete acceptance;
synthetic data cannot manufacture that evidence. Real IME and visual usability
remain UI/UX checks. The opt-in live-provider probe is likewise separate from
credential-free CI.

[lcm]: https://github.com/electricsheephq/lcm-x/blob/709099b3a397a7893a7abae8802ebe7bf605f79e/compaction.py
[honcho]: https://github.com/plastic-labs/honcho/blob/607438d20c408575d1c9257f325c4885da871e82/sdks/python/src/honcho/conclusions.py
