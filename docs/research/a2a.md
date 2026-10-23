# A2A: publishing and calling remote agents

Research for [#756](https://github.com/thisisjun786/lina/issues/756), following
[worker protocol research](worker-protocols.md#a2a). Read on 2026-10-23.
This is source inspection, not an interoperability result.

## Seen

The reference is A2A **v1.0.1**, released 2026-05-28. Its
[specification](https://github.com/a2aproject/A2A/blob/v1.0.1/docs/specification.md)
defines behavior; its
[a2a.proto](https://github.com/a2aproject/A2A/blob/v1.0.1/specification/a2a.proto)
defines the messages and their ProtoJSON representation.

- Section 3.6 uses major.minor on the wire: `A2A-Version: 1.0` and
  `supportedInterfaces[].protocolVersion: "1.0"`. The patch version is a
  specification revision. An absent version means 0.3, not automatic 1.0.
- Sections 8 and 9 define Agent Card discovery at
  `/.well-known/agent-card.json`, HTTPS JSON-RPC and SSE. The 1.0 method names
  are `SendMessage`, `SendStreamingMessage`, `GetTask`, `ListTasks`,
  `CancelTask`, `SubscribeToTask` and `GetExtendedAgentCard`.
- The proto's Agent Card has `supportedInterfaces`, `securitySchemes`,
  `securityRequirements`, `defaultInputModes`, `defaultOutputModes` and
  `skills`. Each interface declares its binding, URL, version and optional
  tenant. A tenant is routing data, not authentication. A skill describes an
  ability; the SendMessage request has no standard skill selector.
- The proto's `SendMessageResponse` wraps either `task` or `message`.
  `StreamResponse` wraps one of `task`, `message`, `statusUpdate` or
  `artifactUpdate`. The JSON enum values include `TASK_STATE_WORKING` and
  `TASK_STATE_COMPLETED`; old examples with `working` are not the 1.0 shape.
- Section 3.3.1 makes send deduplication optional. `messageId` is not a
  portable exactly-once guarantee. Reads and cancellation are idempotent.
- Sections 3.1 and 3.4 distinguish terminal states from `INPUT_REQUIRED` and
  `AUTH_REQUIRED`. A terminal task cannot accept another message.
  `SubscribeToTask` starts with a current task snapshot; a broken stream
  does not mean the task stopped.
- Section 3.2.2 and the proto's `returnImmediately` default require waiting
  for a terminal or interrupted task when false. Section 3.1.1 also says
  send returns immediately. The explicit configuration semantics are the
  implementable rule; a client can send `returnImmediately: true` to avoid
  relying on that prose inconsistency.
- Section 13.1 requires authorization on every task operation, including
  unfiltered lists. Inaccessible and nonexistent task ids are indistinguishable.
  Authentication mechanisms describe how to identify a caller, not the work
  the caller may authorize inside LINA.
- The 1.0.1 prose still contains some 0.3 examples and a reference to
  `AgentCard.security`; the proto names the 1.0 field `securityRequirements`.
  Implementers must use the versioned message definitions, not copy those
  older examples.

## Inferred for LINA

- A remote agent needs its own admission boundary. Neither its Agent Card nor
  a successful login can stand in for the owner's grant or approval.
- A remote task id, a local task id and a Node command id serve different
  purposes. Core must persist their association before delivering results.
- A remote `COMPLETED` status reports what that server claims. It cannot
  become a Node receipt or satisfy LINA's done condition without evidence.
- Lost acknowledgments need reconciliation. Repeating a send or assigning
  the same work to another agent can duplicate an outward effect.
- A2A transports requests and results. Local coding workers remain on their
  existing worker connections; exposing a persona must reach Core's actual
  tools, work dispatch and result handling.

## Unknown and implementation evidence

No LINA A2A server, client or Node relay was run for this design. External
consumers are not enumerable: the inspection covered this repository and the
public protocol sources, not deployment logs or another organization's clients.
This change introduces no existing A2A consumer migration or sunset.

The official [Go SDK](https://github.com/a2aproject/a2a-go) advertises
[v2.7.0](https://github.com/a2aproject/a2a-go/releases/tag/v2.7.0), released
2026-10-21. Its behavior, dependency admission and compatibility with Core's
Go toolchain remain unmeasured. An implementation must pin and verify an SDK
before adding it; this design adds no dependency.

Implementation acceptance must use a running Core and controlled HTTP peers,
with real tool or worker effects and durable result reads. It must cover both
directions, authentication, caller isolation, grant revocation, restart, lost
acknowledgment, interrupted streams and uncertain cancellation. Synthetic
credentials and fixtures establish those boundaries without claiming that a
live third-party service or a deployed endpoint was tested.

## Implementation issues

The [integration contract](../design/integrations.md#a2a) is implemented by
separate M3 issues, each with one running-Core acceptance condition:

- [#800: publish a LINA agent](https://github.com/thisisjun786/lina/issues/800).
- [#801: call remote agents](https://github.com/thisisjun786/lina/issues/801).
- [#802: relay granted Node calls](https://github.com/thisisjun786/lina/issues/802),
  after #801 and the Node grant/reconciliation work named there.
