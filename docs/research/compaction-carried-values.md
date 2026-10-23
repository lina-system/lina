# Successive compaction with carried values

Evidence for [#1111](https://github.com/lina-system/lina/issues/1111). The contract is in [Compaction and resume](../design/conversation-and-memory.md#compaction-and-resume).

## Decision

Keep the single-parent root. Carry its specific values byte for byte while the model updates other sections and extracts values from new events. At 24 KiB of UTF-8 values, request one condensation targeting 12 KiB; reject an oversized or failed result. A demanded root-only pass can condense earlier. This reduces repeated rewriting of identifiers without changing coverage or introducing overlapping parents. It trades a larger request for retained facts, and does not promise lossless model summaries.

## Measurement

The reference run is [#849, PR #1126](https://github.com/lina-system/lina/pull/1126), research commit `746f7e05`. Its fixed leaf boundaries and literal-match fact checklists are reused unchanged. Baseline prompt: `compaction-v1`. Candidate: `compaction-v2-carried-values`. Both use `claude-sonnet-5-5` through the same private evaluation route. Only counts are published; source history, match strings, summaries and operational state stay private.

The candidate run is pending. No semantic-retention improvement is claimed until it completes.

## Runtime validation

The headless installed-Core scenario repeatedly compacts a long conversation against a scripted provider that drops earlier receipt values on every reply. It checks that actual subsequent requests retain the earlier and latest values, raw history remains in the ledger, and restart reuses the published root. Focused tests cover 40 successive summaries, UTF-8 bounds, bounded condensation, failure without a partial summary, and retraction rebuilds. These establish mechanics, not real-model quality.
