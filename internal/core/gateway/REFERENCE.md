# internal/core/gateway implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-27 channel turn integration

The channel worker now drains committed owner inbox rows into the existing turn lanes.
Private owner inputs use the shared main conversation and admission/steering coordinator; shared chats retain their separate conversation.
Admission never waits for a running turn or outbound HTTP call.
Consumed channel steering is marked durably before the next model request so restart cannot turn it into another turn.
Channel ledger events retain platform/chat provenance and binding scope; the same model, tool and background-run services execute the work.
A completed reply and a landed background artifact are projected once into the channel ledger and shaped for the existing durable outbound queue.
Restart finds the same inbox/turn/queue identities.
It cannot repeat a started model turn or an uncertain send.
Scoped runners retain the original inbound address and resolve its current effective binding before model dispatch and tool execution, including new thread overrides of a parent fallback.
Accepted steering retains that runner alongside its event and inbox identity; deferred follow-ups use the input's runner even when the preceding turn used the app or another channel scope (#506).
Background delivery projections keep `EntryBackgroundNotice` and `background_notice` provenance; queue validation recognizes those committed events without promoting them to agent history.
No channel stream publishes provisional deltas.
Controls expose permanent delivery failure as the binding's unreachable flag.

### C-27 gateway increment

The #97 package increment provides `gateway.Adapter`, `channels.Store` and `gateway.Queue`.
The shared canon catalog installs `channels.Schema` at version 17 after the agent and inbox tables.
The channel dispatch extends `agent_bindings` with a cascading foreign key, so agent trash removes dispatches in that same transaction and restore never revives them.
Binding changes record the owner, agent and scope.
The most specific topic binding wins; the delivery also retains the original topic when it uses its parent binding.

### C-27 admission mode status

This increment implements the default owner-only admission mode; the optional addressed external-participant mode remains for its consuming flow.
