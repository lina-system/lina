// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work/goal"
)

// TestGoalLineCarriesItsLineState checks that an 일 line reaches the running
// count with a state in the running-list words: the most urgent step state,
// then the goal's own state. The count reads these states, so a line without
// one cannot be counted.
func TestGoalLineCarriesItsLineState(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	steps := []goal.Step{
		{ID: "one", Name: "First", Done: "First is done"},
		{ID: "two", Name: "Second", Done: "Second is done"},
	}
	goalRecord := func(id string, state goal.GoalState, attention string, stages ...goal.TaskStage) goal.Goal {
		g := goal.Goal{ID: id, Person: "person", Agent: "agent", Conversation: "conversation", State: state, AttentionID: attention,
			StepGraph: &goal.StepGraph{GoalID: id, Revision: 1, Steps: steps}}
		for i, stage := range stages {
			g.Tasks = append(g.Tasks, goal.Task{ID: steps[i].ID, Name: steps[i].Name, Stage: stage})
		}
		return g
	}
	goals := []goal.Goal{
		goalRecord("working", goal.GoalRunning, "", goal.TaskBuilding, goal.TaskBuilding),
		goalRecord("needs", goal.GoalRunning, "", goal.TaskBuilding, goal.TaskNeedsPerson),
		goalRecord("queued", goal.GoalRunning, "", goal.TaskReady, goal.TaskReady),
		goalRecord("blocked", goal.GoalBlocked, "attention:1", goal.TaskReady, goal.TaskReady),
		goalRecord("checking", goal.GoalChecking, "", goal.TaskDone, goal.TaskDone),
		goalRecord("failed", goal.GoalRunning, "", goal.TaskBuilding, goal.TaskQAFailed),
	}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		for _, g := range goals {
			raw, err := json.Marshal(g)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var units []Unit
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		var err error
		units, err = Goals(t.Context(), tx, "person", map[string]string{"agent": "LINA"}, func(string) string { return "" })
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, u := range units {
		got[u.ID] = u.State
	}
	for id, want := range map[string]string{
		"working":  "in_progress",
		"needs":    "needs_input",
		"queued":   "waiting",
		"blocked":  "needs_input",
		"checking": "ready_for_review",
		"failed":   "failed",
	} {
		if got[id] != want {
			t.Errorf("line %s state = %q, want %q (all: %+v)", id, got[id], want, got)
		}
	}
}
