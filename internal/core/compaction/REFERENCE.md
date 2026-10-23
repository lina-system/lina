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
