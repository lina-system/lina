# Codex session reference observations

Read before implementation for #132. Wire authority is the generated 0.163.0
schema; design authority is C-29 and the Codex adapter contract.

- **Seen:** CRW commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
  `internal/bridge/appserver/{client,receive,bus}.go`, initializes once, separates
  replies from notifications and server requests, and signals lost observations
  for reconciliation. Its transport is WebSocket; this implementation uses stdio.
  The official [app-server documentation](https://developers.openai.com/codex/app-server/)
  describes initialize/initialized, thread/start, turn/start, expected-turn steering,
  thread/read and interrupted turn completion. Installed Codex reports 0.163.0.
- **Inferred:** LINA needs an owned child, correlated concurrent RPC replies,
  bounded framing, observable connection failure, and generation/epoch fencing.
  A canceled RPC context cannot establish that a turn stopped. User settings must
  stay omitted from the wire. A version check is evidence, never a version gate.
- **Unknown:** the supplied Hermes/plugin paths were unavailable. No Hermes or
  Honcho/LCM behavior is ported. CRW observations do not verify this Codex version;
  actual round-trip evidence must be recorded separately. Approvals, dynamic work
  tools remain #133, and this adapter must not claim those
  capabilities before they are implemented.

No reference implementation code is copied or executed.

For #208, the same official documentation and vendored schema establish
`account/rateLimits/read` and `account/rateLimits/updated`. Quota reads now
return observed windows, including reset times; absent windows remain unknown.
Updates coalesce into scheduler wake hints. Synthetic protocol tests establish
blocked quota and its wake, without a provider credential or a live quota claim.
