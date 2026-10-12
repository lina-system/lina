# Host protocol

This contract is the LINA host contract: the one envelope that every message between LINA Core and another party uses, the JSON Schema that defines it, how parties declare and negotiate protocol and capability versions, which combinations are supported, the conformance fixtures that prove an implementation, the effect states that a message can report, and the payloads of the client and Node connections. It is normative: implementations follow it, and any change to it is made in this file and joins `dev` under [integration.md](../policy/integration.md).

## Scope

This contract covers two connections:

| Connection | Parties | Transport |
| --- | --- | --- |
| `client` | The TUI and the LINA app on every platform, with LINA Core | A stream: the LINA Core local socket, or the tailnet for a remote client |
| `node` | Node, with LINA Core | A stream: local, or over the tailnet |

It does not cover the protocols of workers, such as the Codex app-server protocol that the Codex worker adapter uses ([work-and-delegation.md](work-and-delegation.md)), the MCP protocol of plugins ([integrations.md](integrations.md)), the model wire to opencodex ([runtime.md](runtime.md)), or user-facing rendering ([surfaces.md](surfaces.md)). Product boundaries, people and agents are in [product-families.md](product-families.md). Grants, epochs, receipts and the main's command outcomes are in [main-authority.md](main-authority.md). This contract proves nothing about an implementation. Runtime proof belongs to the implementation issues that consume it.

## Schema and generated types

