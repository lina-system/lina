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

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-29 session integration

#132 connects session start, input, interrupt and snapshot to Core's normal conversation tool registry.
Launch receipts record the installed version and control handle; version and terminal-output notices survive in conversation history.
The per-session process belongs to Core, and hard activity is held per active turn.
Session completion is not task delivery; full work dispatch, recovery, approvals and dynamic work tools remain their separate roadmap steps.

### C-29 work tools and approvals

The #133 running-Core bridge declares the four `lina` dynamic work tools, routes worker approvals through authenticated authority cards carrying the originating conversation, claims each answer before its single send, and exposes measured/unknown quota rows through `worker_quota`.
Request withdrawal and terminal turns stop pending decisions.
Tool results and completion receipts remain worker evidence, not verification verdicts.
The headless `e2e/m2/codex-work-tools.json` scenario covers the stdio and conversation protocol round trip.

### C-59 live sessions

#766 adds `worker.Live`: the registry of worker sessions Core keeps after their
turn. A session records the Core handle, worker and version, its start time and
its thread, and is ended by joining the process without closing the thread or
session record. Ended sessions stay readable in the registry, and the durable
record remains the work ledger's thread handle.
Ending is idempotent; a failed join is retried by the next event or sweep.
The registry drains the adopted adapter's events, so an idle process cannot
block on a full channel. That drain ends the adopted entry, not whatever
session later reuses its handle. Bulk ending forwards the caller's context, so
a canceled join stays retryable.
