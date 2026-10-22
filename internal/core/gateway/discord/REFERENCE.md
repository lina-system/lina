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
