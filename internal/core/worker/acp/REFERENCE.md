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
