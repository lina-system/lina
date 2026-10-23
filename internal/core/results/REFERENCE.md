# internal/core/results implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-52 planning migration order

Core installs the results and planning receipt schemas in the numbered canon catalog at migration 49, after the published Korean records search migration 33, waiting decisions migration 34, preset effects migration 35 and skill maintenance migration 36, work engine migration 37 and to-do records migration 38, work event sequence guard migration 39, work completions migration 40 timers migration 41, starter/selection migration 42 and worker choice migration 43, naming-breach migration 44, record-operation migration 45 and operational switch migration 46, handoffs migration 47 and work-review migration 48.

### C-52 result tool increment

The #104 increment implements these boundaries in `kit/result`, `internal/core/results` and `internal/core/exec`.
Core supplies the trusted person, agent, conversation and turn; none is accepted from tool arguments.
`present_result` takes `type`, `title`, `summary` and a complete `body`, with `result_id` and `base_revision` only for replacement.
Optional `reply_type` identifies `chat`, `status`, `comparison`, `candidates` or `third_party` for the work-method shape checks; it is stored with the result.
A `document_draft` may name a `recipient`, and its blocks may mark `audience` as `recipient` or `person`.
These annotations never change the stored or rendered body.
`read_result` takes `result_id` and returns the complete current result and its revision.
`save_result` takes `result_id`, `revision` and a resolved `destination`; its binding requires an exact matching request from the person in the current turn.
A grant alone does not constitute that request.
The three operations use the existing tool registry, authority gate and leases, run in Core, and require no network or approval interaction.

### CP-05 result save key

The #104 implementation hashes the unambiguous tuple of result id, revision, `result_save` and the normalized destination; call ids, turns and boot epochs do not change it.

### Generated cards

`present_surface` uses the same result scope, authority and revision transaction.
Its body is a bounded A2UI message batch. `PresentSurface` folds against the current
surface privately, then `Present` rechecks the base revision while committing the
whole body and text. A failed fold or a concurrent revision publishes nothing.
`deleteSurface` becomes an empty tombstone; the result id and history remain.
No database migration or renderer dependency is added.
