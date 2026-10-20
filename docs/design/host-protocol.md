# Host protocol

This contract is the LINA host contract: the one envelope that every message between LINA Core and another party uses, the JSON Schema that defines it, how parties declare and negotiate protocol and capability versions, which combinations are supported, the conformance fixtures that prove an implementation, the effect states that a message can report, and the payloads of the client and Node connections. It is normative: implementations follow it, and any change to it is made in this file and joins `dev` under [integration.md](../policy/integration.md).

## Scope

This contract covers two connections:

| Connection | Parties | Transport |
| --- | --- | --- |
| `client` | The TUI and the LINA app on every platform, with LINA Core | A stream: the LINA Core local socket, or the tailnet for a remote client. A mobile app host may use a WebSocket on LINA Core's tailnet address (see Client connection). |
| `node` | Node, with LINA Core | A stream: local, or over the tailnet |

It does not cover the protocols of workers, such as the Codex app-server protocol that the Codex worker adapter uses ([work-and-delegation.md](work-and-delegation.md)), the MCP protocol of plugins and the platform protocols of external channels ([integrations.md](integrations.md)), the wires of the model providers ([runtime.md](runtime.md#model-path)), or user-facing rendering ([surfaces.md](surfaces.md)). Product boundaries, people and agents are in [product-families.md](product-families.md). Grants, epochs, receipts and the main's command outcomes are in [main-authority.md](main-authority.md). This contract proves nothing about an implementation. Runtime proof belongs to the implementation issues that consume it.

## Schema and generated types

JSON Schema, draft 2020-12 ([json-schema.org](https://json-schema.org/specification)), is the canonical source of the protocol. The schema defines the envelope, every payload kind and every closed code list. This document explains the schema; when the two disagree about a field, the schema is the record and this document is corrected in the same change.

Rules:

- The schema lives in `protocol/schema/` of the LINA repository and is versioned per connection, as described under "Versions and capabilities".
- Every schema file declares draft 2020-12 with `$schema`. No other dialect is used.
- Types are generated from the schema for each language that uses it: Go for LINA Core, the TUI and Node, and TypeScript for the LINA app. Generated types are never edited by hand. The foundation verification regenerates them and fails on any difference ([foundation.md](../policy/foundation.md)).
- The Go types are part of the LINA kit ([runtime.md](runtime.md)). LINA Core, the TUI and Node use them through the kit.
- The TypeScript types serve the LINA app on every platform, because the app is one React codebase ([surfaces.md](surfaces.md#app-front-end)).
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
| `epoch` | string | The main epoch ([main-authority.md](main-authority.md)). The epoch is an unsigned 64-bit integer written as a decimal string without leading zeros and compared numerically. |
| `sent_at` | string | [RFC 3339](https://www.rfc-editor.org/rfc/rfc3339) timestamp in UTC |
| `idempotency_key` | string or null | On a message that requests an effect, the key built from the task, the action, the normalized target and the intent; null otherwise. For an effect asked inside a conversation turn the task component is the turn id. |
| `effect_state` | string | One value from "Effect states" |
| `receipt_ref` | object or null | `{ kind, id, revision }`, where `kind` is `receipt`. Names the receipt that states an `applied` effect, when this message is not that statement itself; null otherwise. |
| `source_refs` | array | Source references the payload relies on; empty when there are none |
| `error` | object or null | `{ code, detail }` when the message refuses or reports a failure; null otherwise |
| `payload` | object | Defined by the schema for `kind` at `protocol.version` |

A source reference is `{ origin, namespace, id, revision, locator, observed_at, observed_by }`. `revision` is a content revision, a hash or a commit. `observed_by` is the node id that observed it, or null. A file reference from a Node carries the provenance fields of [main-authority.md](main-authority.md) in this shape.

Error codes form a closed list in the schema. Every protocol shares these codes: `unsupported_combination`, `capability_not_negotiated`, `malformed`, `stale_epoch`, `stale_revision`, `not_authorized`, `out_of_scope`, `duplicate` and `unavailable`. `detail` is text for logs. No party parses it to make a decision.

Framing: envelopes travel in [JSON-RPC 2.0](https://www.jsonrpc.org/specification) messages, one per line. A request or notification carries the envelope in `params`, a response carries it in `result`, and a JSON-RPC error carries a typed error. On a WebSocket transport, each JSON-RPC message is one text message and carries no line break.

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

1. The connecting party sends `hello` with its party, id, product version, the protocol versions it speaks and the capabilities it declares. The product version is recorded for diagnosis and never decides compatibility ([versioning.md](versioning.md)).
2. LINA Core selects the highest protocol version that both sides speak and that the supported-combination table lists for the peer. It answers `welcome` with the selected version, the granted capabilities, the main id and the current epoch. On `client`, `welcome` also carries the person id the connection acts for and the main's agents, each with its agent id, its name and the preset it was created from, or none for an agent created from a description.
3. When no listed combination exists, LINA Core answers with `unsupported_combination` and closes the connection. It reports the refusal to the user and records it as an event ([self-diagnosis-log.md](self-diagnosis-log.md)).

No other message is accepted before `welcome`. After the handshake, a message that needs a capability the handshake did not grant is refused with `capability_not_negotiated`, and the connection continues.

## Supported combinations

LINA Core ships the table of combinations it supports in the manifest of its build ([runtime.md](runtime.md)). Each row has these columns:

| Column | Meaning |
| --- | --- |
| Connection | `client` or `node` |
| Peer | The component or app |
| Platform | The OS and architecture the peer runs on |
| Protocol version | The `MAJOR.MINOR` the row covers |
| Capabilities | The capabilities the row allows |
| Evidence | The conformance, mixed-version and failure-injection runs that accepted the row, as recorded by the foundation verification ([foundation.md](../policy/foundation.md)) |

A row is keyed by these columns and names no product version: compatibility is decided by the protocol version and the capabilities, never by an app or product version ([versioning.md](versioning.md)).

Rules:

- The table of every build lists the components built together with it from the same commit, with that commit's conformance fixture runs as their evidence. So the TUI and LINA Core of one build find their row, and a development build can run the acceptance of a platform or app that is not accepted yet.
- A release build contains only the platforms and apps that have passed their acceptance ([runtime.md](runtime.md), [surfaces.md](surfaces.md)). Others are built only as development builds, so a release table never lists an unaccepted platform or app. Rows for the LINA app, Node and LINA Core inside LINA OS also need the failure injections below before they enter a release.
- Any other row is added only after its combination is tested and accepted. Nothing is listed by assumption.
- Every listed combination has a passing test. Every unlisted combination is refused cleanly, and the refusal is reported to the user.
- A row never widens to cover another protocol version. Another protocol version needs its own row and its own evidence.

In a release manifest, a row for a LINA component (the LINA app, Node, or LINA Core inside LINA OS) is accepted only after the failure injections of [runtime.md](runtime.md) pass for that composition.

## Conformance fixtures

LINA keeps conformance fixtures for every protocol version in `protocol/fixtures/` of the LINA repository, versioned with the schema and shipped in each LINA release tag. A fixture holds input envelopes, the expected output envelopes and the expected recorded outcome.

Every protocol version has fixtures for at least:

- the normal flow of each kind
- refused, failed and unknown outcomes
- cancelled outcomes
- a peer at an older and at a newer version than the one under test
- on `client`: a conversation of an agent that does not exist or was deleted, an agent management request on a deleted agent, and a channel binding or a scope change that names a deleted agent
- on `client`: a result of each type; a result of a type the client did not list; a client without the `results` capability; an edit and a save at a stale revision; a save sent twice; a result whose conversation was erased
- on `node`: a stale epoch, a reboot with a new node boot id and a lost receipt

Every implementation of a side of a connection passes the fixtures of every version it declares: LINA Core, the TUI, the LINA app on each platform and Node. The foundation verification runs them ([foundation.md](../policy/foundation.md)). A failing fixture blocks the supported row. Clients are tested in isolation against a fake LINA Core built from the same fixtures.

## Client connection

The TUI and the LINA app on every platform talk to LINA Core over the `client` connection. A local client uses the LINA Core local socket, and a remote client reaches LINA Core directly inside the tailnet ([runtime.md](runtime.md)). Clients use only the public event and control API.

The empty TUI increment (#32) implements the local client of the empty Core handshake: one newline-delimited JSON-RPC 2.0 `hello` request, offering client 1.0 and no capabilities. It validates the generated response envelope, JSON-RPC request id, `reply_to`, Core sender, selected version, main id, epoch and granted capabilities before reporting the connection as live. A refusal remains a typed error code; clients never infer compatibility from its diagnostic detail. The connection stays open until the person quits or input ends, then closes without sending a conversation request or stopping Core. Malformed or oversized handshake frames fail the connection; the frame limit is 1 MiB, matching the empty Core. This increment implements no reconnect, stream or conversation operations.

Every client connection acts for one person ([product-families.md](product-families.md)). LINA Core settles the person during the handshake and names the person id in `welcome`. Every later message on the connection is that person's: a message, an answer to a question or an approval, a cancel, a sign-in relay, an agent management request. A LINA admits only its owner, so a client on the local socket or inside the tailnet acts for the owner.

One connection reaches every agent of the main. A conversation kind, and a question asked in a conversation, names the conversation by its conversation id, and the conversation's participating agents follow from it. A request that starts a conversation names its participating agent by agent id. Every history item and every conversation event carries the agent id of the agent that spoke or acted, next to the person id where a person acted ([product-families.md](product-families.md)). A system-originated event, such as a background notice, carries the agent id of the conversation's agent and is marked as system-originated.

| Capability | What the client can do |
| --- | --- |
| Conversation | Start a conversation with an agent, send a message, interrupt, read history, subscribe to events, receive connect and disconnect notices |
| Agent management | List the main's agents, create an agent from a preset or a description, rename, tune and delete an agent. LINA Core records each change with the person id. |
| Work records | List, read, capture, edit, complete and reorder 목표 (goal), 일 (project), 할 일 (to-do), 계속 챙기는 것 (area) and memos; read the views (오늘 (today), 예정 (upcoming), 언제든 (anytime), 나중에 (later), the flow board) and the candidates; answer a proposal to add or split, and undo a promotion, an addition or an archive. Each is a request to LINA Core, and LINA Core computes the views. |
| Memory | Remember, correct, forget and move an item between layers, open an item's evidence, expand a ledger event or a summary, run a memory import (rehearsal, run, report) and answer its promotion batches ([conversation-and-memory.md](conversation-and-memory.md)) |
| Accounts | List the person's model-provider accounts with their plan, status and quota rows; start a sign-in; add an API-key account; sign out or remove an account; change the model policy ([runtime.md](runtime.md#accounts-and-sign-in)). LINA Core records each change with the person id. A credential never travels to a client; a key the person types travels once, from the client to LINA Core. |
| Task | Answer the questions of a task or background run, receive task and run status events, read the running list, cancel a task or stop a background run, send a follow-up to a background run |
| Approval | Answer approval requests with their choices: once or deny, and always allow where the request offers it ([main-authority.md](main-authority.md)) |
| Sign-in relay | Listen on a loopback port for a sign-in redirect and report the port to LINA Core, or report that it cannot listen; open a sign-in URL from LINA Core in the system browser; and return the redirect's `code`, `state` and `iss`, or its `error` fields, or the URL the person pasted ([integrations.md](integrations.md)). The sign-in of a plugin and the sign-in of a model-provider account use the same relay. |
| Multi-task control | Control several tasks or background runs at once, including stop all |
| Results | Receive typed results, read one by id, edit, save ([Typed results](#typed-results)) |
| Progress | Read the progress view: a line for each running 목표 and 일 with its name, its stage and bar, what just finished, what comes next and whether it waits on the person, and the rows that need the person on top; receive changes to it; and ask for its digest, the short form that a channel shows ([surfaces.md](surfaces.md#progress-at-a-glance)). LINA Core computes the view and the digest, and a client only draws them. |
| Waiting list | Read the list of what the person must do or decide in the order LINA Core ranks it, receive changes to it, ask for another of its orders, and answer a row |
| Details | Read the audit trail behind a card, a result or an answer, and the activity log, when the person asks |
| Channels | List channel connections, bindings and scopes; add a connection (the token passes to LINA Core once and is never returned); show a pairing code; bind an agent to a chat; change a scope; disconnect; receive channel state notices (connected, needs pairing, unreachable, messages possibly missed) |
| Agent exchanges | Read the agent talk log, expand one exchange, and subscribe to log entries and to the flag that an exchange needs the person |

Version 1.0 of `client` has at least these kinds: `hello` and `welcome`; starting a conversation; sending input; interrupting; reading history; subscribing to events; the stream events, which are an output delta, an item done and the turn state; an approval request and its answer; a question and its answer, where a question may carry a flat form (the name of the asking plugin and fields that are text, a number, yes or no, or a choice) and its answer is accept with the values, decline or cancel; a result, and the requests to read, edit and save one; the running list of background runs and delegated tasks with its updates, a stop and a follow-up; the background notice that delivers a landing into a conversation; the progress view with its changes and its digest; the waiting list, its changes and the answer to a row; a request for Details and its result; a readiness failure that names the preparation step that failed ([conversation-and-memory.md](conversation-and-memory.md)); agent management requests and their results; work record requests and their results; account requests and their results; memory requests and their results, including the progress of an import and a promotion batch; channel management requests, their results and channel state notices; agent talk log entries and their expansion; and an error. The Approval, Agent management, Work records, Memory, Accounts, Results, Progress, Waiting list, Details, Channels and Agent exchanges capabilities are part of version 1.0. Their payload fields are set in the schema (see Deferred).

Rules:

- A client never writes canon. Every client message is a request that LINA Core accepts or refuses. What a client displays comes from LINA Core events.
- After a reconnect, a client reads state from LINA Core again. It never replays its own display as truth.
- A client never reaches a model provider, a provider adapter or a worker directly. Everything passes through LINA Core.
- On desktop, the LINA app's shell holds the `client` connection (the local socket or the tailnet) and relays frames to the interface over a validated channel. The interface never opens a socket of its own, and the shell refuses any message that the schema of the negotiated version does not allow. The client side of the app is one TypeScript implementation that passes the conformance fixtures wherever it runs. The shell offers the connection to the interface through one bridge interface that is the same in every shell, so that the shell of an operating system can be replaced without a change to the interface or to the protocol ([surfaces.md](surfaces.md#app-front-end)).
- On mobile, a WebView host cannot use the local socket. The app connects with a WebSocket on LINA Core's tailnet address that carries the same JSON-RPC messages. This adds a transport, not a connection: the handshake, the kinds and the rules are the same. Where a mobile host does not allow that connection, a small native stream plugin in the host carries the same frames. The mobile transport check decides between them ([surfaces.md](surfaces.md#platform-adaptation)).
- A client relays a sign-in redirect, or the error of a failed one, as it received it and makes no decision about it.
- A protocol error, such as a broken line or an unknown kind, is connection status and log data. It never appears as conversation content.
- A channel is not a client connection. A message from an external channel never reaches LINA Core on `client`: the channel adapter inside LINA Core receives it, and LINA Core records it as a conversation event. An approval answered by a button in a channel is recorded by LINA Core as an approval answer with the platform's ids; it is not carried on `client`.
- No `client` message ever carries a channel token back to a client. A client passes a token to LINA Core once, when the person adds a connection.
- A background notice is sent to clients at the turn boundary at which LINA Core delivers it ([conversation-and-memory.md](conversation-and-memory.md#background-results)). A running-list update may arrive at any time and is display state only.

## Typed results

Core presents anything that the person should open and keep apart from the chat text as one typed result: a table, a diagram, a chart, a comparison, a plan, an outline, a draft document or a file. A result of delegated work ([work-and-delegation.md](work-and-delegation.md)) is presented the same way when it is shown to the person. Results are data. How each type is drawn is defined in [surfaces.md](surfaces.md#result-surfaces), how a conversation stores and uses a result in [conversation-and-memory.md](conversation-and-memory.md#results), and how a result is saved in [materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result).

A result has these fields:

| Field | Rule |
| --- | --- |
| `result_id` | Opaque id assigned by LINA Core, unique in the main, stable across revisions |
| `revision` | An integer from 1. Every change creates the next revision. |
| `type` | One value of the list below |
| `title` | A short name |
| `summary` | At most two sentences. It is what a card shows. |
| `text` | A complete plain-text rendering of the body, derived by LINA Core from the body. No model writes it. |
| `body` | The typed body of the type |
| `status` | `preview` or `saved` |
| `saved_as` | Null while `preview`; otherwise the reference to the saved material or record |
| `revised_by` | The person id or the agent id that made this revision |
| `provenance` | `{ agent_id, conversation_id, turn_id, task_id }`. `task_id` is null unless delegated work (a task or a background run) produced the result; it then holds that task's or run's id. |

The sources a body relies on travel in the envelope's `source_refs`. The `summary` field exists on every result; the `summary` type is a result whose body is an outline.

The closed list of types for version 1.0:

| Type | The body holds |
| --- | --- |
| `summary` | `outline` (ordered sections, each a heading with optional notes), `key_points`, `decisions`, `open_questions` (each with an answer, empty until the person answers), `scope` (what a document that follows covers and what it does not) and `length` (an estimate in screens) |
| `table` | `columns` (name, value type of text, number, date or boolean, and unit), `rows` and optional `totals` |
| `diagram` | `kind` (flow, sequence or tree), `direction`, `nodes`, `edges` (the edges of a sequence are in order) and `groups` |
| `chart` | `kind` (bar, line, area, pie or scatter), `series` with their values inline, axis labels and units |
| `document_draft` | `blocks` (heading, paragraph, list, table, code, quote, image reference, citation) and `from_summary`, the `summary` result and revision whose outline it was written from, or none |
| `comparison` | `targets`, each with a label, the old text, the new text and its `hunks`, and `apply`, the apply request it belongs to, or none |
| `plan_view` | Ordered `entries`, each with the kind of plan object, a title, a state and an optional parent entry, and `confirm`, the plan confirmation point it leads to, or none |
| `file` | A reference to the bytes, the name, the media type (a PDF or a slide deck) and the size |

One more type, `app`, is reserved for a later view that a plugin supplies ([integrations.md](integrations.md)). It is not part of version 1.0, no party sends or accepts it, and its fallback is the tool's text result.

Rules:

- Bodies are data only. No type holds HTML, script, an expression, or a URL that a renderer would fetch. A chart and a diagram are LINA's own closed specs, never the source text of another tool's language, because a spec language that can evaluate an expression or load a URL is not data. An image in a document is a reference to a material by asset id and revision, never a URL. A link in text is text and opens only when the person chooses, through the system.
- Text inside a body is data. It is never an instruction, an approval or a persona change ([materials-and-knowledge.md](materials-and-knowledge.md)).
- LINA Core validates every body against the schema and the size limits of its type before it creates the result. A body that fails is refused with `malformed`, and the reason goes to the agent that made it, never to the person as content.
- A revision replaces the whole result. A request that names a revision other than the current one is refused with `stale_revision`. Typed bodies arrive complete and never stream.
- A saved result takes no new revision. After a save, changes go to the saved material ([materials-and-knowledge.md](materials-and-knowledge.md#saving-a-result)).
- A client never writes canon. `result_edit` and `result_save` are requests that LINA Core accepts or refuses.

The `results` capability. A client declares in `hello` the result types it renders, and LINA Core grants the capability in `welcome` with the types it will send in full. A result of a type the client did not list reaches it without `body`: the card fields and `text`, which the client shows as a text card. A type that only a newer minor version defines is handled the same way. A client without the capability receives each result as an ordinary output item whose text is the title, the summary and `text`, and can do nothing further with it. A channel adapter inside LINA Core declares the same list in its own capability descriptor, and a channel is a low-capability client, not a special case.

Kinds, all in version 1.0:

| Kind | Direction | Content |
| --- | --- | --- |
| `result` | Core to client | A result in card form, which is every field except `body` and `text`. It is sent on creation, on every revision and on a change of status, to every client subscribed to the conversation, and it is the history item for a result. When it answers `result_get` it also carries `body` and `text`. |
| `result_get` | client to Core | Asks for one result by id, optionally at a revision. A result that was erased with its conversation answers `unavailable`. |
| `result_edit` | client to Core | The person's direct edit: the base revision and the replacement body. In version 1.0 only `summary` and `document_draft` accept it. A preview takes the edit as its next revision. A saved result refuses it with `out_of_scope`; the client edits the saved material through the material's own edit request. |
| `result_save` | client to Core | Asks to save one revision, with the destination. It carries an `idempotency_key` built from the result id, the revision, the action and the destination, and the answer reports the effect state ([Effect states](#effect-states)) and the saved reference. A repeated request never makes a second record. |

Applying the accepted hunks of a `comparison` is the apply request of delegated work, whose wire format is added with the work engine ([work-and-delegation.md](work-and-delegation.md)).

## Node connection

Node connects to the main over the `node` connection, locally or inside the tailnet. Its `hello` carries the node id, the node boot id, the host OS and the product version. `welcome` carries the main id and the current epoch. The Node then enters RECONCILING, and the state machines of [main-authority.md](main-authority.md) govern everything that follows.

| Message family | Direction | Content |
| --- | --- | --- |
| Grant | main to Node | Issue and revoke grants, with every grant field, including the agent that asked for the work or none ([main-authority.md](main-authority.md)) |
| Command | main to Node | Dispatch a command under a grant, and request cancellation |
| Receipt | Node to main | What the Node did under a grant, with provenance for every artifact it produced or touched |
| Effect report | Node to main | The effect state of every unconfirmed command, sent on reconnect and on request |
| Heartbeat | both | Liveness. The interval is set during Node implementation acceptance ([main-authority.md](main-authority.md)). |
| Worker relay | Node to main, and back | Events (including a worker's finish, failure or exit) and server requests (approvals, questions, dynamic tool calls) from a worker that the Node runs, and the answers ([work-and-delegation.md](work-and-delegation.md)) |

Rules:

- Every `node` message carries the epoch. A message from an earlier epoch is refused with `stale_epoch` by whichever side sees it first.
- LINA Core never connects to a worker on another device. The Node on that device owns the worker process and relays it.

## Deferred

- The JSON Schema to TypeScript type generator: chosen before the first LINA app work ([surfaces.md](surfaces.md#app-front-end)).
- Request timeouts on the `client` and `node` connections: set by measurement during implementation acceptance of each connection.
- The payload fields of the `client` 1.0 kinds: set in the schema by the implementation issue that ships the first conversation product.
- The JSON Schema of the result bodies, their size limits and the payload fields of the result kinds: set in the schema by the implementation issue that ships results.
