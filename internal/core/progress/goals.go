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
		u := Unit{ID: g.ID, Kind: "goal", Name: name, Stages: stages, Active: true, Percent: goalPercent(g), Waiting: goalWaiting(g), Now: goalNow(g, stages, language), Next: goalNext(stages), Rank: g.Rank}
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
	// The person's pause comes before a step phrase: a paused goal starts no new
	// step, and its in-progress step ends its current turn.
	if g.Paused {
		return design.Text(language, "progress.paused")
	}
	if working := stepsInState(stages, StepWorking); len(working) > 0 {
		if len(working) > 1 {
			return fmt.Sprintf(design.Text(language, "stage.more"), working[0].Name, len(working)-1)
		}
		return working[0].Name
	}
	// A goal waiting for a run slot says why and which place it holds.
	if place := queuePlace(g, language); place != "" {
		return place
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

// queuePlace is a queued goal's one reason and its place in rank order.
func queuePlace(g goal.Goal, language design.Language) string {
	if g.QueuePlace <= 0 {
		return ""
	}
	text, ok := design.Lookup(language, "progress.queue."+g.QueueReason)
	if !ok {
		return ""
	}
	return fmt.Sprintf(text, g.QueuePlace)
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
