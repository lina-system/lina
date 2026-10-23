# internal/core/compaction implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-18 inherited exclusion compatibility

For compatibility with previously published trees (#511), inherited exclusions are validated against the canonical ledger and consumed at their ledger positions, independently of their stored child order.
Earlier builders could assign an audit or other excluded event beyond a retained message to a child.
Those rows retain their ownership, reasons and links, but an inherited exclusion in the verbatim tail does not extend the new node's prefix or time bounds.
This recovery applies only to stored, canonically excluded events; actual child inputs and all newly proposed coverage must still follow the ordered prefix.
It supports root-only condensation and later extension without rewriting summaries or introducing a schema migration.
Invalid reasons, foreign events, unavailable children and interleaved inputs remain errors.

### Successive compaction values

`Auxiliary.Summarize` implements `compaction-v2-carried-values`. Its embedded
`summary.txt`, `values-delta.txt` and `values-condense.txt` prompts distinguish
ordinary updates from demanded condensation. The JSON text schema is unchanged;
existing roots can be extended without migration. A prepared result records the
new prompt version through the existing configuration fingerprint.

Normal updates preserve the old `specific_values` bytes and append the new
section. UTF-8 bytes bound growth at 24 KiB; at most one extra auxiliary call can
condense an oversized candidate. The complete request still goes through Pass's
measurement. Invalid output, an oversized second result or a failed stream
returns no summary. Publication still fences the root, ledger and revocation
epoch. Core's retraction rebuild passes only freshly rebuilt child text to this
same auxiliary path.

The installed-Core compaction scenario verifies repeated updates and restart
against a deliberately forgetful provider. Package tests cover the byte bound,
failed condensation, correction ordering and rebuild after retraction. Real-model
retention and request sizes are reported in
[the #1111 measurement](../../../docs/research/compaction-carried-values.md).
