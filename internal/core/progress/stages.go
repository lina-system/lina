// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
)

// ErrStageGraph refuses a revision that is not a step graph: a duplicate or
// empty step id, a blocker that is not a step of the same revision, or a cycle.
// Admission already refuses these; a projection never guesses a numbering.
var ErrStageGraph = errors.New("stage_graph_invalid")

// StepState is the observed state of one step, in the work card words. A step
// removed by a later revision is superseded and leaves the counts.
type StepState string

const (
	StepNext       StepState = "next"
	StepWaiting    StepState = "waiting"
	StepWorking    StepState = "working"
	StepNeedsInput StepState = "needs_input"
	StepFailed     StepState = "failed"
	StepStopped    StepState = "stopped"
	StepUnknown    StepState = "unknown"
	StepDone       StepState = "done"
	StepSuperseded StepState = "superseded"
)

// StageKind tells a numbered stage from the entries that frame the list.
type StageKind string

const (
	KindStage    StageKind = "stage"
	KindDesign   StageKind = "design"
	KindChecking StageKind = "checking"
	KindDone     StageKind = "done"
)

// Step is one step of an admitted revision with the state observed with that
// revision. A step present in an earlier revision and absent from a later one
// was closed as superseded.
type Step struct {
	StartedAt time.Time
	ID        string
	Name      string
	DependsOn []string
	State     StepState
}

// Revision is one admitted step graph revision, whole, with the step states
// observed when it was committed. The ledger keeps the goal record in each
// revision's event, so a replay needs no second source.
type Revision struct{ Steps []Step }

// StageStep is one step of a stage, as the 일 view lists it. The id stays on
// this side: a line holds no identity.
type StageStep struct {
	StartedAt time.Time `json:"started_at,omitzero"`
	ID        string    `json:"-"`
	Name      string    `json:"name"`
	State     StepState `json:"state"`
}

// Stage is one entry of a stage list. Number is 0 for 설계, 확인 and 끝, which
// carry no number. A stage of parallel steps is one entry, never one per step.
type Stage struct {
	Number int         `json:"number,omitempty"`
	Kind   StageKind   `json:"kind"`
	Label  string      `json:"label"`
	State  StepState   `json:"state"`
	Steps  []StageStep `json:"steps,omitempty"`
}

// Stages projects the numbered stage list from the admitted revisions, in the
// order they were committed, and the goal state. Numbering is the longest chain
// of blockers: a step with no blocker is in stage 1 and any other step is one
// stage past its deepest blocker. A done stage keeps its number and its steps
// and never reopens, so a step admitted later is placed at least one stage past
// the leading run of done stages. The same revisions always give the same list.
func Stages(revisions []Revision, state goal.GoalState, language design.Language) ([]Stage, error) {
	if state == goal.GoalDone {
		return []Stage{{Kind: KindDone, Label: design.Text(language, "progress.done"), State: StepDone}}, nil
	}
	if len(revisions) == 0 {
		if state == goal.GoalDefining {
			return []Stage{{Kind: KindDesign, Label: design.Text(language, "progress.design"), State: StepNext}}, nil
		}
		return []Stage{checkingEntry(state, language)}, nil
	}
	states := map[string]StepState{}
	names := map[string]string{}
	blockers := map[string][]string{}
	// placed is the sticky stage of every step ever admitted. A revision only
	// moves a step that has not started and whose stage is still open, so a done
	// stage keeps its number and its steps.
	placed := map[string]int{}
	done := 0
	for _, revision := range revisions {
		depth, err := stepDepths(revision.Steps)
		if err != nil {
			return nil, err
		}
		present := make(map[string]bool, len(revision.Steps))
		for _, step := range revision.Steps {
			present[step.ID] = true
			states[step.ID] = step.State
			names[step.ID] = step.Name
			blockers[step.ID] = step.DependsOn
		}
		for id := range names {
			if !present[id] {
				states[id] = StepSuperseded
			}
		}
		ordered := append([]Step(nil), revision.Steps...)
		sort.SliceStable(ordered, func(i, j int) bool { return depth[ordered[i].ID] < depth[ordered[j].ID] })
		for _, step := range ordered {
			if number, ok := placed[step.ID]; ok && number <= done {
				continue
			}
			number := depth[step.ID]
			if number <= done {
				number = done + 1
			}
			// A clamped step pushes its dependents past it in the same revision.
			for _, blocker := range step.DependsOn {
				if below, ok := placed[blocker]; ok && below >= number {
					number = below + 1
				}
			}
			placed[step.ID] = number
		}
		done = doneRun(placed, states)
	}
	// A step in the ledger's ready stage is waiting only while its blockers have
	// landed; otherwise it belongs to a later stage that has not started.
	for id, stepState := range states {
		if stepState != StepWaiting {
			continue
		}
		for _, blocker := range blockers[id] {
			if landed := states[blocker]; landed != StepDone && landed != StepSuperseded {
				states[id] = StepNext
			}
		}
	}
	highest := 0
	members := map[int][]string{}
	for id, number := range placed {
		members[number] = append(members[number], id)
		if number > highest {
			highest = number
		}
	}
	started := map[string]time.Time{}
	for _, revision := range revisions {
		for _, step := range revision.Steps {
			started[step.ID] = step.StartedAt
		}
	}
	out := make([]Stage, 0, highest+1)
	for number := 1; number <= highest; number++ {
		ids := members[number]
		sort.Slice(ids, func(i, j int) bool {
			if !started[ids[i]].Equal(started[ids[j]]) {
				return started[ids[i]].Before(started[ids[j]])
			}
			return ids[i] < ids[j]
		})
		entry := Stage{Number: number, Kind: KindStage, State: StepDone}
		first, live := "", 0
		for _, id := range ids {
			stepState := states[id]
			entry.Steps = append(entry.Steps, StageStep{ID: id, Name: names[id], State: stepState, StartedAt: started[id]})
			if stepState == StepSuperseded {
				continue
			}
			if first == "" {
				first, entry.State = id, stepState
			}
			live++
			if urgency(stepState) < urgency(entry.State) {
				entry.State = stepState
			}
		}
		if first == "" {
			first = ids[0]
		}
		entry.Label = names[first]
		if live > 1 {
			entry.Label = fmt.Sprintf(design.Text(language, "stage.more"), names[first], live-1)
		}
		out = append(out, entry)
	}
	return append(out, checkingEntry(state, language)), nil
}

