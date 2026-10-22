# Claude runtime boundary

Reference: `@anthropic-ai/claude-agent-sdk` 0.3.308, published `sdk.d.ts`
and `README.md`, inspected before implementing the adapter.

- **Seen:** `query` accepts a system prompt, explicit model/effort, an abort
  controller, `tools: []`, SDK MCP servers, `settingSources: []`, plugins,
  `persistSession: false` and partial messages. Init reports tools, MCP servers
  (including source), and plugins. Result reports usage, session id and estimated
  cost; rate-limit events carry the vendor's structured limit information.
- **Inferred:** one fresh query per LINA start, an empty temporary cwd, explicit
  settings and environment, plus init validation can isolate the runtime surface.
  MCP handlers must await a correlated Core `tool_result`; no handler executes
  a tool locally. The SDK's prompt API cannot reproduce arbitrary native message
  history, so LINA's ordered items are supplied as a JSON conversation transcript
  in the user prompt. This is the AgentRuntime fidelity boundary in C-45.
- **Unknown:** live subscription entitlement, login discovery and quota-report
  availability are not established by a stub. Automatic account installation,
  pooling and idle shutdown belong to #168. The SDK and its platform runtime are
  installed by the person from this lockfile, never redistributed by LINA.
