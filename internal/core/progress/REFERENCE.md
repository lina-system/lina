# internal/core/progress implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-55 progress digest increment

#159 implements `progress_digest_get` through the same current-source overview.
`progress.Digest` keeps waiting rows first and emits at most five single-line display rows without bars or navigation identities, plus total and omitted counts and a progress link.
The requested waiting order is computed by Core before the limit.
