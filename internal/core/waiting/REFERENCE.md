# internal/core/waiting implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-55 waiting migration

Canon migration 34 installs the already-defined waiting decision journal in the application catalog.

### Projection reads without the writer

`Build` serves `waiting_get` and the actions on a waiting row. It rereads every
candidate in canon's writer and calls `Source.Done` for a finished candidate.
`BuildProjection` serves the progress and overview projections: it runs the same
sources in a read transaction and skips `Done`, because a publish follows every
commit and must not queue behind the writer that holds those commits. A finished
candidate is absent from the list either way, so its cleanup waits for the next
`Build` — the read that a person's waiting list or an answer performs.
