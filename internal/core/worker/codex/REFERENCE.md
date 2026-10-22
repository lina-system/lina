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
  tools and quota integration remain #133, and this adapter must not claim those
  capabilities before they are implemented.

No reference implementation code is copied or executed.

## Work tools, approvals and quota (#133)

- **Seen:** CRW commit `821d6453aacfc9053b17c3b24a134c0ce4077f80`,
  `internal/bridge/appserver/responder.go`, retains thread ownership and the
  method-specific response shape. Its flow responder only refuses requests;
  it cannot approve on a person's behalf. The vendored Codex 0.163.0 schema
  defines dynamic tool declarations, request ids, approval responses and account
  quota snapshots/updates.
- **Inferred:** LINA must keep pending requests bound to the original thread,
  turn and process, expose approvals through its existing person decision path,
  and return the chosen response without changing worker arguments. Dynamic
  tools need declared names and scoped Core handlers. Quota windows remain
  distinct; absent measurements are unknown rather than zero.
- **Unknown:** reference behavior does not prove live account quota or every
  installed Codex version. Synthetic stdio and headless Core scenarios establish
  integration; no Hermes, Honcho or LCM-X behavior is ported here.

Reference files are read only; no CRW commands or services are executed.
