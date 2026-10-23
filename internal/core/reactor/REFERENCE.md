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