JSON Schema, draft 2020-12 ([json-schema.org](https://json-schema.org/specification)), is the canonical source of the protocol. The schema defines the envelope, every payload kind and every closed code list. This document explains the schema; when the two disagree about a field, the schema is the record and this document is corrected in the same change.

Rules:

- The schema lives in `protocol/schema/` of the LINA repository and is versioned per connection, as described under "Versions and capabilities".
- Every schema file declares draft 2020-12 with `$schema`. No other dialect is used.
- Types are generated from the schema for each language that uses it: Go for LINA Core, the TUI and Node, and Dart for the LINA app. Generated types are never edited by hand. The foundation verification regenerates them and fails on any difference ([foundation.md](../policy/foundation.md)).
- The Go types are part of the LINA kit ([runtime.md](runtime.md)). LINA Core, the TUI and Node use them through the kit.
- The Dart types serve the LINA app on every platform, because the app is one Flutter codebase ([surfaces.md](surfaces.md)).
- Every generated type set parses and re-serializes every conformance fixture without loss.

## Envelope

Every message on every connection is one envelope. An envelope is a JSON object with exactly the fields below. Every field is present; a field that does not apply holds an explicit null where the rule allows it. A sender never adds a field that the negotiated protocol version does not define. The envelope carries no agent field: which agent a message concerns is part of its payload (see Client connection and Node connection).

| Field | Type | Rule |
| --- | --- | --- |
| `protocol` | object | `{ name, version }`. `name` is `client` or `node`. `version` is the negotiated `MAJOR.MINOR`. |
| `kind` | string | The payload kind, from the closed list of the negotiated version |
| `message_id` | string | Opaque logical id, unique per sender. A resend of the same message keeps it. |
| `attempt_id` | string | Opaque id of this delivery attempt |
| `reply_to` | string or null | The `message_id` this message answers, or null |
| `sender` | object | `{ party, id }`. `party` is `core`, `client` or `node`. `id` is the main id, the client device id or the node id. |
| `epoch` | string | The main epoch ([main-authority.md](main-authority.md)) |
| `sent_at` | string | [RFC 3339](https://www.rfc-editor.org/rfc/rfc3339) timestamp in UTC |
| `idempotency_key` | string or null | On a message that requests an effect, the key built from the task, the action, the normalized target and the intent; null otherwise |
| `effect_state` | string | One value from "Effect states" |
| `receipt_ref` | object or null | `{ kind, id, revision }`, where `kind` is `receipt`. Names the receipt that states an `applied` effect, when this message is not that statement itself; null otherwise. |
| `source_refs` | array | Source references the payload relies on; empty when there are none |
| `error` | object or null | `{ code, detail }` when the message refuses or reports a failure; null otherwise |
| `payload` | object | Defined by the schema for `kind` at `protocol.version` |

A source reference is `{ origin, namespace, id, revision, locator, observed_at, observed_by }`. `revision` is a content revision, a hash or a commit. `observed_by` is the node id that observed it, or null. A file reference from a Node carries the provenance fields of [main-authority.md](main-authority.md) in this shape.

Error codes form a closed list in the schema. Every protocol shares these codes: `unsupported_combination`, `capability_not_negotiated`, `malformed`, `stale_epoch`, `stale_revision`, `not_authorized`, `out_of_scope`, `duplicate` and `unavailable`. `detail` is text for logs. No party parses it to make a decision.

Framing: envelopes travel in [JSON-RPC 2.0](https://www.jsonrpc.org/specification) messages, one per line. A request or notification carries the envelope in `params`, a response carries it in `result`, and a JSON-RPC error carries a typed error.

## Delivery

- A sender resends a message only when the receiver's refusal before delivery is proven. A resend keeps `message_id` and the payload identical and uses a new `attempt_id`.
- A message that is in flight, a delivery whose result is uncertain and a lost acknowledgement are never grounds to resend a message that requests an effect. The sender asks for the outcome instead.
- A receiver deduplicates by `message_id` and answers a repeated message with its first answer.
- An executor that receives a second request with the same `idempotency_key` never runs it again. It returns the first result, or `unknown`.
- Transport acceptance, arrival at the peer, application and verification are separate facts. None of them implies the next.
- A late message that the receiver does not expect (an earlier epoch, a revoked grant, a message already settled) is refused and never applied.

## Effect states

`effect_state` reports what happened to the effect a message reports on:

| Value | Meaning | What the receiver does |
| --- | --- | --- |
| `none` | The message reports no effect: a request, a query, a display update or a declaration. | Nothing beyond the message itself |
| `refused` | The actor refused before any effect: authorization, scope, capability or version. | Records it. Never counts it as success. |
| `failed` | The action failed, and the actor states that no effect occurred. | Records and reports it. Never counts it as success. |
| `applied` | The effect occurred. A receipt states it. | Verifies the statement, then records it |
| `cancelled` | The actor confirmed that it stopped before the effect. | Records it |
| `unknown` | The effect may or may not have occurred. | Records it as unknown. Never re-runs it automatically, and never falls back to another path. It stays unknown until reconciliation or an explicit decision by the task owner settles it ([main-authority.md](main-authority.md)). |

A failure after an effect may have started is `unknown`, not `failed`. A reported completion, an exit code or a model's final text never makes a state `applied` by itself.

## Versions and capabilities

Each connection has its own protocol version, `MAJOR.MINOR`. A minor version only adds optional fields, kinds, codes or capabilities. Any other difference is a new major version. The schema of each version includes the envelope.

A capability is a named, versioned feature of a connection, such as answering approvals on `client` or relaying a worker on `node`. A party may use a capability only when both sides declare it and the supported row lists it. The same JSON shape never implies the same capability.

Each connection negotiates with a handshake:

1. The connecting party sends `hello` with its party, id, product version, the protocol versions it speaks and the capabilities it declares.
2. LINA Core selects the highest protocol version that both sides speak and that the supported-combination table lists for the peer's product version. It answers `welcome` with the selected version, the granted capabilities, the main id and the current epoch. On `client`, `welcome` also carries the person id the connection acts for and the main's agents, each with its agent id, its name and the preset it was created from, or none for an agent created from a description.
3. When no listed combination exists, LINA Core answers with `unsupported_combination` and closes the connection. It reports the refusal to the user and records it as an event ([self-diagnosis-log.md](self-diagnosis-log.md)).

No other message is accepted before `welcome`. After the handshake, a message that needs a capability the handshake did not grant is refused with `capability_not_negotiated`, and the connection continues.

## Supported combinations

LINA Core ships the table of combinations it supports in the manifest of its build ([runtime.md](runtime.md)). Each row has these columns:

| Column | Meaning |
| --- | --- |
| Connection | `client` or `node` |
| Peer | The component or app |
| Peer product version | The exact product version of the peer: its release tag, or `dev-<commit>` for a development build |
| Platform | The OS and architecture the peer runs on |
| LINA Core product version | The exact product version of LINA Core, in the same form |
| Protocol version | The `MAJOR.MINOR` the row covers |
| Capabilities | The capabilities the row allows |
| Evidence | The conformance, mixed-version and failure-injection runs that accepted the row, as recorded by the foundation verification ([foundation.md](../policy/foundation.md)) |

Rules:

- The table of every build lists the components built together with it from the same commit, with that commit's conformance fixture runs as their evidence. So the TUI and LINA Core of one build find their row, and a development build can run the acceptance of a platform or app that is not accepted yet.
- A release build contains only the platforms and apps that have passed their acceptance ([runtime.md](runtime.md), [surfaces.md](surfaces.md)). Others are built only as development builds, so a release table never lists an unaccepted platform or app. Rows for the LINA app, Node and LINA Core inside LINA OS also need the failure injections below before they enter a release.
- Any other row is added only after its combination is tested and accepted. Nothing is listed by assumption.
- Every listed combination has a passing test. Every unlisted combination is refused cleanly, and the refusal is reported to the user.
- A row never widens to cover another version. Another version needs its own row and its own evidence.

In a release manifest, a row for a LINA component (the LINA app, Node, or LINA Core inside LINA OS) is accepted only after the failure injections of [runtime.md](runtime.md) pass for that composition.

## Conformance fixtures

LINA keeps conformance fixtures for every protocol version in `protocol/fixtures/` of the LINA repository, versioned with the schema and shipped in each LINA release tag. A fixture holds input envelopes, the expected output envelopes and the expected recorded outcome.

Every protocol version has fixtures for at least:

- the normal flow of each kind
- refused, failed and unknown outcomes
- cancelled outcomes
- a peer at an older and at a newer version than the one under test
- on `client`: a conversation of an agent that does not exist or was deleted, and an agent management request on a deleted agent
- on `node`: a stale epoch, a reboot with a new node boot id and a lost receipt

Every implementation of a side of a connection passes the fixtures of every version it declares: LINA Core, the TUI, the LINA app on each platform and Node. The foundation verification runs them ([foundation.md](../policy/foundation.md)). A failing fixture blocks the supported row. Clients are tested in isolation against a fake LINA Core built from the same fixtures.

## Client connection

The TUI and the LINA app on every platform talk to LINA Core over the `client` connection. A local client uses the LINA Core local socket, and a remote client reaches LINA Core directly inside the tailnet ([runtime.md](runtime.md)). Clients use only the public event and control API.

Every client connection acts for one person ([product-families.md](product-families.md)). LINA Core settles the person during the handshake and names the person id in `welcome`. Every later message on the connection is that person's: a message, an answer to a question or an approval, a cancel, a sign-in relay, an agent management request. A LINA admits only its owner, so a client on the local socket or inside the tailnet acts for the owner.

One connection reaches every agent of the main. A conversation kind, and a question asked in a conversation, names the conversation by its conversation id, and the conversation's participating agents follow from it. A request that starts a conversation names its participating agent by agent id. Every history item and every conversation event carries the agent id of the agent that spoke or acted, next to the person id where a person acted ([product-families.md](product-families.md)).

| Capability | What the client can do |
| --- | --- |
| Conversation | Start a conversation with an agent, send a message, interrupt, read history, subscribe to events, receive connect and disconnect notices |
| Agent management | List the main's agents, create an agent from a preset or a description, rename, tune and delete an agent. LINA Core records each change with the person id. |
| Task | Answer a task's questions, receive task status events, cancel a task |
| Approval | Answer approval requests with the choices of their tier: once, always allow this tool, deny or block ([main-authority.md](main-authority.md)) |
| Sign-in relay | Open a sign-in URL from LINA Core in the system browser, and return the redirect's `code`, `state` and `iss`, or the URL the person pasted ([integrations.md](integrations.md)) |
| Multi-task control | Control several tasks at once |

Version 1.0 of `client` has at least these kinds: `hello` and `welcome`; starting a conversation; sending input; interrupting; reading history; subscribing to events; the stream events, which are an output delta, an item done and the turn state; an approval request and its answer; a question and its answer; a readiness failure that names the preparation step that failed ([conversation-and-memory.md](conversation-and-memory.md)); agent management requests and their results; and an error. The Approval and Agent management capabilities are part of version 1.0. Their payload fields are set in the schema (see Deferred).

Rules:

- A client never writes canon. Every client message is a request that LINA Core accepts or refuses. What a client displays comes from LINA Core events.
- After a reconnect, a client reads state from LINA Core again. It never replays its own display as truth.
- A client never reaches opencodex or a worker directly. Everything passes through LINA Core.
- A client relays a sign-in redirect as it received it and makes no decision about it.
- A protocol error, such as a broken line or an unknown kind, is connection status and log data. It never appears as conversation content.

## Node connection

Node connects to the main over the `node` connection, locally or inside the tailnet. Its `hello` carries the node id, the node boot id, the host OS and the product version. `welcome` carries the main id and the current epoch. The Node then enters RECONCILING, and the state machines of [main-authority.md](main-authority.md) govern everything that follows.

| Message family | Direction | Content |
| --- | --- | --- |
| Grant | main to Node | Issue and revoke grants, with every grant field, including the agent that asked for the work or none ([main-authority.md](main-authority.md)) |
| Command | main to Node | Dispatch a command under a grant, and request cancellation |
| Receipt | Node to main | What the Node did under a grant, with provenance for every artifact it produced or touched |
| Effect report | Node to main | The effect state of every unconfirmed command, sent on reconnect and on request |
| Heartbeat | both | Liveness. The interval is set during Node implementation acceptance ([main-authority.md](main-authority.md)). |
| Worker relay | Node to main, and back | Events and server requests (approvals, questions, dynamic tool calls) from a worker that the Node runs, and the answers ([work-and-delegation.md](work-and-delegation.md)) |
| Branch transfer | Node to main | A work branch as a Git bundle, with the head SHA stated in the receipt |

Rules:

- Every `node` message carries the epoch. A message from an earlier epoch is refused with `stale_epoch` by whichever side sees it first.
- LINA Core never connects to a worker on another device. The Node on that device owns the worker process and relays it.

## Deferred

- The JSON Schema to Dart type generator: chosen before the first LINA app work ([surfaces.md](surfaces.md)).
- Request timeouts on the `client` and `node` connections: set by measurement during implementation acceptance of each connection.
- The payload fields of the `client` 1.0 kinds: set in the schema by the implementation issue that ships the first conversation product.
