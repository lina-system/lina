# ACP lifecycle reference observations

Read before implementation for issue #747. Design authority is
[the ACP adapter contract](../../../../docs/design/work-and-delegation.md#acp-adapter)
and C-29/C-60 in [Go Core](../../../../docs/design/go-core.md#work-engine).

- **Seen:** The shared client from #746 negotiates version 1, owns stdio children,
  separates replay updates and returns typed prompt stop reasons. CRW commit
  `821d6453aacfc9053b17c3b24a134c0ce4077f80`,
  `internal/bridge/appserver/bus.go`, correlates notifications by thread and
  explicitly signals lost observations. LINA's existing adapter boundary also
  binds the turn, process generation and binding epoch.
- **Inferred:** Each ACP prompt needs a Core turn identity. Streamed progress must
  precede the finish event; a missing prompt reply is a connection failure.
  Cancellation completes only on the prompt's `cancelled` reply. The existing
  dispatch verifier, rather than worker prose or process exit, derives receipts
  from observed Git and pull-request evidence.
- **Unknown:** Synthetic lifecycle tests do not verify installed vendor versions,
  permission routing, work-tool MCP, recovery, steering or vendor quota sources.
  Those integrations have separate issues. No Hermes or Honcho code is copied.

## Cursor readiness (#210)

- **Seen:** [The protocol measurements](../../../../docs/research/worker-protocols.md)
  record Cursor CLI 2026.10.22-27dcf5f answering ACP v1 without an agent version,
  and rejecting `session/new` with -32000 without sign-in. The built-in profile
  already runs `cursor-agent acp` and probes `cursor-agent --version`.
- **Inferred:** That explicit authentication refusal is a failed readiness
  check before any prompt. Close the partial client before returning a typed
  sign-in failure; do not expose the agent's error message or data. Other
  handshake failures do not prove that another worker can safely take over.
- **Unknown:** A live signed-in Cursor round trip requires the person's account
  and remains opt-in. Synthetic tests do not verify a vendor release. This
  change implements the existing ACP contract, not a CRW or Hermes port.

## Implementation notes

Design authority: [Go Core](../../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-60 worker profiles

`internal/core/worker/acp` implements C-29 on that client.
A profile holds the worker name, the command and arguments, the environment variables it sets (such as `CLAUDE_CODE_EXECUTABLE`), the version probe and the vendor rate-limit key it may read.
Claude Code, Cursor, Gemini CLI and Grok are built-in profiles; person-added profiles come from an editable settings file defined by their own issue.
