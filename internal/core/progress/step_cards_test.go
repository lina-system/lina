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
// count holds each goal once, and each step starts when its card started. Two
// goals that name the same step id keep their own cards, because each goal's
// actions dispatch in its own turn. A card handed off outside any goal stays a
// line of its own.
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
	h := goal.Goal{ID: "h", Person: "person", Agent: "agent", Conversation: "conversation", Turn: "turn-h", State: goal.GoalRunning,
		StepGraph: &goal.StepGraph{GoalID: "h", Revision: 1, Steps: steps[:1]}, Tasks: []goal.Task{{ID: "one", Name: "First", Stage: goal.TaskBuilding}}}
	// After a restart, the second step's worker is observed by a new action that
	// names the one that dispatched it; both lead to the same card.
	actions := []goal.Action{
		{ID: "assign-one", GoalID: "g", TaskID: "one", Kind: goal.Assign, State: "running"},
		{ID: "assign-two", GoalID: "g", TaskID: "two", Kind: goal.Assign, State: "finished", Superseded: true},
		{ID: "observe-two", GoalID: "g", TaskID: "two", Kind: goal.Assign, State: "running", ObserveActionID: "assign-two"},
		{ID: "inspect-one", GoalID: "g", TaskID: "one", Kind: goal.InspectPR, State: "queued"},
		{ID: "assign-h-one", GoalID: "h", TaskID: "one", Kind: goal.Assign, State: "running"},
	}
	started := map[string]time.Time{
		work.DispatchID(g.Turn, "assign-one"):   time.Unix(2_000, 0).UTC(),
		work.DispatchID(g.Turn, "assign-two"):   time.Unix(1_000, 0).UTC(),
		work.DispatchID(h.Turn, "assign-h-one"): time.Unix(1_500, 0).UTC(),
		"loose":                                 time.Unix(3_000, 0).UTC(),
	}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		for _, each := range []goal.Goal{g, h} {
			raw, err := json.Marshal(each)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", each.ID, raw); err != nil {
				return err
			}
		}
		for _, a := range actions {
			raw, err := json.Marshal(a)
			if err != nil {
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
			raw, err := json.Marshal(card)
			if err != nil {
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
	parent := map[string]string{}
	line := ""
	for _, entry := range entries {
		byRef[entry.Ref] = entry
		if entry.Line.Depth == 0 {
			line = entry.Ref
			continue
		}
		parent[entry.Ref] = line
	}
	for card, owner := range map[string]string{
		work.DispatchID(g.Turn, "assign-one"):   "g",
		work.DispatchID(g.Turn, "assign-two"):   "g",
		work.DispatchID(h.Turn, "assign-h-one"): "h",
	} {
		if entry, ok := byRef[card]; !ok || !entry.Row || parent[card] != owner {
			t.Errorf("card %s = %+v under %q (present %v), want a row of %s", card, entry, parent[card], ok, owner)
		}
	}
	for _, id := range []string{"g", "h", "loose"} {
		if entry := byRef[id]; entry.Row || entry.Line.Depth != 0 {
			t.Errorf("%s = %+v, want a line", id, entry)
		}
	}
	if len(entries) != 6 {
		t.Errorf("entries = %d, want two goals, their three cards and the loose card: %+v", len(entries), entries)
	}
	if got := CountLines(entries); got != (Count{Running: 3}) {
		t.Errorf("count = %+v, want the two goals and the loose card", got)
	}

	// A step starts with its card, both for one goal and for the overview,
	// which reads every goal's cards at once.
	starts := func(list []Stage) map[string]time.Time {
		out := map[string]time.Time{}
		for _, entry := range list {
			for _, step := range entry.Steps {
				out[step.ID] = step.StartedAt
			}
		}
		return out
	}
	want := map[string]map[string]time.Time{
		"g": {"one": time.Unix(2_000, 0).UTC(), "two": time.Unix(1_000, 0).UTC()},
		"h": {"one": time.Unix(1_500, 0).UTC()},
	}
	got := map[string]map[string]time.Time{"g": starts(stages)}
	for _, u := range units {
		if u.ID == "h" {
			got["h"] = starts(u.Stages)
		}
	}
	for id, steps := range want {
		for step, at := range steps {
			if !got[id][step].Equal(at) {
				t.Errorf("%s step %s started %v, want its card's start %v", id, step, got[id][step], at)
			}
		}
	}
}
