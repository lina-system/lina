# Self-diagnosis log

This contract fixes the one structured event format that every LINA component writes, what an event may never contain, how LINA reads its own events back, where they live, and what proves that LINA can explain a failure from them. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md).

It builds on the product boundaries in [product-families.md](product-families.md), the epoch and grant terms in [main-authority.md](main-authority.md) and the state root in [filesystem.md](filesystem.md). Finishing this document does not mean any event is written, queried or explained yet. Runtime proof belongs to the implementation issues that consume this contract.

## Purpose

The log exists so that LINA can read its own events and find out what failed and why. The reader is LINA itself, not a person. A person may still look at the events, but the format is chosen for machine reading first: fixed fields, closed code lists and opaque ids that a query can filter and a summary can cite.

Human-readable text logs are secondary. A component may write them, but nothing in diagnosis depends on them and no acceptance check reads them. When a text log and a structured event disagree, the structured event is the record.

Every component writes the same format. Conversation, task, memory, materials, Node and LINA APP share one schema, so a failure that crosses components can be followed by id from the first event to the last without translating between formats.

Each event records what happened, what caused it, what it produced and what LINA does next.

## Event schema

An event is a flat record with exactly the fields below. Every field is present in every event. A field that does not apply holds an explicit null where the rule allows null; it is never omitted. No other field is permitted, so a writer that needs more information encodes it as a code or an opaque id, never as a new field.

| Field | Type | Rule |
| --- | --- | --- |
| `event_id` | string | Opaque id, unique across the whole install. Never reused. |
| `time` | string | RFC 3339 timestamp in UTC. Written by the component at the moment the event is recorded. |
| `component` | string | Exactly one of `conversation`, `task`, `memory`, `materials`, `node`, `app`, `os`. |
| `conversation_id` | string or null | The conversation the event belongs to, or null when there is none. |
| `task_id` | string or null | The task, or the background run (its run id), the event belongs to, or null when there is none. |
| `agent_id` | string or null | The agent the event is recorded for: the agent that spoke or acted, or the requesting agent of the task ([product-families.md](product-families.md)). Null when no agent is involved, such as an install event or a job for the whole main. |
| `epoch` | string or null | The main epoch the event was recorded under, as defined in [main-authority.md](main-authority.md), or null for events recorded before an epoch is durable. |
| `node_id` | string or null | The Node that recorded the event, as defined in [main-authority.md](main-authority.md), or null when the main or a client recorded it. |
| `cause` | object | `{ code, parent_event_id }`. `code` is an allowlisted cause code. `parent_event_id` is the `event_id` of the event that led to this one, or null for a root event. |
| `result` | string | Exactly one of `pending`, `success`, `failure`, `cancelled`, `denied`, `unknown`. |
| `next_action` | string | An allowlisted action code that names what LINA does next. Never free text. |
| `error_class` | string or null | Null, or exactly one of `model`, `interrupted`, `storage`, `permission`, `network`, `internal`. |

Rules that apply across fields:

- `cause.parent_event_id` links events into a chain. A diagnosis follows the chain backwards from the failing event to its root; every link must resolve to an event that exists.
- `result` describes this event only.
- `unknown` is a real result, not a placeholder. It is written when the component can't tell whether an effect happened, which is the same meaning the term has in [main-authority.md](main-authority.md).
- `denied` means an authorization check refused the action before any effect; it is never used for an effect that started and then failed.
- Cause codes and action codes come from closed allowlists owned by the implementation. A code outside the allowlist is never written.
- Component names in `component` match the product boundaries in [product-families.md](product-families.md): `app` is LINA APP, `node` is Node, `os` is the host/boot layer (or the optional LINA OS), and the remaining four are LINA Core. Events of the channel gateway are written under `conversation`, because they belong to a conversation; the gateway is not a component of its own.

The table above is the complete field list. A check that compares an event's keys against this table must accept exactly these twelve names and reject anything else.

### Go emitter and code vocabulary

`kit/diag` supplies the emitter and its wire codec. `Fields` accepts numeric enum types, opaque `ID` values and a numeric `Epoch`; it has no string, payload or error argument. The emitter creates a fresh random 128-bit event id and a UTC timestamp. IDs serialize as 32 lowercase hexadecimal characters, with the zero value serialized as null only for optional references. Epochs serialize as positive decimal strings, or null before an epoch is durable. Unknown enum values, malformed ids, duplicate keys, missing fields and additional fields are rejected before a record can enter the store. The same strict codec validates stored records on read; it does not redact or discard fields.

