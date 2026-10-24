// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
)

// A client sends work commands with the goal Core names on a goal line. Any
// other card, such as a task handed off on its own, names no goal, so the
// client cannot send its ref as one.
func TestOwnerProgressNamesTheGoalOfGoalLinesOnly(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	g := goal.Goal{ID: "named-goal", Person: r.person, Agent: r.agent, State: goal.GoalRunning,
		StepGraph: &goal.StepGraph{GoalID: "named-goal", Revision: 1, Steps: []goal.Step{{ID: "draft", Name: "Draft"}}},
		Tasks:     []goal.Task{{ID: "draft", Stage: goal.TaskReady}}}
	card := work.Card{Task: work.Task{ID: "own-task", PersonID: r.person, AgentID: r.agent, Title: "Read notes"}, State: "input_needed"}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		raw, _ := json.Marshal(g)
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw); err != nil {
			return err
		}
		raw, _ = json.Marshal(card)
		_, err := tx.ExecContext(t.Context(), "INSERT INTO work_cards(task_id,record) VALUES(?,?)", card.Task.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p, err := r.ownerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range *p.Rows {
		seen[row.Ref] = true
		switch row.Ref {
		case g.ID:
			if row.GoalID == nil || *row.GoalID != g.ID {
				t.Fatalf("goal line names goal %v, want %s", row.GoalID, g.ID)
			}
		default:
			if row.GoalID != nil {
				t.Fatalf("card %s names goal %s", row.Ref, *row.GoalID)
			}
		}
	}
	if !seen[g.ID] || !seen[card.Task.ID] {
		t.Fatalf("rows = %+v", *p.Rows)
	}
}
