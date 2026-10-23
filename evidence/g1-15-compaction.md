# G1-15 compaction acceptance

Issue [#67](https://github.com/thisisjun786/lina/issues/67) was checked against
`dev` commit `689ff60e3d0218867b9ecc3e0209f1cd14fea1eb` on 2026-10-22.
Its sub-issues [#69](https://github.com/thisisjun786/lina/issues/69) and
[#70](https://github.com/thisisjun786/lina/issues/70) were both closed. All five
behavioral acceptance conditions already have implementations and passing
regressions on that revision; no additional runtime change is needed for this
parent issue.

The contracts are [C-18](../docs/design/go-core.md#records-and-memory) and
[compaction and resume](../docs/design/conversation-and-memory.md#compaction-and-resume).
The existing [reference specification](../internal/core/compaction/reference-spec.md)
records seen, inferred and unknown LCM-X behavior. This acceptance adds no port.

| Done condition | Executable evidence | What the assertions establish |
| --- | --- | --- |
| A failed summarizer publishes no node and retains the events in the request path. | `TestStoredSummaryFailurePublishesNothingAndPreservesRequest` in [builder_test.go](../internal/core/compaction/builder_test.go), `TestFailedSummaryKeepsEventsAndPublishesNothing` in [pass_test.go](../internal/core/compaction/pass_test.go), and `TestLoopTransformBlocksDispatchOnSummaryFailure` in [session_test.go](../internal/core/compaction/session_test.go). | Injected summary failure leaves all three summary tables empty, preserves history, returns no request and prevents the loop's provider dispatch. |
| A stale-frontier publish fails. | `TestPublicationRejectsStaleFrontierAndEpoch` in [store_test.go](../internal/core/compaction/store_test.go). | A ledger append, competing publication or epoch advance rejects the saved proposal without changing summary-table counts. |
| `expand` returns the exact range of a fact stated before compaction. | `TestPublishedNodeExpandsExactFactThroughTool` and `TestNodeExpansionPagesDescendantsInLedgerOrderAndRejectsOtherAgent` in [builder_test.go](../internal/core/compaction/builder_test.go). | A real published node expands through the agent-bound tool to the original event id and full body; descendant pages preserve ledger order and reject another agent. |
| A continuation that does not fit sends nothing and logs sizes. | `TestStoredContinuationOverThresholdRetainsLineageAndSizes` in [builder_test.go](../internal/core/compaction/builder_test.go), `TestOversizeContinuationRecordsSizesAndSendsNothing` in [pass_test.go](../internal/core/compaction/pass_test.go), and `TestContinuationLedgerSurvivesOversizeWithoutCopyingInput` in [continuation_ledger_test.go](../internal/core/compaction/continuation_ledger_test.go). | Oversized continuation dispatch is refused, the ended turn and input reference remain, and complete/per-part sizes match the durable ledger record without duplicating the input. |
| A retraction rebuilds the node. | `TestRetractionRebuildsLeafAndAncestorsAtomically`, `TestRebuildRejectsConcurrentAppendWithoutPartialWrites` and `TestRebuildFencesEpochAndCompetingRebuild` in [store_test.go](../internal/core/compaction/store_test.go). | Real retraction erases affected derived text; bottom-up rebuilding excludes the retracted event and uses rebuilt children. Failure or a frontier/epoch race publishes no partial replacement. |

Verification on the baseline:

```sh
go test ./internal/core/compaction/... ./internal/core/canon/... ./internal/core/exec/...
```

All three package suites passed. This combines the two commands named by #67
without running the shared compaction suite twice. The existing tests inject
failures and races; there is no new implementation fix requiring a red/green
cycle, and no test pins this document's wording.

These are package and loop-boundary acceptance results using real canon and
synthetic model responses. They do not establish live model quality or complete
conversation/TUI composition. As specified by C-18 and G1-15, those consuming
flows retain their own acceptance. The broader prerequisite issues #56 and #63
were still open when checked; this record does not claim to complete them.
