# kit/loop implementation notes

## Implementation notes

Design authority: [Go Core](../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-08 loop increment

The #55 increment implements `kit/loop.Run` over `provider.Stream`.
It waits for a terminal event and a clean stream end before releasing any call, and reports `incomplete_response` for a cut-off call so a caller can request complete arguments without an automatic retry.
Completed items are committed verbatim as they arrive; aborted visible deltas are stored separately and excluded from model context.
Validation and before hooks finish in call order before execution.
Independent calls use at most eight slots; normalized write targets form ordered dependency chains.
Results commit as each call ends and enter model context in call order.
Cancellation does not replace a started call's returned result: a committed success or unknown receipt remains success or unknown in the turn journal.
Only calls that never started receive a synthetic aborted result.
Steering is drained at request boundaries, before the context transform.
Every return, including errors, atomically closes steering admission and returns pending input in `Outcome.FollowUps`.
An input accepted after the final drain is included in that handoff; `Inputs.Push` returns false after closure so the lane retains ownership and routes it to the next turn.
Each run uses its own input queue.
