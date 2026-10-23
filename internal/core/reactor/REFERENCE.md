# Completion-event reference observations

Read before implementation for #128. Design authority is C-58 and
[Event-driven flow](../../../docs/design/work-and-delegation.md#event-driven-flow).

## Seen

The pinned CRW `CompletionReceipt` in
[testdata](../../../testdata/crw/schema/completion-receipt.json) separates a
worker's delivery claim from a terminal observation. Its identity binds the
relationship, generation, revision and outcome; execution-only outcomes also
bind the turn and attempt. A completed turn alone is not a verified delivery.
The pinned source and hashes are recorded in the corpus provenance.

## Inferred

LINA needs its own transactional outbox, keyed by task, execution attempt and
observed Git head. It must retain the receipt identity, re-read current work
before claiming, and use the existing conversation queue for reactions.
Reconciliation restores missing signals from durable work facts without
starting a worker, calling a model or inventing a completion observation.

## Unknown

The supplied live CRW and Hermes checkouts are unavailable in this environment.
The pinned schema establishes neither their queue implementation nor crash or
reconciliation behavior. No upstream queue algorithm is copied or claimed as
verified, and no CRW process or review workflow is run.

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-58 completion migration

#128 adds canon migration 40 for `work_completions`, separate claim tombstones and Core-captured completion origins.
A trusted terminal observation and its completion row commit with the work ledger/card in one fenced transaction, after a receipt exists.
A late receipt creates the same signal when termination was already observed.
The attempt key is Core's run ID; Git heads come from Core observations, and a result without a Git head uses the empty head.
A corrected file receipt or directory fingerprint replaces the current row for that task/attempt/head with a new reaction identity; prior claims remain tombstones, and unchanged output retains its identity.
Worker receipt formats stay unchanged.
On upgrade, persisted dispatch PR evidence fills a previously absent terminal head; it cannot replace a known head.
