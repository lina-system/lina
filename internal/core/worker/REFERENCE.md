# Worker boundary reference observations

Read before implementation, for issue #131. Design authority remains C-29 and
[Work and delegation](../../../docs/design/work-and-delegation.md#adapter-boundary).

- **Seen:** CRW, commit `7e65fcb82d426d228e18958d728fe63a0991ddd0`,
  `internal/bridge/appserver/receive.go` separates responses, notifications and
  server requests, retaining request IDs and thread/turn attribution.
  `bus.go` carries completion, item, usage, status and error notifications and
  explicitly signals lost observations so consumers reconcile instead of
  treating silence as success. `capabilities.go` separates implementation
  capabilities from version-scoped observations. Its schema fixture is 0.154.0.
- **Inferred:** LINA's common boundary needs typed progress, usage, completion,
  question, approval and tool-call payloads, correlated thread/turn identities,
  a snapshot operation, and capability declarations independent of version.
  A turn finish is not a verified product result. No CRW transport code is copied.
- **Unknown:** CRW observations do not establish behavior on Codex 0.163.0.
  Session execution and approval round trips remain #132 and #133. The supplied
  Hermes checkout was unavailable; no Hermes behavior is ported by this issue.

Codex 0.163.0's locally generated experimental JSON Schema is the authority for
wire types. The official [app-server documentation](https://developers.openai.com/codex/app-server/)
confirms version-specific generation and the initialize/initialized handshake;
it is not the source of wire constants. Schema generation verifies a type
baseline, not a runtime-tested worker version.
