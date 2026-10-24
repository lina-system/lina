# kit/provider/messages implementation notes

## Implementation notes

Design authority: [Runtime](../../../docs/design/runtime.md#claude) and the contracts it cites.
These notes retain implementation and validation history from the cited increment;
they do not establish current acceptance or change the contracts.

### Claude API-key increment (#882)

`kit/provider/messages` serves one Claude API-key account over the Messages API
with `net/http` and a small SSE reader. `ReadConfig` reads
`providers.claude_api_key` from the build manifest through the
transport-independent `messages/config` package; an absent required field, an
unsupported field, another adapter identity or a nonzero retry count is refused,
and `Request.Fields` may narrow that list but never widen it. The retry count is
0 and is recorded in the release manifest.

Core's items are mapped one way: a conversation message is one text block, a
reasoning item is a thinking block with its signature (or a redacted block), a
`function_call` is `tool_use` and a tool result is `tool_result`. A developer or
context message is sent as user text, because Messages has only two roles. A
subscription token, a custom tool declaration, a `custom_tool_call` item and an
item type this route cannot map are refused as not accepted before sending.
`max_tokens` comes from the manifest row; a reasoning effort becomes an explicit
thinking budget, and an effort outside low, medium and high is refused rather
than silently ignored.

Cache marks are placed at Core's selected boundaries: the last content block
derived from the boundary's item carries `cache_control` with the requested
lifetime, and a boundary with no items marks its last tool declaration. A mark
outside the layout, a fifth mark, or a lifetime other than 1 hour or 5 minutes is
refused before the request starts. Usage keeps the reported plain input, cache
reads and cache writes, with writes split by the reported lifetime buckets.

A background job may defer through Message Batches when its context deadline lies
beyond the batch window (24 hours) and the route admits `batch`. The adapter
submits one request under a fixed custom id, reads the status until the batch
ends, and replays a succeeded message as the same item, tool-call and terminal
sequence a stream produces. A refusal, an unknown state, a failure, an expiry or
a cancelled batch reports no batch and the caller runs exactly one normal
request, including when the initial batch submission itself fails. A
needs-sign-in refusal and a canceled call are returned instead, because a
second request cannot fix them. The batch id reaches the caller through
`provider.Meta.Batch`.

### Verification

Adapter tests use scripted wires: thinking and signature round trip inside a
tool loop, marks at the selected boundaries, refusal precedence, an absent key
and a refused key with no second request, batch serving and expiry fallback,
models, quota, budget accounting and the request-shape refusals. The installed
Core scenario lives in `e2e/m2/claude_api_key_test.go`: a Messages turn with a
tool loop, a signed-out Core that sends nothing, and `lina provider-replay` over
the public fixture reporting cache share and first-token time for this route.

This increment does not claim live entitlement, cache savings or first-token
improvement on a real account.

### Reference spec for the cache-mark placement

Read-only sources: `~/.hermes/hermes-agent/agent/prompt_caching.py`,
`agent/anthropic_message_convert.py` and `agent/prompt_cache_boundary.py`. No
Honcho, LCM-X or CRW behaviour is ported here.

- **Seen.** Hermes places four `cache_control` breakpoints that share one TTL:
  the static system prefix, the end of the system prompt, and the last two or
  three messages. The marker goes on the last content part of a message; an
  empty message cannot carry one, and a top-level marker on a tool envelope is
  dropped or refused. The marker is `{"type":"ephemeral"}` for five minutes and
  `{"type":"ephemeral","ttl":"1h"}` for one hour. A blank text block is refused
  by the API ("text content blocks must contain non-whitespace text"), so a
  marker on a dropped block is relocated. Tool ids are restricted to
  `[a-zA-Z0-9_-]`. Both `thinking` and `redacted_thinking` blocks exist.
- **Inferred.** LINA's boundaries are Core-selected exclusive item positions, so
  this adapter marks the block derived from each boundary instead of a rolling
  window of recent messages. Refusing an unplaceable mark before sending keeps
  the recorded capability honest.
- **Unknown.** Whether a real account retains a 1-hour mark on LINA's exact
  layout, and how a mark interacts with a batch result, are unmeasured on this
  route; the provider replay carries the before-and-after that would show it.