func checkingEntry(state goal.GoalState, language design.Language) Stage {
	entry := Stage{Kind: KindChecking, Label: design.Text(language, "progress.checking"), State: StepNext}
	if state == goal.GoalChecking {
		entry.State = StepWorking
	}
	return entry
}

// urgency orders entry states from the most to the least urgent step, as the
// design lists them. Next is last: an entry reads next only when none of its
// steps is ready or started.
func urgency(state StepState) int {
	switch state {
	case StepNeedsInput:
		return 0
	case StepFailed:
		return 1
	case StepStopped:
		return 2
	case StepWorking:
		return 3
	case StepWaiting:
		return 4
	case StepUnknown:
		return 5
	case StepDone:
		return 6
	case StepNext:
		return 7
	case StepSuperseded:
		return 8
	default:
		return 9
	}
}

// doneRun counts the leading numbered stages whose every step landed or was
// closed as superseded.
func doneRun(placed map[string]int, states map[string]StepState) int {
	run := 0
	for {
		members := 0
		closed := true
		for id, number := range placed {
			if number != run+1 {
				continue
			}
			members++
			if state := states[id]; state != StepDone && state != StepSuperseded {
				closed = false
			}
		}
		if members == 0 || !closed {
			return run
		}
		run++
	}
}

// stepDepths numbers each step by its longest chain of blockers.
func stepDepths(steps []Step) (map[string]int, error) {
	byID := make(map[string]Step, len(steps))
	for _, step := range steps {
		if step.ID == "" {
			return nil, ErrStageGraph
		}
		if _, exists := byID[step.ID]; exists {
			return nil, ErrStageGraph
		}
		byID[step.ID] = step
	}
	for _, step := range steps {
		for _, blocker := range step.DependsOn {
			if _, ok := byID[blocker]; !ok {
				return nil, ErrStageGraph
			}
		}
	}
	const visiting, settled = 1, 2
	mark := make(map[string]int, len(steps))
	depth := make(map[string]int, len(steps))
	var visit func(string) (int, error)
	visit = func(id string) (int, error) {
		switch mark[id] {
		case settled:
			return depth[id], nil
		case visiting:
			return 0, ErrStageGraph
		}
		mark[id] = visiting
		value := 1
		for _, blocker := range byID[id].DependsOn {
			below, err := visit(blocker)
			if err != nil {
				return 0, err
			}
			if below+1 > value {
				value = below + 1
			}
		}
		mark[id], depth[id] = settled, value
		return value, nil
	}
	for _, step := range steps {
		if _, err := visit(step.ID); err != nil {
			return nil, err
		}
	}
	return depth, nil
}

