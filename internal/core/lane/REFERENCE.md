# internal/core/lane implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-09 steering increment

The #61 increment adds `lane.Manager.SubmitLoop`, `Steer` and `Stop`.
Each submitted loop receives a fresh steering queue.
Admission commits a message before offering it to the active turn; an accepted message enters the next request boundary.
A rejected push remains owned by admission and becomes a follow-up.
Stop carries the supplied `turn.Interrupt` cause through the active turn and its tools, refuses further steering to that turn, and leaves queued turns intact.
The lane stays occupied until the active work returns.
Preparation and claim failures close the input queue too and return pending input to the caller.
