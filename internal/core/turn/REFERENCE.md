# internal/core/turn implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-08 tool activity increment

LN-450 adds `Journal.Start` after durable call commit, queueing and final admission, immediately before tool execution.
Preflight refusals and calls aborted or denied while queued emit no activity.
A start observer error prevents execution, commits a synthetic aborted result and propagates.
`turn.Options.ToolActivity` observes that start and its matching finish only after the result commits, with call id, raw registered tool name, started flag, result code and monotonic duration; it carries no arguments or output.
Independent calls can notify concurrently.
Finish uses the loop's bounded cleanup context even after cancellation, and observer errors never rewrite receipts or suppress committed-result notifications or budget accounting.
Client wiring translates tool names with the trusted `tools.DisplayName` catalog; unknown names always become `Using tool`.
