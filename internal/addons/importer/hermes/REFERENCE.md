# Staged Hermes import reference specification

References are read-only public snapshots at the revisions already credited in
[THIRD-PARTY-NOTICES.md](../../../../THIRD-PARTY-NOTICES.md). The named local plugin
paths were unavailable during this implementation. No installation data is used.

- **seen** — LCM-X `host_uid.py:102-130` resolves session lineage through
  compression parents, stopping at forks, missing parents and cycles;
  `tests/test_host_uid_shadow.py:115-124` binds newly stored messages as canonical
  in that lineage. Import namespaces must include the profile and lineage.
- **seen** — LCM-X `host_uid.py:299-334` distinguishes canonical bindings from
  payload-matching versions. `tests/test_host_uid_shadow.py:173-185` retains
  uid-less messages. Import retains all raw rows, not only UID-bearing rows;
  generated engine bindings must not turn into invented message rows.
- **seen** — Honcho `hermes-plugin-honcho/session.py:425-451` writes user-role
  messages under the per-message author when supplied, otherwise the session's
  user peer. `tests/test_turn_author_peers.py:40-48,85-96,119-135` separates bot
  authors from the owner and writes the author's peer. An owner peer alone
  therefore cannot establish person attribution in historical records.
- **inferred** — Map source copies to a canonical event while preserving each
  source row's distinct fields and key. Reuse the existing host/LCM provenance
  classifier after all matches; never derive speaker kind from text.
- **inferred** — LINA's ordered stages, fenced conversation transaction,
  row-level legacy references, typed canonical row hashing and reverse mapper
  implement C-28; these are LINA requirements, not upstream import algorithms.
- **unknown** — The installation's published Honcho API description remains
  unregistered. Live Honcho ingestion must retain the existing refusal. The
  stage engine accepts adapter-prepared records; synthetic tests exercise
  matching and history adoption without registering a fictional server schema.

The Go tests cover canonical/version/engine binding distinctions, repeated IDs
in different profiles, unchanged per-copy source fields, owner/automation
attribution, missing links, rollback, round-trip hashes and idempotent replay.

Identical payload versions retain a separate replay event while host copies keep
the canonical binding. Unsealed events use private staging tables until the
separate seal, and sidecar bytes use the native material writer’s recovery and
transactional adoption path. These storage boundaries are LINA requirements.

- **seen** — LCM-X `externalize.py:25-28,684-750,1253-1285,1412-1434`
  at the same pinned revision defines `lcm-large-outputs`, basename `ref=`
  markers and JSON payloads with content/character/UTF-8 byte counts. The CLI
  recognizes that representation, confines files to the chosen source, checks
  the payload shape/counts and snapshots whole original JSON bytes. The source
  does not record a full file SHA-256; capture computes it and verifies the
  second read. An absent file keeps an unavailable reference with no invented
  digest. A sidecar reference is scoped by its LCM message id and basename.
- **inferred** — Rehearsal plans may retain sidecar snapshots without preparing
  destination materials and cannot be adopted. After confirmation, the host
  prepares native assets. Sealed events retain their material identities;
  ledger reads and search apply the original source spans before exposing text.

## Implementation notes

JSON display masking uses the shared decoded-string scanner at publication and
material readback. Original `import_event_span` and `import_source_span` offsets
remain byte-addressed and unchanged. A masking failure publishes an inline
placeholder and retains `body_masking_error` in the native event's origin.
`SealReport.UnavailableBodies` and `BodyFailures` count and identify these events
on both first import and retry. The original sidecar remains referenced by
`import_event` even when the native event uses the inline fallback. No migration
is needed. `TestM1ImportJSONMasking` exercises installed import, verified backup,
repeat import, safe promotion and a restarted Core's memory API with synthetic data.

