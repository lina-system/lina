// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
)

// Goals projects each goal with work in flight or something waiting on the
// person into the line the person sees. The line carries Now, its start and its
// stage list, read from the work ledger in the same snapshot; the title comes
// from the goal's canonical record, because the execution ledger never holds
// the goal's prose. The same ledger and record therefore give the same line
// after a restart.
func Goals(ctx context.Context, tx *canon.Tx, person string, agents map[string]string, title func(string) string) ([]Unit, error) {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM work_records WHERE kind='goal' ORDER BY id")
	if err != nil {
		return nil, err
	}
	var goals []goal.Goal
	for rows.Next() {
		var raw []byte
		var g goal.Goal
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal(raw, &g); err != nil {
			rows.Close()
			return nil, err
		}
		goals = append(goals, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	units := make([]Unit, 0, len(goals))
	for _, g := range goals {
		if g.Person != "" && g.Person != person {
			continue
		}
		if _, ok := agents[g.Agent]; !ok {
			continue
		}
		// A finished or stopped goal leaves the lines; a new run makes a new goal.
		if g.State == goal.GoalDone || g.State == goal.GoalStopped {
			continue
		}
		name := ""
		if title != nil {
			name = title(g.ID)
		}
		language := design.ConversationLanguage(name, design.English)
		stages, err := GoalStages(ctx, tx, g.ID, person, language)
		if err != nil {
			return nil, err
		}
		// One manager runs one 일, so a managed goal is the 일 the person reads.
		u := Unit{ID: g.ID, Kind: "project", Name: name, Stages: stages, Active: true, Percent: goalPercent(g), Waiting: goalWaiting(g), Now: goalNow(g, stages, language), Next: goalNext(stages), State: goalState(g, stages)}
		if u.Name == "" {
			u.Name = design.Text(language, "progress.untitled")
		}
		// The hand-over instant is the line's start. Legacy goals with no
		// recorded instant stay unknown rather than claiming a later time.
		if g.CreatedAt != 0 {
			u.StartedAt = time.Unix(g.CreatedAt, 0)
		}
		units = append(units, u)
	}
	return units, nil
}

// goalPercent is the share of the goal's 일 that are done, each counted
// equally. An empty denominator is unobserved, never zero.
func goalPercent(g goal.Goal) *int {
	if len(g.Tasks) == 0 {
		return nil
	}
	done := 0
	for _, task := range g.Tasks {
		if task.Stage == goal.TaskDone {
			done++
		}
	}
	percent := done * 100 / len(g.Tasks)
	return &percent
}

// goalWaiting marks a goal whose own row or one of its 일 waits on the person.
func goalWaiting(g goal.Goal) int {
	if g.State == goal.GoalBlocked || g.AttentionID != "" {
		return 1
	}
	for _, task := range g.Tasks {
		if task.Stage == goal.TaskNeedsPerson {
			return 1
		}
	}
	return 0
}

// goalNow is what the goal is doing right now, in the order the design gives:
// a step that needs the person, the working step with the number of the others,
// then the stage word. Per-step start instants order the choice and never come
// from a worker's own claim.
func goalNow(g goal.Goal, stages []Stage, language design.Language) string {
	if step, ok := firstStepInState(stages, StepNeedsInput); ok {
		return design.Text(language, "progress.needs_you") + " · " + step.Name
	}
	if g.State == goal.GoalBlocked || g.AttentionID != "" {
		return design.Text(language, "progress.needs_you")
	}
	if working := stepsInState(stages, StepWorking); len(working) > 0 {
		if len(working) > 1 {
			return fmt.Sprintf(design.Text(language, "stage.more"), working[0].Name, len(working)-1)
		}
		return working[0].Name
	}
	switch g.State {
	case goal.GoalDefining:
		return design.Text(language, "progress.design")
	case goal.GoalChecking:
		return design.Text(language, "progress.checking")
	case goal.GoalDone:
		return design.Text(language, "progress.done")
	case goal.GoalRunning, goal.GoalWaiting, goal.GoalStopped, goal.GoalBlocked:
		// A blocked goal already returned above; the others wait on their
		// queue place or on the person, which the line's own state carries.
		return design.Text(language, "progress.waiting_word")
	}
	return design.Text(language, "progress.waiting_word")
}

// goalState is the 일 line's state in the running list words, which the count
// reads. It is the most urgent step state in the order the stage list uses —
// input needed, failed, stopped, working, waiting — then the goal's own state.
// A line says landed only when the goal is done.
func goalState(g goal.Goal, stages []Stage) string {
	if g.State == goal.GoalBlocked || g.AttentionID != "" {
		return "needs_input"
	}
	for _, observed := range []struct {
		state StepState
		word  string
	}{
		{StepNeedsInput, "needs_input"},
		{StepFailed, "failed"},
		{StepStopped, "stopped"},
		{StepWorking, "in_progress"},
		{StepWaiting, "waiting"},
	} {
		if len(stepsInState(stages, observed.state)) > 0 {
			return observed.word
		}
	}
	switch g.State {
	case goal.GoalDone:
		return "done"
	case goal.GoalChecking:
		return "ready_for_review"
	case goal.GoalDefining, goal.GoalRunning, goal.GoalWaiting, goal.GoalStopped, goal.GoalBlocked:
		// A blocked or stopped line is caught above or reads as waiting; neither
		// invents a plan outcome the ledger does not hold.
		return "waiting"
	}
	return "unknown"
}

// goalNext names the entry after the current one. The current entry is what the
// line's stage already says, so a goal in 확인 has no next they can be told.
func goalNext(stages []Stage) string {
	for i, entry := range stages {
		if entry.State == StepDone {
			continue
		}
		if i+1 < len(stages) {
			return stages[i+1].Label
		}
		return ""
	}
	return ""
}

// stepsInState lists the steps of an observed state, earliest start first and
// then by name, which is the order the design uses for a step phrase.
func stepsInState(stages []Stage, state StepState) []StageStep {
	out := []StageStep{}
	for _, entry := range stages {
		for _, step := range entry.Steps {
			if step.State == state {
				out = append(out, step)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func firstStepInState(stages []Stage, state StepState) (StageStep, bool) {
	steps := stepsInState(stages, state)
	if len(steps) == 0 {
		return StageStep{}, false
	}
	return steps[0], true
}