// GoalStages reads one goal's committed step graph revisions and the observed
// state of its tasks from the work ledger. Each revision carries the task states
// recorded with it, and the newest observation comes from the current goal
// record, so two reads of the same ledger give the same list after a restart.
func GoalStages(ctx context.Context, tx *canon.Tx, goalID, person string, language design.Language) ([]Stage, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT body FROM work_records WHERE kind='goal' AND id=?", goalID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, goal.GraphUnavailable
	}
	if err != nil {
		return nil, err
	}
	var current goal.Goal
	if err = json.Unmarshal(raw, &current); err != nil {
		return nil, err
	}
	if current.Person != person {
		return nil, goal.GraphUnavailable
	}
	graphs, err := stepGraphRevisions(ctx, tx, goalID)
	if err != nil {
		return nil, err
	}
	if current.StepGraph != nil && (len(graphs) == 0 || graphs[len(graphs)-1].Graph.Revision != current.StepGraph.Revision) {
		graphs = append(graphs, graphRevision{Graph: *current.StepGraph, Tasks: current.Tasks})
	}
	if len(graphs) == 0 {
		return Stages(nil, current.State, language)
	}
	revisions := make([]Revision, len(graphs))
	for i, graph := range graphs {
		revisions[i] = Revision{Steps: graphSteps(graph.Graph.Steps, graph.Tasks)}
	}
	// The current record holds the newest task states, including any movement
	// after the last revision was committed.
	last := graphs[len(graphs)-1]
	revisions[len(revisions)-1].Steps = graphSteps(last.Graph.Steps, current.Tasks)
	started, err := cardStarts(ctx, tx, goalID)
	if err != nil {
		return nil, err
	}
	for i := range revisions {
		for j := range revisions[i].Steps {
			revisions[i].Steps[j].StartedAt = started[revisions[i].Steps[j].ID]
		}
	}
	return Stages(revisions, current.State, language)
}

// cardStarts reads when each step of this goal started, which orders a parallel
// stage's label. A step that has not started has no start and sorts by id.
func cardStarts(ctx context.Context, tx *canon.Tx, goalID string) (map[string]time.Time, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM work_cards ORDER BY task_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var raw []byte
		var card work.Card
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &card) != nil {
			return nil, ErrStageGraph
		}
		if card.Task.GoalID == goalID && !card.StartedAt.IsZero() {
			out[card.Task.ID] = card.StartedAt
		}
	}
	return out, rows.Err()
}

// graphSteps joins the manager-owned step list to the task states recorded with
// it. A step with no task yet is unknown, never done.
func graphSteps(steps []goal.Step, tasks []goal.Task) []Step {
	observed := make(map[string]goal.TaskStage, len(tasks))
	for _, task := range tasks {
		observed[task.ID] = task.Stage
	}
	out := make([]Step, len(steps))
	for i, step := range steps {
		out[i] = Step{ID: step.ID, Name: step.Name, DependsOn: step.DependsOn, State: StepUnknown}
		if stage, ok := observed[step.ID]; ok {
			out[i].State = stepState(stage)
		}
	}
	return out
}

// graphRevision is one admitted revision with the task states committed with it.
type graphRevision struct {
	Graph goal.StepGraph
	Tasks []goal.Task
}

// stepGraphRevisions reads the whole revisions the manager submitted, in commit
// order. Each step graph event journals the goal record that carries it.
func stepGraphRevisions(ctx context.Context, tx *canon.Tx, goalID string) ([]graphRevision, error) {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM work_events WHERE kind='goal.step_graph' ORDER BY seq")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var graphs []graphRevision
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var entry struct {
			Changes []struct {
				Kind string          `json:"kind"`
				ID   string          `json:"id"`
				Body json.RawMessage `json:"body"`
			} `json:"changes"`
		}
		if json.Unmarshal(raw, &entry) != nil {
			return nil, ErrStageGraph
		}
		for _, change := range entry.Changes {
			if change.Kind != "goal" || change.ID != goalID {
				continue
			}
			var g goal.Goal
			if json.Unmarshal(change.Body, &g) != nil {
				return nil, ErrStageGraph
			}
			if g.StepGraph != nil {
				graphs = append(graphs, graphRevision{Graph: *g.StepGraph, Tasks: g.Tasks})
			}
		}
	}
	return graphs, rows.Err()
}

// stepState maps a task's execution stage to the step state the person sees.
// A merged pull request has landed, so it reads done even before its check.
func stepState(stage goal.TaskStage) StepState {
	switch stage {
	case goal.TaskReady:
		return StepWaiting
	case goal.TaskBuilding, goal.TaskDelivered:
		return StepWorking
	case goal.TaskMerged, goal.TaskDone:
		return StepDone
	case goal.TaskNeedsPerson:
		return StepNeedsInput
	case goal.TaskConflicted, goal.TaskWorkerLost, goal.TaskNoReceipt, goal.TaskQAFailed:
		return StepFailed
	default:
		return StepUnknown
	}
}