The initial cause-code allowlist is `boot_started`, `turn_started`, `task_started`, `operation_started`, `operation_succeeded`, `operation_failed`, `model_failed`, `task_stopped`, `storage_failed`, `permission_denied`, `network_failed` and `effect_unknown`. The action-code allowlist is `none`, `continue`, `report_failure`, `reconcile` and `request_approval`. Adding a code requires an enum/codec change, tests and an update to this section in the same pull request; existing wire meanings are never repurposed.

`Emitter.Root` creates a boot root under `os`, a turn root under `conversation` with a conversation id, or a task root under `task` with a task id. Roots have a null parent, `pending` result, `continue` action and null error class. Other events require an existing parent. Agent, epoch and Node references remain optional according to the table. Callers map their errors to the closed class and cause codes; raw errors never enter the emitter. Connecting these roots to running Core flows belongs to those flows' implementation issues.

## Redaction

Redaction happens before persistence. A component never writes an event and cleans it later; the event that reaches storage is already the redacted form, and no unredacted copy exists anywhere.

An event carries only allowlisted codes and opaque ids. In particular an event must never contain:

- secrets: keys, tokens, passwords, session cookies or anything a credential store holds;
- raw personal content: message text, document bodies, memory contents or any personal material the person gave LINA;
- prompts: the text sent to a model, in whole or in part;
- provider payloads: request or response bodies from a model provider or any external service;
- absolute paths under the person's home or any other user-owned location.

Materials are referenced by asset id and revision, as defined in [filesystem.md](filesystem.md), never by path or content. A human file outside the state root is referenced the same way once it has an asset id, and not at all before that.

Error detail follows the same rule. An error is recorded as an `error_class` and a cause code; the provider's message, the stack trace and the offending input stay out of the event.

A redaction failure is a defect of the writing component. Any check that finds forbidden content in a persisted event fails the component, regardless of whether the diagnosis would have succeeded.

## Query interface

LINA reads its own events through one read-only query, exposed to LINA as one of its own conversation tools ([runtime.md](runtime.md)). The tool's name is set with its tool declaration, not by this document; this document fixes what the query accepts and returns.

Filters:

| Filter | Meaning |
| --- | --- |
| `since` | Only events with `time` at or after this moment |
| `until` | Only events with `time` before this moment |
| `component[]` | Only events whose `component` is in the list |
| `conversation_id` | Only events with this `conversation_id` |
| `task_id` | Only events with this `task_id` |
| `agent_id` | Only events with this `agent_id` |
| `cause_code` | Only events whose `cause.code` equals this code |
| `result[]` | Only events whose `result` is in the list |
| `error_class[]` | Only events whose `error_class` is in the list |
| `limit` | Maximum number of events in one page |
| `cursor` | Continuation token from a previous page |

Filters intersect. An event is returned only when it satisfies every filter given; an omitted filter matches everything. The query never writes, never redacts on the way out (the stored event is already the redacted form) and never returns fields that are not in the schema.

The response holds `events`, ordered by `time`, and `next_cursor`, which is null when no more events match.

`internal/core/eventstore.Query` implements these filters as a package API. Zero ids and a zero cause code omit their filters. An empty enum list matches all values; the error-class list may include the null class. `limit` defaults to 100 and must be between 1 and 1000 when supplied. Equal timestamps are ordered by event id. The opaque cursor records the last time/id and binds to the same filters; callers may change the page size. Pages are live, not snapshots: events inserted before an already returned position do not appear in later pages. `Chain` retrieves an event's complete ancestry, root first, independently of query filters. Neither function is an authorization boundary; the conversation tool must apply access rules before querying. Both reject malformed stored records, duplicate ids, missing parents and cycles. The initial implementation scans the dedicated event database; indexing, retention and size measurements remain acceptance work.

A summary that LINA produces from the query cites event ids. Every claim about a cause names the `event_id` of the event it rests on, so that the claim can be checked against the stored record. A summary without event ids is not a diagnosis.

## Storage

Events live under the system area of the state root defined in [filesystem.md](filesystem.md). That is the `system/` area: installation state, written by the install, snapshotted with the OS root and not part of a backup generation.

Events are not canon and not materials. Events never go through the canon writer, and writing an event never changes canon or materials. A backup generation does not include events, and a restore does not restore them.

Events from a Node use the same schema. A Node event is never a receipt and never confirms an effect on its own.

The concrete database form under `system/` follows the storage rules of [runtime.md](runtime.md).

