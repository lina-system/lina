// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
)

// TestDispatchedStepCardsAreRowsOfTheirGoal seeds the ledger the way a code goal
// writes it: each step's worker action dispatches work, and the card carries the
// dispatch's id, not the step's. The cards are rows of the goal's 일 line, the
// count holds the goal once, and each step starts when its card started. A card
// handed off outside any goal stays a line of its own.
func TestDispatchedStepCardsAreRowsOfTheirGoal(t *testing.T) {
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

	steps := []goal.Step{{ID: "one", Name: "First"}, {ID: "two", Name: "Second"}}
	g := goal.Goal{ID: "g", Person: "person", Agent: "agent", Conversation: "conversation", Turn: "turn", State: goal.GoalRunning,
		StepGraph: &goal.StepGraph{GoalID: "g", Revision: 1, Steps: steps}}
	for _, step := range steps {
		g.Tasks = append(g.Tasks, goal.Task{ID: step.ID, Name: step.Name, Stage: goal.TaskBuilding})
	}
	// After a restart, the second step's worker is observed by a new action that
	// names the one that dispatched it; both lead to the same card.
	actions := []goal.Action{
		{ID: "assign-one", GoalID: "g", TaskID: "one", Kind: goal.Assign, State: "running"},
		{ID: "assign-two", GoalID: "g", TaskID: "two", Kind: goal.Assign, State: "finished", Superseded: true},
		{ID: "observe-two", GoalID: "g", TaskID: "two", Kind: goal.Assign, State: "running", ObserveActionID: "assign-two"},
		{ID: "inspect-one", GoalID: "g", TaskID: "one", Kind: goal.InspectPR, State: "queued"},
	}
	started := map[string]time.Time{
		work.DispatchID(g.Turn, "assign-one"): time.Unix(2_000, 0).UTC(),
		work.DispatchID(g.Turn, "assign-two"): time.Unix(1_000, 0).UTC(),
		"loose":                               time.Unix(3_000, 0).UTC(),
	}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		raw, err := json.Marshal(g)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw); err != nil {
			return err
		}
		for _, a := range actions {
			if raw, err = json.Marshal(a); err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('action',?,1,?)", a.ID, raw); err != nil {
				return err
			}
		}
		for id, at := range started {
			task := work.Task{ID: id, PersonID: "person", AgentID: "agent", ConversationID: "conversation", Title: "Work " + id}
			run := work.Run{ID: id + ":run", TaskID: id, Generation: 1, Worker: "fixture", ThreadID: id, TurnID: id, NodeID: "local"}
			events := []work.Event{
				{Key: id + ":create", TaskID: id, Kind: "created", Task: &task, RecordedAt: at.Format(time.RFC3339Nano)},
				{Key: id + ":start", TaskID: id, Kind: "started", Run: &run, RecordedAt: at.Format(time.RFC3339Nano)},
			}
			card, err := work.Project(events)
			if err != nil {
				return err
			}
			if raw, err = json.Marshal(card); err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_cards(task_id,record) VALUES(?,?)", id, raw); err != nil {
				return err
			}
			for _, event := range events {
				line, err := json.Marshal(event)
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_ledger(task_id,event_key,record) VALUES(?,?,?)", id, event.Key, line); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var units []Unit
	var stages []Stage
	agents := map[string]string{"agent": "LINA"}
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		cards, err := WorkUnits(t.Context(), tx, "person", agents)
		if err != nil {
			return err
		}
		goals, err := Goals(t.Context(), tx, "person", agents, func(string) string { return "Plan" })
		if err != nil {
			return err
		}
		units = append(cards, goals...)
		stages, err = GoalStages(t.Context(), tx, "g", "person", design.English)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	entries := Compute(units)
	byRef := map[string]Entry{}
	for _, entry := range entries {
		byRef[entry.Ref] = entry
	}
	for _, action := range []string{"assign-one", "assign-two"} {
		id := work.DispatchID(g.Turn, action)
		if entry, ok := byRef[id]; !ok || !entry.Row || entry.Line.Depth != 1 {
			t.Errorf("card of %s = %+v (present %v), want a row of the goal's line", action, entry, ok)
		}
	}
	if entry := byRef["g"]; entry.Row || entry.Line.Depth != 0 {
		t.Errorf("goal line = %+v, want a line", entry)
	}
	if entry := byRef["loose"]; entry.Row || entry.Line.Depth != 0 {
		t.Errorf("loose card = %+v, want a line of its own", entry)
	}
	if len(entries) != 4 {
		t.Errorf("entries = %d, want the goal, its two cards and the loose card: %+v", len(entries), entries)
	}
	if got := CountLines(entries); got != (Count{Running: 2}) {
		t.Errorf("count = %+v, want the goal and the loose card", got)
	}

	want := map[string]time.Time{"one": time.Unix(2_000, 0).UTC(), "two": time.Unix(1_000, 0).UTC()}
	for _, entry := range stages {
		for _, step := range entry.Steps {
			if !step.StartedAt.Equal(want[step.ID]) {
				t.Errorf("step %s started %v, want its card's start %v", step.ID, step.StartedAt, want[step.ID])
			}
		}
	}
}
