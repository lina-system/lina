# Successive compaction with carried values

Evidence for [#1111](https://github.com/lina-system/lina/issues/1111). The contract is in [Compaction and resume](../design/conversation-and-memory.md#compaction-and-resume).

## Decision

Keep the single-parent root. Carry its specific values byte for byte while the model updates other sections and extracts values from new events. At 24 KiB of UTF-8 values, request one condensation targeting 12 KiB; reject an oversized or failed result. A demanded root-only pass can condense earlier. This reduces repeated rewriting of identifiers without changing coverage or introducing overlapping parents. It trades a larger request for retained facts and does not promise lossless model summaries.

The replay supports this bounded change: depth 2 retained 41/59 checklist entries instead of 11/59, and depth 3 retained 35/93 instead of 16/93. It is not full LCM-X parity: the system profile at depth 3 still trails the reference, 15/46 against 24/46. The 24 KiB limit was chosen before the run; it was not tuned against the checklist.

## Method

The baseline is the completed [#849 run](memory-parity.md), at `5735b5a9`, prompt `compaction-v1`. The candidate uses the implementation from `193a5527`, prompt `compaction-v2-carried-values`. Both use `claude-sonnet-5-5` through the same evaluation-only Claude adapter and gateway route. The baseline results are reused, not a fresh paired model run. The adapter workarounds described in #849 remain outside this change.

The two original chains reaching depth 3 were replayed through the real Builder, Store, Auxiliary and expansion path: 41 leaf updates for dev and 32 for system. Checkpoint depth is LCM-X depth; LINA still has a chain of single-parent roots. The range plan and frozen literal-match checklists are byte-identical to #849. The saved baseline and candidate conversations also have identical raw event bodies, sensitivity spans and speaker kinds. Scoring is unchanged: Unicode NFC, lowercase and collapsed whitespace, with any fixed literal alias counting as retained.

Only counts are published. Source history, match strings, summaries and operational state stay private. Input and implementation digests and the full numeric results are in [the measurement artifact](../../evidence/compaction-carried-values.json).

## Retention and request size

Characters and bytes measure the summary message content that replaces the covered range in a request, including its fixed reference note. They are not token estimates. The live Core separately measures the complete request, including tools and protected context.

| Profile | Depth | Messages | Facts before | Facts after | LCM-X facts | Summary characters before → after | UTF-8 bytes before → after |
| --- | --- | --- | --- | --- | --- | --- | --- |
| dev | 2 | 373 | 7/31 | 20/31 | 15/31 | 4,445 → 21,661 | 7,707 → 22,089 |
| dev | 3 | 1,574 | 3/47 | 20/47 | 15/47 | 4,302 → 19,362 | 4,390 → 21,196 |
| system | 2 | 258 | 4/28 | 21/28 | 21/28 | 4,681 → 18,983 | 4,727 → 19,031 |
| system | 3 | 1,104 | 13/46 | 15/46 | 24/46 | 12,432 → 8,702 | 12,632 → 8,816 |

## Model work

Counts are cumulative through each checkpoint; do not add earlier checkpoints to later ones. Input tokens include the provider’s reported uncached input, cache reads and cache writes. Monetary cost was not reported and remains null in the artifact.

| Profile | Depth | Requests before → after | Input tokens before → after | Output tokens before → after | Model seconds before → after |
| --- | --- | --- | --- | --- | --- |
| dev | 2 | 15 → 16 | 374,827 → 449,034 | 60,453 → 58,837 | 415.5 → 453.7 |
| dev | 3 | 42 → 45 | 1,325,333 → 1,592,020 | 149,529 → 170,825 | 1,118.8 → 1,319.5 |
| system | 2 | 16 → 16 | 220,914 → 265,589 | 40,503 → 22,378 | 330.7 → 184.9 |
| system | 3 | 32 → 36 | 836,542 → 1,089,079 | 85,916 → 83,856 | 705.3 → 663.1 |

The candidate made 81 requests for 73 leaf updates: six additional condensation calls and two retries after rejected summary attempts. The system chain had one invalid summary and one secret-pattern rejection; neither was published. The baseline made 74 requests, including one failed attempt. Failed work is included in the reported totals.

## Limits and runtime proof

- This is one owner’s history, one fixed checklist and one candidate run. Exact alias retention is not a semantic quality judgment or statistical confidence interval. Earlier and later checkpoints include some of the same facts.
- The final system condensation reduced its summary to 8,702 characters and retained only 15/46 facts. Bounded carry delays loss; it does not eliminate loss when condensation is required. Leaf condensation remains a possible later alternative.
- Both sides use the same imported content-only events. Tool-call arguments missing from that import are still unavailable to the summarizer, as #849 reports. The other two, shallow-only #849 chains were not rerun for this depth-2/3 decision.
- Expansion returned the exact covered event set in ledger order at all eight candidate checkpoints. Comparing expansion bodies to original external snapshots is affected by Core’s masking/recovery version; it is not used as a pure summarizer comparison.
- The headless installed-Core scenario passed 40 real protocol turns against a scripted HTTP provider that deliberately forgets earlier receipt values. Subsequent requests retained earlier and latest values, raw history remained in the ledger, and restart reused the published root. This establishes runtime wiring; the replay above establishes real-model retention.
- Focused regressions cover 40 successive summaries, exact UTF-8 bounds, bounded condensation, failed calls without a partial summary, correction order and a three-level retraction rebuild. The retention and retraction regressions fail against the previous implementations and pass after the change.
