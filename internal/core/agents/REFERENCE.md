# internal/core/agents implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-23 growth migration order

The published catalog through version 19 retains its SQL and order; growth appends at 20 and job/budget tables at 21.

### Throwaway-root durability option

`Open` takes options, and `NoSync()` is the only one. Core opens the store without options, so every immutable file and directory sync goes through `os.File.Sync`. Tests that open a private root they discard pass `NoSync()` and keep the same write, link and recovery path while the file sync becomes a no-op. `durability_test.go` checks both: the default store surfaces an injected sync failure from `Create`, and the throwaway store reads back its revision without calling the sync hook.