The Go store uses a dedicated SQLite database supplied by its caller at `system/events/events.sqlite`. `Open` accepts `database/sql.DB`, reserves one connection, requests and verifies WAL and `synchronous=FULL`, and creates the `events` table. Each row stores an indexed event id and the complete, strict twelve-field JSON record. It validates existing records at open and on reads. A mutex serializes this store's writes and reads; the caller must give it exclusive writer ownership of this database. Every append commits synchronously, including failure, unknown, denied and cancelled outcomes. For those four terminal results, both the emitter and store append detach from the caller's cancellation and deadline with `context.WithoutCancel`, retain its context values, and impose an independent five-second commit timeout. Passing from emitter to store preserves that budget rather than renewing it. Thus a stopped task can record its cancelled event using its own cancelled running context; pending and successful events still honor that context. This writer never opens canon or materials and never uses the canon writer.

`PrepareReserve` validates any existing reserve and attempts to allocate and sync a 256 KiB file at `system/events/reserve`, allocating bytes rather than extending a sparse file. If that attempt fails with a full disk, preparation still succeeds with the reserve reported as consumed by `Reserve.Present() == false`. Failed partial allocations are removed; symlinks, non-regular files, unsafe permissions and other storage errors still fail preparation. `Open` validates the reserve without allocating it, so an absent or consumed reserve does not prevent reopening and reading committed failure events. A complete existing reserve is reused without rewriting it. The caller supplies a classifier for its binding's typed SQLite full-disk error. On that error for a failure, unknown, denied or cancelled result, the writer releases the reserve and retries the same event once. Pending and successful events never consume the emergency reserve. A retry checks an already committed id against its complete record, so an ambiguous commit cannot duplicate it or replace another event. Other errors are not retried. A consumed reserve stays consumed unless preparation can restore it or the caller explicitly calls `Replenish` after recovering space. Explicit replenishment errors are reported and leave the reserve marked consumed; successful replenishment marks it present. The caller closes the store, reserve and supplied database in that order. Driver error messages are never returned as events or exposed by the store's error API.

The SQLite binding is supplied by #39; this increment adds no driver or module dependency. Scripted `database/sql` tests establish call ordering, transaction error handling, strict record decoding and bound values, not SQLite persistence. Reopen/crash durability, real full-disk commits and reserve sizing with that binding remain unverified until binding-dependent acceptance runs.

## Diagnosis acceptance

The contract is accepted when LINA explains an injected failure from events alone. Three failures are injected, one at a time, in a running install:

- a model error: the model provider returns a failure, so the event chain ends in `error_class` = `model`;
- a task interruption: a running task is stopped before it completes, so the chain ends in `error_class` = `interrupted`;
- a storage failure: a write to the state root fails, so the chain ends in `error_class` = `storage`.

For each injected failure, LINA is asked what went wrong with no access to text logs, provider output, screens or the person's description. It answers with a summary that cites event ids, and the summary is checked by machine, not by reading.

The assertions check ids and codes:

- the cited root `event_id` is the event the injection produced;
- the chain from the failing event through `cause.parent_event_id` reaches that root without a missing link;
- the `error_class` and cause code named in the summary equal the ones stored on the failing event;
- no event returned by the query for the injected failure contains content forbidden by the Redaction section.

Prose is not asserted. A test that compares the summary's wording, tone or length is not an acceptance check for this contract.

Passing these checks in an implementation is claimed by the implementation issue that ran them, with the event ids and query filters it used, not by this document.

The #41 package tests simulate a provider failure, cancellation of a running context and a failed store write. They record each failure, query by agent and task, and build a machine-readable citation from those events alone. `diag.VerifyCitations` checks the independently supplied injection root, the cited failing event, its class and cause code, and every link to the root. It rejects empty citations, missing ids, cycles, duplicate ids, incorrect roots and mismatched codes. Tests log the generated root/failure ids and query filters and assert the exact stored key set and absence of synthetic forbidden content. They do not assert summary prose or claim a running-install model diagnosis. No protocol or TUI operation exposes this package yet; that observable scenario belongs to the issue that wires the conversation query tool.

## Deferred

- The retention period and the size cap of the event store are set during implementation acceptance, after the first measurement of a running install.
- The query tool's name and declaration, recorded with the conversation tool declarations in the release manifest ([runtime.md](runtime.md)), and its implementation are set during implementation acceptance.
- Additional cause and action codes follow the growth rule above as writing components are implemented.
- Rotation and the binding-dependent persistence and reserve measurements remain implementation acceptance work within the storage rules of [runtime.md](runtime.md).
- The diagnosis acceptance runs themselves, including how each failure is injected and which install they run on, are set during implementation acceptance.
- Whether and how Node events are copied to the main beyond the reports that the main authority contract already allows is set during implementation acceptance.
- Whether an event records the person it acts for is set together with admitting people other than the owner, a backlog item on the person axis ([cognition-and-life.md](cognition-and-life.md)).