Design authority: [Go Core](../../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-28 import migration

The shared canon catalog appends import storage and completion receipts at migration 24, after the unchanged published migrations 1–23.
Core, backup and restore can reopen imported state using that same catalog.
Empty import tables do not load an adapter or start a run.

### Imported tool-call bodies

- **seen** — LCM-X `store.py:477-497,2129-2147` at the pinned revision
  stores `tool_calls` separately from content, decodes the JSON on read and
  restores both fields in `to_openai_msg`. The local reference directories were
  unavailable; these lines were read from that revision's public source.
- **seen** — LINA's native transcript stores function calls with `type`,
  `call_id`, `name` and string `arguments`. The summary builder and event
  expansion consume the live ledger body, not `legacy_ref.fields`.
- **inferred** — Seal can publish an assistant body containing its content and
  an ordered list of those native call items while leaving the staged body and
  source reference fields intact for byte-exact row reconstruction.
- **unknown** — Unregistered call formats and local installation modifications.
  An unsupported nonempty call payload stays in the live body as
  `unparsed_tool_calls`, preserving its exact source text rather than guessing
  executable fields or losing arguments. Existing sealed conversations remain append-only.

## Current host layout and LCM kinds (#1102)

- **seen** — Hermes Agent `gateway/delivery_ledger.py:1-10,188-208` at
  `944c3a434071f43e64b443d033f3eba30c64434c` stores gateway delivery retries
  in `delivery_obligations` and explicitly migrates the older table by adding
  `adapter_profile TEXT`. Both exact column layouts are registered alongside
  the original host layout. The local reference paths were unavailable;
  this observation uses the public source, without installation data.
- **seen** — LCM-X `host_uid.py:420-453` at the revision above calls
  `alias_candidate` shadow, observational evidence, with `position_proof` or
  `unknown` proof. `identity_anchor.py:1336-1339` records `composite` groups
  from the timestamp donor to ordered constituents, including the donor itself.
  `store.py:1176-1205` retains the group identity in `created_at`.
- **inferred** — Omit delivery retries as operational state and report their
  inventory. Preserve alias candidates on the referenced LCM event without
  using them to match host identities. Preserve composite source rows and
  ordered head/member links without merging events, inventing a composite
  event, or asserting that one constituent is a view of another.
- **unknown** — Other delivery table layouts and binding/relation kinds are
  not registered. The actual installation revision was not available here.

Tool-call projections use the same JSON display scanner and unavailable-body
reporting as other imported bodies. Scalar credential preparation failures also
become `body_masking_error`, with originals and retry reports retained. The final
scalar coverage check reads decoded JSON; scanning serialized escapes as plain
text would discard already-masked quoted arguments.

## Owner channels and display provenance

Read-only reference: Hermes Agent (MIT), revision
`cb757fac59c92b49e9b8f2910e5cd7ab75efffdd`. The named local installation was
unavailable; the public source snapshot was inspected before implementation.

- **seen** — `gateway/run_turn_prepare.py:726-738` creates a persistence owner
  UUID from the delivery namespace/message id, or a random UUID for keyless
  input. `gateway/run_turn_persistence.py:416-432` writes it as
  `display_metadata.gateway_input_owner`; `gateway/session_transcript.py:608-628`
  uses it to find an already persisted input. It proves delivery ownership,
  not the human sender.
- **seen** — `gateway/response_filters.py:111-129` marks injected internal and
  heartbeat turns `internal_notification`. `agent/prompt_builder.py:586-595`
  stores mid-turn user steering as a user row with `display_kind=steer`.
- **seen** — `tui_gateway/session_registry.py:339-357` retains the desktop
  transport login; `tui_gateway/session_workdir.py:594-603` stores it in
  `sessions.user_id`. `tests/tui_gateway/test_desktop_session_login_identity.py`
  demonstrates authenticated and anonymous desktop sessions. Anonymous and
  legacy sessions have no recorded login. `tui_gateway/session_lifecycle.py:392`
  also recognizes `webui`; `agent/prompt_builder.py:724-732` explains that current
  dashboard chat uses desktop/tui rather than constructing a webui platform.
- **inferred** — A sole nonempty string `gateway_input_owner` is neutral for
  attribution. It never substitutes for the confirmed channel; unknown keys or
  malformed values remain unproven. `steer` still requires channel proof and
  `internal_notification` is non-person regardless of the confirmed map.
- **inferred** — CLI, desktop and legacy webui sessions may be confirmed
  individually by their owner. The confirmation attests ownership of the
  displayed session and recorded identities, including identity-less local
  sessions. Gateway confirmation instead requires an exact source/user/chat
  tuple and a private/dm host session. Neither permits shared-chat attribution.
- **unknown** — Historical webui deployments and anonymous desktop sessions do
  not provide independently verifiable human identity in the registered schema.
  They require explicit per-session attestation; a source name, token, title or
  message body is never enough. No new Honcho schema is registered.
