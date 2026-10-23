# internal/core/waiting implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-55 waiting migration

Canon migration 34 installs the already-defined waiting decision journal in the application catalog.
