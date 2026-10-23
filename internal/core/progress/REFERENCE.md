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

### C-55 progress line increment

#765 publishes the line's Now, start and stage entries.
`progress.Goals` projects each goal with work in flight or something waiting on the person from `work_records`, so the projection runs in the same snapshot as the work cards.
The title is the goal's own sentence from its canonical record, because the execution ledger never holds that prose; a missing title uses the untitled word.
The start is the recorded hand-over instant, and a legacy goal with no instant stays unknown rather than claiming a later time.
Now follows the order of the design: a step that needs the person, then the earliest-started working step with the number of the others, then the stage word, then the wait.
The stage word is the lowest-numbered stage entry that is not done.
`Line.Stages` carries the entries, and the line holds no step id; `StageStep.ID` stays on the projection side.
`e2e/m3/progress-line.json` drives a headless Core whose first step lands from an injected worker result: the subscribed topic carries the entries, the changed line arrives within 250 ms of the commit, exactly one line changes, and a recomputation that changes no line publishes no frame.
`internal/core/boot/progress_line_test.go` checks the same projection and wire shape without a socket.

### Event-to-card latency

#771 projects a goal with its own stage list and no separate project rows as
one work card. Grouping goals keep their child cards. The execution state and
Now both come from observed steps.

A progress overview reads graph revision history and work-card start times
once per canon snapshot and groups them by goal. `GoalStages` and the overview
share the same stage projection. This removes repeated decoding of every
other goal's history without retaining a cache across transactions.

`TestM3WorkPanelEventLatency` uses the fixed graph/name corpus, actual Core
step reports and eight TUI PTYs. Each sample joins the durable work-engine
event's precise admission time to the first complete renderer write showing
its changed card. The scenario logs every sample and enforces the T-16 budget
per language, glyph and width combination.
