# External Claude adapter

This is LINA's TypeScript program, run by the person's Node.js 24.20.0 or newer.
The SDK is a runtime download, never linked into Go or included in LINA binary
artifacts. The source release carries this program and its independent npm v3
lockfile. Install in a private directory outside the LINA artifact:

```sh
npm ci --ignore-scripts
npm audit signatures
npm test
npm start
```

The pinned SDK and all platform packages come from the public registry with
SHA-512 integrity. No lifecycle script runs. Repository verification copies the
adapter to an isolated temporary installation and runs these same checks.

Core accepts an explicit route through `LINA_CLAUDE_NODE` (absolute Node path),
`LINA_CLAUDE_ADAPTER` (absolute `main.ts` path) and `LINA_CLAUDE_MODEL`. It refuses
partial configuration and simultaneous Responses/Claude routes. A subscription
token, if supplied, uses `CLAUDE_CODE_OAUTH_TOKEN`; otherwise the SDK may use the
command's own OAuth login. It never loads the command's settings, so a gateway
login there (an `apiKeyHelper` or `ANTHROPIC_BASE_URL`) is not used: a gateway is
a Claude API-key route with a confirmed base URL and key. Core passes an
allowlisted environment. Neither competing credentials nor Node injection
variables reach the child. Automatic
account discovery, verified first-use installation, quota projection and the
account-scoped idle process manager remain #168's work. This initial explicit
route starts a fresh adapter for each request and joins it when the stream closes.

## C-45 wire version 1

UTF-8 JSON lines on stdin/stdout, at most 8 MiB per input frame. Every command
has a string `id` (1–128 ASCII letters, digits, `_ . : -`). Turn ids are unique
for the process lifetime. At most two starts are active in one adapter process.
A message with an unknown type, id or tool result is refused with `error`.
Diagnostics never include SDK exception text. Stdout contains protocol only.

| Core command | Fields beyond `type` and `id` |
| --- | --- |
| `start` | `system` text, ordered `items`, `tools` with unique `name`, optional `description` and object JSON Schema `parameters`, exact `model`, optional `effort` |
| `tool_result` | `call_id`, `result` containing MCP `content` and optional `isError` |
| `cancel` | none; cancels just this turn, including waiting tool handlers |
| `ping` | none; responds `pong` with the same id without starting a runtime |

| Adapter event | Fields beyond `type` and turn `id` |
| --- | --- |
| `init` | reported `tools`, `mcp_servers` and `plugins`; precedes all model output |
| `delta` | visible `text` only |
| `item_done` | completed SDK assistant `item`; provisional until successful terminal |
| `tool_call` | unique `call_id`, unprefixed LINA `name`, JSON `arguments` |
| `usage` | `usage` with reported `input`, `output`, `cached`, `cache_write`; absent counts remain absent |
| `limit` | vendor's structured `limit` event; no invented quota |
| `terminal` | `reason`: `completed`, `failed` or `cancelled`; reported `session_id` and optional USD `cost_estimate` in millionths |
| `error` | stable `code`; malformed uncorrelatable input has a null id |

The adapter disables built-in tools, settings sources, plugins and session
persistence, and replaces the system prompt. LINA items travel as one ordered
JSON conversation transcript in the SDK user prompt, not native reconstructed
vendor messages. Runtime compaction remains opaque. Every MCP handler awaits a
Core result; it has no local executor. Both sides refuse an init advertising
unlisted tools, non-LINA/non-SDK MCP servers or plugins. The TypeScript side
also refuses loaded skills and agents. A failed or truncated runtime cannot
complete a turn. EOF cancels and joins all active queries and removes their
empty temporary working directories.

`npm test` exercises the wire against scripted runtimes and imports the actual
SDK to check MCP registration without a model call. The headless Core scenario
is [claude-adapter.json](../../e2e/m2/claude-adapter.json). It executes a real Core
tool via this adapter and verifies the reply survives Core restart. These tests
do not establish live subscription entitlement or vendor quota availability.
See [REFERENCE.md](REFERENCE.md) for observed and inferred SDK behavior.
