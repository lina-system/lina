# Self-diagnosis log

This contract fixes the one structured event format that every LINA component writes, what an event may never contain, how LINA reads its own events back, where they live, and what proves that LINA can explain a failure from them. It is normative: implementations must follow it, and any change to it is made in this file and joins dev under [integration.md](../policy/integration.md).

It builds on the product boundaries in [product-families.md](product-families.md), the epoch and grant terms in [main-authority.md](main-authority.md) and the state root in [filesystem.md](filesystem.md). Finishing this document does not mean any event is written, queried or explained yet. Runtime proof belongs to the implementation issues that consume this contract.

## Purpose

The log exists so that LINA can read its own events and find out what failed and why. The reader is LINA itself, not a person. A person may still look at the events, but the format is chosen for machine reading first: fixed fields, closed code lists and opaque ids that a query can filter and a summary can cite.

Human-readable text logs are secondary. A component may write them, but nothing in diagnosis depends on them and no acceptance check reads them. When a text log and a structured event disagree, the structured event is the record.

Every component writes the same format. Conversation, task, memory, materials, Node, LINA APP and LINA OS share one schema, so a failure that crosses components can be followed by id from the first event to the last without translating between formats.

Each event records what happened, what caused it, what it produced and what LINA does next.

## Event schema

An event is a flat record with exactly the fields below. Every field is present in every event. A field that does not apply holds an explicit null where the rule allows null; it is never omitted. No other field is permitted, so a writer that needs more information encodes it as a code or an opaque id, never as a new field.

| Field | Type | Rule |
| --- | --- | --- |
| `event_id` | string | Opaque id, unique across the whole install. Never reused. |
| `time` | string | RFC 3339 timestamp in UTC. Written by the component at the moment the event is recorded. |
| `component` | string | Exactly one of `conversation`, `task`, `memory`, `materials`, `node`, `app`, `os`. |
| `conversation_id` | string or null | The conversation the event belongs to, or null when there is none. |
| `task_id` | string or null | The task the event belongs to, or null when there is none. |
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
- Component names in `component` match the product boundaries in [product-families.md](product-families.md): `app` is LINA APP, `os` is LINA OS, `node` is Node, and the remaining four are LINA Core.

The table above is the complete field list. A check that compares an event's keys against this table must accept exactly these twelve names and reject anything else.

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

A summary that LINA produces from the query cites event ids. Every claim about a cause names the `event_id` of the event it rests on, so that the claim can be checked against the stored record. A summary without event ids is not a diagnosis.

## Storage

Events live under the system area of the state root defined in [filesystem.md](filesystem.md). That is the `system/` area: installation state, written by the install, snapshotted with the OS root and not part of a backup generation.

Events are not canon and not materials. Events never go through the canon writer, and writing an event never changes canon or materials. A backup generation does not include events, and a restore does not restore them.

Events from a Node use the same schema. A Node event is never a receipt and never confirms an effect on its own.

The concrete file or database form under `system/` is not fixed here; it follows the storage rules of [runtime.md](runtime.md).

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

## Deferred

- The retention period and the size cap of the event store are set during implementation acceptance, after the first measurement of a running install.
- The query tool's name and declaration, recorded with the conversation tool declarations in the release manifest ([runtime.md](runtime.md)), and its implementation are set during implementation acceptance.
- The cause-code and action-code allowlists, and the rule for growing them, are set during implementation acceptance.
- The concrete file or database form of the event store under `system/`, and its rotation, are set during implementation acceptance within the storage rules of [runtime.md](runtime.md).
- The diagnosis acceptance runs themselves, including how each failure is injected and which install they run on, are set during implementation acceptance.
- Whether and how Node events are copied to the main beyond the reports that the main authority contract already allows is set during implementation acceptance.
- Whether an event records the person it acts for is set together with admitting people other than the owner, a backlog item on the person axis ([cognition-and-life.md](cognition-and-life.md)).
