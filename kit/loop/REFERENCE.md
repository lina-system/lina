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
A call may name another call's write target as its gate and waits for that call, whatever its position in the response; gated calls with different targets still run in parallel.
Results commit as each call ends and enter model context in call order.
Cancellation does not replace a started call's returned result: a committed success or unknown receipt remains success or unknown in the turn journal.
Only calls that never started receive a synthetic aborted result.
Steering is drained at request boundaries, before the context transform.
Every return, including errors, atomically closes steering admission and returns pending input in `Outcome.FollowUps`.
An input accepted after the final drain is included in that handoff; `Inputs.Push` returns false after closure so the lane retains ownership and routes it to the next turn.
Each run uses its own input queue.

### Terminal text-call recovery

`GuardTextCalls` accepts Core's deterministic detector and wraps a stream function.
It holds possible marker fragments (including single-byte prefixes) and completed
assistant messages until a clean terminal boundary. It releases normal text
verbatim; native call events and runtime bridge calls preempt recovery. Opaque
provider items remain intact on normal turns; a defective response's reasoning
is withheld along with its assistant text so a fresh stream can restart item
indices without retaining contaminated state. A recovered call becomes a canonical function-call
item with a fresh ID and travels through `Run`'s existing journal and execution.

An unusable call opens one fresh request through the same stream function with a
bounded task/receipt summary, reserving separate space for recent task/steering
inputs and receipts so tool output cannot displace the current request. Server-session, reasoning and stale cache-mark
positions are cleared. The first stream is closed before the next opens. A second
unusable call returns `ErrTextCall`; cancellation and transport errors never
trigger this continuation. Nested installation preserves one recovery allowance.
Reported usage is combined across attempts, with unknown values kept unknown.

The Core parser adapter is `internal/core/textcall.Guard`. Turn and run entry
points install it for injected stream functions; `modelpath.Config.Stream` also
covers exchanges, replay and auxiliary consumers outside the loop. Focused parser
stream tests exercise fragments, false positives, normal hooks, native runtime
calls and the retry bound. The M4 running-Core scenarios cover conversation and
background-run dispatch, history, progress suppression and terminal failure.
