# internal/core/progress implementation notes

## Implementation notes

Design authority: [Go Core](../../../docs/design/go-core.md) and the contracts it cites.
These notes retain implementation and validation history from the cited increments;
they do not establish current acceptance or change the contracts.

### C-55 progress digest increment

#159 implements `progress_digest_get` through the same current-source overview.
`progress.Digest` keeps waiting rows first and emits at most five single-line display rows without bars or navigation identities, plus total and omitted counts and a progress link.
The requested waiting order is computed by Core before the limit.

### C-55 stage list increment

#764 projects the numbered stage list from the work ledger.
`progress.Stages` takes the admitted step graph revisions in commit order and the step states observed with each, and returns the numbered entries followed by 확인.
Numbering is the longest chain of blockers; a stage is one entry, labelled by its first step by start and then by id, with the number of the others.
A done stage keeps its number and its steps, so a step admitted later is placed past the leading run of done stages, and its dependents follow it in the same revision.
A superseded step leaves the counts but stays in its stage.
`progress.GoalStages` reads the revisions from the `goal.step_graph` work events and the newest task states from the current goal record, so a restart projects the same list.
An unknown or another person's goal returns `goal.GraphUnavailable`.
The line's stage word and the published entries remain with #765; the TUI drawing remains with #770.
