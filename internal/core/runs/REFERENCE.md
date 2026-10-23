# internal/core/runs implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-55 display integration status

The display rows and notices are Core APIs in this increment; client 1.0 waiting/running payloads remain reserved until their surface implementation.
