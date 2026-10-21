# Scripted fake Core

`New(fixtures, clock, steps)` loads the client 1.0 corpus supplied as an `fs.FS`
and validates the script before I/O. `Run(ctx, conn)` serves one accepted stream
and closes it on every exit. The caller creates the listener, accepts clients,
joins each run and removes its temporary resources. No product process imports
this peer.

A step names an optional request fixture and zero or more response fixtures.
The request's kind and payload must match; envelope ids and time may differ.
Hello's device id and diagnostic product version may also differ, but the device
id must agree with its sender. The first emission answers the request, preserving
its JSON-RPC id and correlating `reply_to` to its message id. Subsequent emissions,
or all emissions without a request, are notifications. Fixtures remain unchanged.

Supply a clock with `Now()` and a cancellable `Wait(ctx, duration)`. Tests may
advance time immediately or hold a wait behind a channel until explicitly
released. There is no default wall clock, timer or sleep. Reset the clock to
replay the same bytes. The clock's synchronization belongs to its caller.

`Step.Faults` supports:

| Switch | Effect |
| --- | --- |
| `Drop` | Consume the step but suppress its output. |
| `SlowReader` | Wait on the injected clock before consuming the request. JSON durations are nanoseconds. |
| `Epoch` | Change the current epoch for this and later steps, including the welcome payload. |
| `Duplicate` | Send each envelope twice with identical logical data and different attempt ids. |
| `DisconnectAfter` | Close after this many delivered frames in the step, including duplicates; zero closes before output. Return `ErrDisconnected`. |
| `Recovering` | Replace mutation responses with `unavailable`; hello and read-only queries still use their fixtures. |
| `Refusal` | Replace the response with a schema-valid `refused` error code. |

Faults reset at each step except `Epoch`. The order is wait, request, epoch,
explicit refusal (otherwise recovering), drop, duplicate and disconnect. A
disconnect count beyond the frames delivered by that step never triggers.
Message ids are assigned in script order even to dropped frames. Each run starts
the sequence again; distinct connections must not treat these synthetic ids as
globally unique. The script models a peer, not a database or effect executor. It
does not implement automatic reconnect, request deduplication or capability
authorization. Reserved payload fixtures still prove only their current shape.

The runnable example is [the M1 scenario](../../e2e/m1/fakecore.json), driven by
[the headless harness](../../e2e/fakecore_test.go). Run:

```sh
go test ./internal/fakecore/...
go test ./e2e/...
node scripts/foundation.mjs
```
