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

### C-55 running count increment

#767 makes the running count follow lines, not rows.
`progress.CountLines` counts the visible lines of a projection: a row at depth 0 belongs to its own line, a goal's step cards are rows under it, and a line whose row waits on the person counts as waiting for them. Core publishes the result as `counts.running` and `counts.waiting` beside the overview rows, so a client draws Core's number and never recounts the cards it folds.
`progress.Compute` marks each entry as a line or a row: a step card is a row of the 일 that runs it, and a task handed under a 목표 is that 목표's 일, a line of its own. A line carries `row` on the wire so a client never draws a row as a card.
A step's card carries the id of the dispatch that started it, not the step id: `runGoalWorker` passes the action id to `RunGoal`, and dispatch keys the record and the card by `work.DispatchID(turn, action)`. `progress.GoalStages` reads the goal's worker actions from `work_records` and puts each step's card ids on `StageStep.Cards`, off the wire; an action that observes another after a restart leads to that action's card. `Compute` links a card to its 일 through those ids, and a step's start is the earliest start among its cards. Background and wait steps start no dispatch and have no cards; a background step's run is still published as a line of its own.
A managed goal is one 일, so `progress.Goals` publishes it as a project line; a card that groups others stays a 목표 line.
A goal line carries the running list's state word, projected by `progress.goalState` in the order the stage list uses: input needed, failed, stopped, working, waiting, then the goal's own state.
`e2e/m3/running-count.json` drives installed Core with three 일 of four steps and one background run outside any 일: four lines by state, thirteen rows in the running list, and the TUI status line prints Core's number.
