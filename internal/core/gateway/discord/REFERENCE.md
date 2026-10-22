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
