# Discord behavioral reference

Read before implementation: public Hermes Agent commit
`944c3a434071f43e64b443d033f3eba30c64434c`,
[`plugins/platforms/discord/adapter.py`](https://github.com/NousResearch/hermes-agent/blob/944c3a434071f43e64b443d033f3eba30c64434c/plugins/platforms/discord/adapter.py)
and [`adapter_component_auth.py`](https://github.com/NousResearch/hermes-agent/blob/944c3a434071f43e64b443d033f3eba30c64434c/plugins/platforms/discord/adapter_component_auth.py).

- **Seen:** 2,000-character message limit, a cap on split messages, configurable
  allowed mentions, forbidden-send handling, and component authorization using
  numeric user IDs and a live authorization callback. Hermes also permits role
  and allow-all alternatives.
- **Inferred for LINA:** retain the platform limit and repeat authorization at
  button consumption. LINA's C-27 requires paired-owner identity, current binding,
  epoch and canonical presentation; role and allow-all alternatives are excluded.
  Core owns durable native-ID receipts and queue retry decisions. Paragraph
  splitting preserves source text; no upstream implementation is copied.
- **Unknown:** differences in an unavailable local installation, actual Discord
  account permissions and live service behavior. Synthetic Gateway/REST tests
  establish LINA's protocol and persistence behavior only.

## Non-resumable Gateway closes (#548)

- **Seen:** Discord's [Gateway close codes](https://docs.discord.com/developers/topics/opcodes-and-status-codes#gateway-gateway-close-event-codes)
  require a new session for 4007 (invalid sequence) and 4009 (session timed out).
  The current adapter preserves its durable cursor on all WebSocket read errors.
- **Inferred for LINA:** clear only that bot's session/sequence cursor before
  returning to Core's existing reconnect supervisor. The next connection must
  Identify. Keep native receipts, pairing and bindings so replay cannot repeat
  work. Other transport closes retain the cursor for Resume.
- **Unknown:** live Discord behavior and account-specific conditions. This is a
  Discord protocol correction, not a new Hermes/Honcho/LCM-X/CRW port; the local
  reference installations were unavailable. Synthetic WebSocket and running-Core
  tests establish the recovery behavior.

## Implementation notes

Design authority: [Go Core](../../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-27 Discord integration

#147 adds `gateway/discord` behind the same adapter boundary and boots one outbound Gateway session per saved Discord bot.
Its `coder/websocket` transport is pinned at v1.8.15; REST calls use the fixed Discord v10 origin without redirects or automatic effect retries.
The shared catalog appends Discord presentations and pending buttons plus consumed channel steering at migration 32, leaving migrations 1–31 unchanged.
The optional channel control `platform` field defaults to Telegram.
One Gateway session runs per bot token inside one Core's canon: `Listen` joins
that session, so a channel connection and a bot-mode messenger source on the
same token receive every event from one socket (#1079). The session identifies
with the union of its subscribers' intents and reconnects with a fresh Identify
when that union changes; a Resume keeps the intents it identified with.
A close 4014 (disallowed intents) removes only the subscribers that asked for
the refused privileged intent: each is told `ErrIntentsDenied`, so its source
reports text unavailable, and the session reconnects with the remaining
subscribers' intents so a channel worker on the same token keeps running. A
socket this hub interrupts for an intent change reconnects with the current
union instead of failing every subscriber.
A subscriber that cannot store a dispatch yet returns `channels.Retry`. Each
subscriber has its own delivery worker, so the socket's read loop and the other
subscribers keep running while that dispatch waits: later messages arrive and
heartbeats are answered. The worker replays the dispatch with a bounded backoff
and the shared checkpoint advances only after every subscriber accepts it, so a
held edit is neither acknowledged nor lost (#1079). A closing socket waits
briefly for the dispatches it already read and then lets the next session replay
the rest from the last accepted checkpoint.
Discord connection verifies the bot identity before replacing its credential or worker.
Gateway close 4007/4009 clears that bot's durable session/sequence cursor before the existing supervisor retries with Identify.
This also applies before Hello.
Other transport closes retain Resume state; native-event receipts and bindings are preserved across the reset (#548).
