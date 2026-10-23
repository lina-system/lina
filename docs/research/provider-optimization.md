# Provider optimization: cache, reasoning state and batch

Research for [#850](https://github.com/thisisjun786/lina/issues/850). It measures which provider features cut cost or latency for LINA's request shape, on the providers the person connected, and what in LINA's requests blocks them today. It is evidence, not a contract. The rules it supports are in [runtime.md](../design/runtime.md#provider-capabilities) and in the request layout and compaction of [conversation-and-memory.md](../design/conversation-and-memory.md#request-layout).

## Result

- **Prompt caching is the only feature that cut billed input on every provider, and LINA's current requests lose most of it.** On a fixed replay of 7 to 9 requests of 20,000 to 36,000 tokens each:
  - ChatGPT: 5% of input came from the cache as LINA sends it today. With a stable conversation identity and the ContextPacket after the person's input, 84%.
  - Claude: without a cache mark nothing is cached. The current adapter shape caches only the instructions and tools, and writes the whole transcript to the cache again every turn: about 14,000 tokens a turn at the 1-hour write rate, measured through the pinned Agent SDK. Separate history blocks with a LINA mark cut that to about 2,000.
  - Gemini: 78% from implicit caching with LINA's layout, 39% when something that changes every turn comes first.
  - Grok: 58% to 81%. The first request of a turn often missed; a stable cache key helped on some turns, but the runs were noisy.
- **Three request rules decide most of the result, and Core can apply all of them without a model.**
  1. Nothing that changes every turn goes before the history. With the ContextPacket first, Gemini cached nothing across turns, and every provider lost the history.
  2. The ContextPacket goes after the person's input, not before it. ChatGPT's route writes its cache at the end of the latest user message. A packet before the input is gone in the next turn, and the cached history goes with it. Moving it raised ChatGPT's cross-turn hits from 55% to 93%. No provider got worse.
  3. Each conversation keeps one stable cache identity where the route takes one. On ChatGPT's route a repeated request went from 0% to 97% cached.
- **Claude needs marks that LINA places.** This holds on the Messages API and through the Agent SDK, which keeps a cache mark that LINA puts on a prompt content block.
- **A compaction summary can reuse the conversation's cached prefix.** On Claude it sent 72 uncached tokens instead of 11,870. On Gemini and Grok it sent 11% to 20% fewer uncached tokens. On ChatGPT it read only the instructions and tools, because the turn before it had the packet ahead of the input. With the packet after the input, earlier input ends become cache entries the summary can reuse; that was not measured. Changing `tool_choice` lost the cache on Gemini and Grok.
- **Retention.** Claude's 5-minute marks had expired at 6 minutes. Its 1-hour marks hit at 6 minutes and missed at 20 and 65 (one sample each, see Limits). ChatGPT's route reports 24 hours by default and refuses the retention field; with a session identity, a prefix still hit after 65 minutes. Gemini and Grok hit at 6 minutes and missed at 20.
- **First token.** Cached Claude requests of 23,000 tokens started after a median of 1.1 s, against 2.7 s uncached. On ChatGPT, Gemini and Grok the first text waited on reasoning, and the cache made no reliable difference in five pairs.
- **No server state or batch helps on the plan routes measured.** ChatGPT's route requires `store: false` and refuses `previous_response_id` and the flex tier. Its compaction endpoint returned one opaque item: 33 s, and the next request was 11,927 input tokens instead of 18,454. xAI keeps stored responses, but refuses `instructions` together with `previous_response_id`. No plan route has a batch endpoint. Batch is documented for API keys only: 50% off at OpenAI, Anthropic and Google, 20% at xAI.
- **Risk outside these measurements.** Anthropic's Agent SDK overview says: "Unless previously approved, Anthropic does not allow third party developers to offer claude.ai login or rate limits for their products, including agents built on the Claude Agent SDK." LINA's Claude provider uses the person's subscription through that SDK. The owner kept that route and asked for a separate Claude API-key route.

## Method

- One Linux x86-64 host, 2026-10-23. All four providers were reached through the person's own model gateway, which holds the person's connected accounts. It serves the ChatGPT plan through Codex's backend Responses route, Claude through the Messages API, and Gemini and Grok through an OpenAI-compatible Responses surface. LINA's own sign-in routes were not available on this host, so these are the person's accounts on neighbouring routes, not LINA's routes.
- What the gateway changes, as observed:
  - On the ChatGPT route it replaces `prompt_cache_key` with its own value, and routes by a `session_id` header.
  - It adds a fixed preamble: about 35 tokens on Claude, 550 on Gemini and 1,150 on Grok, which xAI caches across all requests.
  - It has no native Gemini endpoint, so Gemini's explicit context cache and native request shape could not be measured.
- Models: `gpt-6-astra` at effort low, `claude-sonnet-5-5`, `gemini-3.8-flash`, and `grok-4.7` (reported as `grok-4.7-build`). Claude Agent SDK 0.3.308, the adapter's lockfile pin.
- The request shape is LINA's own:
  - the instruction text of `assemble.Instructions` with the public Lina persona;
  - LINA's 45 conversation tool declarations;
  - a 40-turn synthetic history, made of short questions answered with paragraphs of the public [runtime.md](../design/runtime.md);
  - a 1,400-token ContextPacket that changes every turn and never enters the history;
  - four live turns, two of which run a tool loop.
  No private conversation text was sent.
- Arms. Each arm is one fresh conversation, so one arm's cache cannot help another.
  - **today**: the request as LINA sends it now.
  - **layout**: the current request layout, with a stable conversation identity where the route takes one.
  - **packet-first**: the ContextPacket before the history.
  - **packet-after**: the ContextPacket after the person's input.
  - **keep-packet**: earlier turns' packets stay in the request, so it only grows at its end.
  - **drop-reasoning**: earlier turns' reasoning items left out.
  - **cache-key** and **conv-id**: xAI's two routing hints.
  - **adapter-today**, **native** and **none** (Claude):
    - adapter-today: one prompt holding the whole JSON transcript, marked the way the runtime marks it;
    - native: native messages with LINA's marks;
    - none: native messages with no mark.
- Other runs:
  - Retention: one fresh prefix per provider and gap, written once and read once after 1, 6, 20 and 65 minutes.
  - First token: five cold and warm pairs per provider.
  - Compaction summary: two repetitions per provider.
  - Probes for server-state and batch fields.
- The throwaway scripts and raw results stay off the repository. Every number is the usage the provider reported:
  - Responses usage: `input_tokens` and `cached_tokens`.
  - Messages usage: `input_tokens`, `cache_read_input_tokens` and `cache_creation_input_tokens`.
  "Plain" below means input billed at the ordinary rate. No cost is computed from a price list. xAI reports its own per-request cost, which is quoted as reported.

The repeatable implementation is now the
[public provider replay fixture](../../testdata/provider-replay/README.md), run
through the installed `lina provider-replay` command and running Core. Preserve
the same fixture digest for a before/after comparison. Its versioned report
keeps unknown counters as null, splits reported cache writes by lifetime, and
measures the first output at the adapter stream. The loopback acceptance test
checks these reports without a real account; real-account runs require explicit
opt-in. This implementation adds no new provider measurements to the historical
results below. Native runtime requests and counters hidden by an adapter cannot
be inferred from a stream total.

## Measurements

### Replay

Hit shares are the cached share of each request's input. The cross-turn column is the first request of turns 2 to 4. The in-turn column is the follow-up requests after tool results. The token columns total the arm's requests.

| Provider | Arm | Cross-turn hit | In-turn hit | Input | Read | Written | Plain |
| --- | --- | --- | --- | --- | --- | --- | --- |
| ChatGPT | today | 0, 0, 18% | 0, 18, 0% | 138,638 | 7,168 | 0 | 131,470 |
| ChatGPT | layout | 55, 54, 54% | 98 to 99% (one miss) | 179,175 | 106,112 | 0 | 73,063 |
| ChatGPT | packet-first | 55, 54, 54% | 98 to 99% | 159,166 | 111,104 | 0 | 48,062 |
| ChatGPT | drop-reasoning | 55, 55, 55% | 98 to 99% | 138,591 | 91,392 | 0 | 47,199 |
| ChatGPT | keep-packet | 94, 94, 94% | 99% | 149,430 | 125,440 | 0 | 23,990 |
| ChatGPT | packet-after | 92, 93, 94% | 98 to 99% | 158,870 | 134,144 | 0 | 24,726 |
| Claude | none | 0, 0, 0% | 0% | 251,108 | 0 | 0 | 251,108 |
| Claude | adapter-today | 58, 56, 56%; the rest written | 98 to 99% | 276,244 | 185,855 | 90,367 | 22 |
| Claude | native | 95, 95, 96% | 95% | 251,237 | 205,171 | 40,063 | 6,003 |
| Gemini | packet-first | 0, 0, 0% | 91% | 163,230 | 63,699 | 0 | 99,531 |
| Gemini | layout | 91, 91, 91% | 91% | 163,267 | 127,627 | 0 | 35,640 |
| Gemini | keep-packet | 88, 89, 91% | 90 to 92% | 177,125 | 139,181 | 0 | 37,944 |
| Gemini | packet-after | 91, 91, 91% | 90 to 91% | 189,744 | 151,452 | 0 | 38,292 |
| Grok | packet-first | 4, 55, 4% | 97 to 99% | 213,397 | 123,392 | 0 | 90,005 |
| Grok | layout | 66, 88, 4% | 5 to 99% | 212,192 | 123,904 | 0 | 88,288 |
| Grok | conv-id | 4, 65, 93% | 97 to 99% | 186,468 | 124,288 | 0 | 62,180 |
| Grok | cache-key | 66, 89, 93% | 97 to 99% | 184,898 | 146,304 | 0 | 38,594 |
| Grok | keep-packet, with key | 4, 95, 95% | 97 to 99% | 224,363 | 168,960 | 0 | 55,403 |
| Grok | packet-after, with key | 65, 88, 93% | 97 to 99% | 213,442 | 173,312 | 0 | 40,130 |

- ChatGPT's cross-turn hit stopped at the instructions and tools (10,880 tokens) whenever the previous packet was removed. This fits OpenAI's documented implicit cache for GPT-5.6 and later: it writes at the end of the latest user message or final tool result, and looks up only at earlier message ends. The packet stood before that point, so the entry included it.
- At effort low the ChatGPT model reported 0 reasoning tokens in every request, so the drop-reasoning arm could not show an effect of reasoning items here.
- Claude's adapter-today shape reads the instructions and tools from the cache, and writes the transcript again each turn because the transcript is one growing block. Its plain count is low only because almost everything is written: a write is billed above the ordinary rate, at 1.25 times for 5 minutes and 2 times for 1 hour.
- Grok's per-request cost, as xAI reported it, totalled 850 million ticks for layout, 613 million for packet-after and 546 million for cache-key. Output length differs between arms, so these totals are a rough guide.

### Claude through the Agent SDK

The same three live turns, through `@anthropic-ai/claude-agent-sdk` 0.3.308 with the adapter's options: built-in tools off, LINA's tools over MCP, no settings and no persistence. With the TTL unset, the runtime wrote every mark with a 1-hour lifetime.

| Prompt shape | Turn | Read | Written |
| --- | --- | --- | --- |
| One JSON transcript (the adapter today) | 2 | 25,207 | 13,915 |
| One JSON transcript (the adapter today) | 3 | 25,207 | 14,047 |
| One text block per earlier turn, LINA mark on the last one, then packet and input | 2 | 37,069 | 1,987 |
| One text block per earlier turn, LINA mark on the last one, then packet and input | 3 | 37,219 | 2,000 |

The runtime kept the mark that LINA set on the content block of its `SDKUserMessage`. The SDK also warned that its `allowedTools` entries bypass `canUseTool`. Every LINA tool call still returns to Core, but the adapter should not rely on that callback.

### Retention

Each prefix is about 7,000 to 12,000 tokens: hit means cached on its single read, miss means not.

| Provider and setting | 1 min | 6 min | 20 min | 65 min |
| --- | --- | --- | --- | --- |
| Claude, 5-minute mark | hit | miss | miss | miss |
| Claude, 1-hour mark | hit | hit | miss | miss |
| ChatGPT, no identity | miss | miss | miss | miss |
| ChatGPT, session identity | hit | hit | hit | hit |
| Gemini | 74% | 74% | miss | miss |
| Grok | miss | hit | miss | miss |

ChatGPT's route echoed `prompt_cache_retention: "24h"` on every response, but refused the field when sent: "Unsupported parameter: prompt_cache_retention". The ChatGPT 65-minute prefix missed on its second write but hit on its read.

### First token

Five pairs per provider: one cold request, then the same request again. The Claude column is the time to the first stream event, since its first text block varied. The others are the time to the first visible text.

| Provider | Request size | Cold median (range) | Warm median (range) | Warm cache share |
| --- | --- | --- | --- | --- |
| Claude | 23,000 | 2,680 ms (1,366 to 6,485) | 1,107 ms (811 to 2,489) | 100% |
| ChatGPT | 14,700 | 2,422 ms (2,051 to 3,313) | 2,927 ms (2,809 to 2,958) | 98% |
| Gemini | 15,400 | 8,475 ms (5,036 to 17,130) | 6,125 ms (5,585 to 29,568) | 87% |
| Grok | 17,200 | 6,487 ms (4,797 to 11,251) | 7,802 ms (4,968 to 13,375) | 3 of 5 hit |

On the Claude retention prefix of about 12,000 tokens, hits started in 979 and 1,041 ms, and misses in 1,490 to 3,931 ms.

### Compaction summary

| Provider | Summary request | Input | Read | Written | Plain |
| --- | --- | --- | --- | --- | --- |
| Claude | separate (today's rule: no persona, no tools) | 11,870 | 0 | 0 | 11,870 |
| Claude | shared prefix, tools unchanged | 33,907 | 33,835 | 0 | 72 |
| Claude | shared prefix, `tool_choice: none` | 33,907 | 33,835 | 0 | 72 |
| ChatGPT | separate | 7,708 | 0 | 0 | 7,708 |
| ChatGPT | shared prefix, tools unchanged | 18,485 | 10,880 | 0 | 7,605 |
| Gemini | separate (first time) | 8,555 | 0 | 0 | 8,555 |
| Gemini | shared prefix, tools unchanged | 21,807 | 14,206 | 0 | 7,601 |
| Gemini | shared prefix, `tool_choice: none` | 21,358 | 0 | 0 | 21,358 |
| Grok | separate | 9,346 | 1,152 | 0 | 8,194 |
| Grok | shared prefix, tools unchanged | 24,072 | 17,536 | 0 | 6,536 |
| Grok | shared prefix, `tool_choice: none` | 24,093 | 1,152 | 0 | 22,941 |

The shared-prefix request carried the conversation's request unchanged through its history, then the summary instruction. On ChatGPT's route the shared request read only the instructions and tools, because the history's cache entry ended after that turn's packet and input. A second identical request read 99%. With the packet after the input, each turn writes an entry at the end of its input, so a summary request can reuse the history up to the last of those entries in its segment. That case was not measured.

### Server state and batch probes

| Route | Field or endpoint | Answer |
| --- | --- | --- |
| ChatGPT | `store: true` | 400, store must be false |
| ChatGPT | `previous_response_id` | 400, resend the full conversation |
| ChatGPT | `service_tier: "flex"` | 400, unsupported |
| ChatGPT | `prompt_cache_retention` | 400, unsupported; 24 hours is echoed by default |
| ChatGPT | `context_management` with a compaction threshold | 200, no visible effect on a short request |
| ChatGPT | `/responses/compact` on the 40-turn history | 200 after 33 s. It returned one opaque `compaction` item of 13.7 KB. The next request was 11,927 input tokens against 18,454 with the full history. |
| Grok | `store: true` | 200 |
| Grok | `previous_response_id` with `instructions` | 400, "instructions and previous_response_id together" not supported |
| Grok | chained `previous_response_id` without instructions | 200, 8,995 input tokens against 17,224 for the stored request |
| Gemini | `previous_response_id` | 400 |
| all | `/v1/batches`, `/v1/messages/batches`, `/v1/conversations` | 404 |

## What each provider needs

### ChatGPT

- The route caches by message ends. LINA needs the ContextPacket after the person's input, and the same conversation identity on every request of a conversation. On LINA's own route, the identity is `prompt_cache_key`. The Sign in with ChatGPT limitations page does not list it, so V1 must show that it is accepted. OpenAI says the key is not needed for caching on GPT-5.6 and later, but routing decided the result on this route.
- Retention follows the route's default, 24 hours here. The Sign in with ChatGPT surface lists `prompt_cache_retention` as unsupported. The newer `prompt_cache_options.ttl` and explicit breakpoints are documented for the API, not for that surface. LINA sends neither until V1 accepts them.
- Reasoning items return unchanged, as now.
- Server state is not available on the plan route. `previous_response_id` over HTTP is refused there, and `store` must be false. WebSocket mode with continuation inside one connection is documented for that surface, but was not measurable here. It is a later candidate for long tool loops.
- `/responses/compact` produces an opaque item that LINA cannot read, expand or check against retraction. It saves input on the next request, but the LINA summary does the same and stays readable. It is not used.
- No batch or flex tier on the plan route.

### Claude

- With no mark there is no cache. The Messages API needs LINA's marks at the end of the fixed part and the end of the history, plus one on the newest current-turn item for tool loops. That is at most four.
- Through the Agent SDK, the adapter sends each earlier turn as its own text block, with LINA's mark on the last history block, then the person's input and the packet. The measured run sent the packet first. After the mark, the order does not change the cache. The SDK keeps the mark, and one block per turn stays far inside the 20-block lookback.
- The runtime's TTL is one setting per query. Unset, it was 1 hour here, and the SDK documents 1 hour on a subscription and 5 minutes on an API key. On the Messages API each mark carries its own TTL.
- The SDK's alpha `sessionStore` could resume a native session from LINA's records. It needs session persistence on and an undocumented entry format, so it is not used.
- An API-key route over the Messages API gives LINA the full request, its own marks, and Message Batches for background jobs (50% off, up to 24 hours). The owner chose to add it as a separate route.

### Gemini

- Implicit caching works with LINA's layout. It needs the start of the history to stay fixed, and the tools and tool choice unchanged.
- Gemini 3.8 Flash and Gemini 4 Argon document a 4,096-token minimum for implicit caching. Every LINA request here is far above it.
- Explicit context caching (`cachedContents`) is not offered for Gemini 4 Argon and later. It charges storage per token-hour, and could not be measured here. It fits only a background job that reads the same large material many times on an older model.
- Thought signatures must return during function calling on Gemini 3 and 4. This is the existing provider-state rule.
- The Gemini account is an API key, so Batch (50% off, 24-hour target) and Flex (50% off, 1 to 15 minutes) are available for background jobs. Neither could be measured through the gateway.
- The Interactions API keeps state on Google's servers, for 55 days by default on paid keys. It is not used: it saves no billed input over caching, and keeps conversation content outside LINA.

### Grok

- xAI caches automatically. `x-grok-conv-id`, or `prompt_cache_key` on the Responses API, routes one conversation to one server. Within a turn the hit share was 97% to 99%. Across turns the first request was noisy, and a stable key raised it on some turns.
- LINA's Grok provider is the `grok` command over ACP, not the xAI API. The command chooses its own routing and caching. LINA controls only the prompt it sends: stable parts first, earlier turns in the same order, then the packet after the input.
- `previous_response_id` cannot be combined with instructions, so it cannot carry LINA's instructions on every request. It is not used.
- xAI's batch is 20% off for API keys. It is not relevant to the command route.

## Data format

- The request evidence gains one optional object that names the capabilities used. The usage record gains the cache writes split by lifetime.
- Both live in JSON bodies: the `request_assembly` ledger event and the budget row's usage JSON. Older readers ignore the new field, and older rows read as unknown.
- No table or column changes, so there is no backfill or contract step. An unreported value stays unknown, never zero.

## Sources

Fetched 2026-10-23. Most pages show no date or version; Gemini pages show "Last updated".

- OpenAI:
  - [Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching): implicit and explicit caching for GPT-5.6 and later, write and read rates, `prompt_cache_options.ttl`, the routing note on `prompt_cache_key`.
  - [Reasoning](https://developers.openai.com/api/docs/guides/reasoning) and [Conversation state](https://developers.openai.com/api/docs/guides/conversation-state).
  - [Compaction](https://developers.openai.com/api/docs/guides/compaction), [Batch](https://developers.openai.com/api/docs/guides/batch), [Flex processing](https://developers.openai.com/api/docs/guides/flex-processing) and [WebSocket mode](https://developers.openai.com/api/docs/guides/websocket-mode).
  - Sign in with ChatGPT [preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations): unsupported fields, `previous_response_id` and WebSocket continuation.
- Anthropic:
  - [Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching): four marks, 5-minute and 1-hour lifetimes, the 20-block lookback, minimum lengths, workspace isolation.
  - [Extended thinking](https://platform.claude.com/docs/en/build-with-claude/extended-thinking), [context editing](https://platform.claude.com/docs/en/build-with-claude/context-editing) and [batch processing](https://platform.claude.com/docs/en/build-with-claude/batch-processing).
  - Claude Code [prompt caching](https://code.claude.com/docs/en/prompt-caching): runtime TTL defaults and controls.
  - Agent SDK [overview](https://code.claude.com/docs/en/agent-sdk/overview): the login note. Agent SDK [session storage](https://code.claude.com/docs/en/agent-sdk/session-storage).
  - The published `sdk.d.ts` of `@anthropic-ai/claude-agent-sdk` 0.3.308: `promptCacheTtl`, `sessionStore` and `SDKUserMessage`.
- Google:
  - [Context caching](https://ai.google.dev/gemini-api/docs/caching), updated 2026-10-13.
  - [Thought signatures](https://ai.google.dev/gemini-api/docs/thought-signatures), [Batch API](https://ai.google.dev/gemini-api/docs/batch-api), [Flex inference](https://ai.google.dev/gemini-api/docs/flex-inference) and [Interactions](https://ai.google.dev/gemini-api/docs/interactions).
  - [Pricing](https://ai.google.dev/gemini-api/docs/pricing), updated 2026-10-22.
- xAI:
  - [Prompt caching](https://docs.x.ai/developers/advanced-api-usage/prompt-caching) and [maximizing cache hits](https://docs.x.ai/developers/advanced-api-usage/prompt-caching/maximizing-cache-hits).
  - [Batch API](https://docs.x.ai/developers/advanced-api-usage/batch-api), [Pricing](https://docs.x.ai/developers/pricing) and the Grok Build [CLI reference](https://docs.x.ai/build/cli/reference).

## Limits

- **Routes.** All runs went through one gateway on neighbouring routes, not through LINA's own:
  - ChatGPT ran on the Codex backend route, not the Sign in with ChatGPT API route.
  - Grok ran on the xAI API, not the `grok` command.
  - Gemini went through a translation layer.
  The gateway also chooses accounts. Claude caches are isolated per workspace, so a request served from another account misses. That alone could explain the 1-hour mark's miss at 20 minutes, and it cannot be separated here.
- **Sample size.** One replay per arm, five pairs per first-token measurement, one read per retention cell. Read differences under about 10 points as noise. Grok varied most.
- **Fixture.** The history is synthetic, and the replay is four live turns long. Long real conversations, compaction in the middle of a conversation, and image or file inputs were not run.
- **Not measured here:**
  - Gemini's explicit cache, batch and flex;
  - every API-key batch route;
  - Claude's API-key 5-minute default through the SDK;
  - OpenAI's WebSocket mode and explicit breakpoints;
  - the `grok` command's own caching;
  - V1 acceptance of any field on LINA's route.
- **Not evidence of more.** These numbers do not rank models or answer quality. They are not V1 or V4 evidence, and not a claim about any person's costs.
