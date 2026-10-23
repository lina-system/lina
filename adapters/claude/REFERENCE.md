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

## Implementation notes

Design authority: [Go Core](../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-45 runtime increment

#167 implements the external TypeScript program and the [version 1 JSON-line wire](README.md#c-45-wire-version-1), including correlated MCP tool results, cancellation, ping/pong, init refusal and reported usage.
The SDK is installed from the adapter lockfile with lifecycle scripts disabled; foundation verifies sources, integrity, exact pins, registry signatures and the stub suite in a disposable installation.
An explicitly configured Core route starts the program through `kit/proc`, routes runtime tool calls through the existing admission and durable call/result journal, and joins it on stream close.
The ledger records `AgentRuntime`; the ordered context travels as a JSON transcript in the SDK prompt, so native vendor request fidelity is not claimed.
`e2e/m2/claude-adapter.json` exercises the installed Core, a real Core tool, streamed reply and restart persistence with a stub vendor runtime.
Automatic account setup, first-use installation, quota projection and idle pooling remain #168; this increment's explicit route launches one process per request.
