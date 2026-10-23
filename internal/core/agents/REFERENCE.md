# internal/core/agents implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-23 growth migration order

The published catalog through version 19 retains its SQL and order; growth appends at 20 and job/budget tables at 21.
