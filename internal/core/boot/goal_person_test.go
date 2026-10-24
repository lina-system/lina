// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
)

// TestGoalStepCardHasNoSecondRow keeps one running-list row per goal task. The
// step's card is joined to the goal through its worker action, as progress
// joins it, so the card stays off the list before its dispatch record is
// marked as a goal's. A card no goal dispatched keeps its row.
func TestGoalStepCardHasNoSecondRow(t *testing.T) {
	r := controlRuntime(t)
	g := goal.Goal{ID: "trip", Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn:trip", State: goal.GoalRunning,
		Tasks: []goal.Task{{ID: "book", Name: "Book travel", Stage: goal.TaskBuilding}}}
	action := goal.Action{ID: "assign:book", GoalID: g.ID, TaskID: "book", Kind: goal.Assign, State: "running"}
	step := work.DispatchID(g.Turn, action.ID)
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		for kind, record := range map[string]any{"goal": g, "action": action} {
			body, err := json.Marshal(record)
			if err != nil {
				return err
			}
			id := g.ID
			if kind == "action" {
				id = action.ID
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES(?,?,1,?)", kind, id, body); err != nil {
				return err
			}
		}
		for id, title := range map[string]string{step: "Book travel", "loose": "Read the notes"} {
			task := work.Task{ID: id, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: title}
			run := work.Run{ID: id + ":run", TaskID: id, Generation: 1, Worker: "fixture", ThreadID: id, TurnID: id, NodeID: "local"}
			card, err := work.Project([]work.Event{{Key: id + ":create", TaskID: id, Kind: "created", Task: &task}, {Key: id + ":start", TaskID: id, Kind: "started", Run: &run}})
			if err != nil {
				return err
			}
			body, err := json.Marshal(card)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_cards(task_id,record) VALUES(?,?)", id, body); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var listed []string
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		var err error
		listed, err = goalQuietWork{inner: runs.WorkListSource{Person: r.person}}.Candidates(t.Context(), tx, r.person)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(listed, func(id string) bool { return strings.HasPrefix(id, step+":") }) {
		t.Fatalf("work rows = %v, the goal step's card is listed beside its goal-task row", listed)
	}
	if !slices.ContainsFunc(listed, func(id string) bool { return strings.HasPrefix(id, "loose:") }) {
		t.Fatalf("work rows = %v, the card no goal dispatched lost its row", listed)
	}
}

func goalTaskListDB(t *testing.T) *canon.Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: ApplicationMigrations(), NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db
}

func saveListGoal(t *testing.T, db *canon.Store, g goal.Goal) {
	t.Helper()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT OR REPLACE INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func listGoal(id, person string, tasks int) goal.Goal {
	g := goal.Goal{ID: id, Person: person, Agent: "agent", State: goal.GoalRunning}
	for i := range tasks {
		g.Tasks = append(g.Tasks, goal.Task{ID: fmt.Sprintf("s%d", i), Name: fmt.Sprintf("Step %d", i), Stage: goal.TaskBuilding,
			Background: &goal.BackgroundBrief{Goal: "Collect the notes", Scope: "notes", NotTouch: "other work", Done: "Notes exist", Result: "notes"}})
	}
	return g
}

func TestGoalTaskListRowsFollowTheGoalTasks(t *testing.T) {
	db := goalTaskListDB(t)
	mine := listGoal("mine", "person", 3)
	mine.Tasks[1].Stage = goal.TaskDone
	mine.Tasks[2].Stage = goal.TaskNeedsPerson
	saveListGoal(t, db, mine)
	saveListGoal(t, db, listGoal("other", "someone-else", 2))
	list := runs.NewRunningList(db, map[string]runs.ListSource{"goal": goalTaskList{}})
	snapshot, err := list.Build(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, e := range snapshot.Entries {
		states[e.Ref.ID] = e.View.State
	}
	want := map[string]string{goalTaskKey("mine", "s0"): "in_progress", goalTaskKey("mine", "s1"): "done", goalTaskKey("mine", "s2"): "needs_input"}
	if fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("rows = %v, want %v", states, want)
	}
	if snapshot.Working != 1 || snapshot.NeedsPerson != 1 || snapshot.Landed != 1 {
		t.Fatalf("counts = %+v", snapshot)
	}
}

// The running list is rebuilt on every run change and once a second. A goal's
// tasks share one record and its actions one table, so the build must not read
// either again per task.
func TestGoalTaskListBuildIsLinearInTasks(t *testing.T) {
	allocs := func(tasks int) float64 {
		db := goalTaskListDB(t)
		g := listGoal("goal", "person", tasks)
		for i := range g.Tasks {
			g.Tasks[i].Kind = goal.BackgroundStep
			g.Tasks[i].Attempt = 1
			action := goal.Action{ID: fmt.Sprintf("action-%d", i), GoalID: g.ID, TaskID: g.Tasks[i].ID, Kind: goal.Assign, Attempt: 1, StepKind: goal.BackgroundStep, State: "running"}
			raw, err := json.Marshal(action)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('action',?,1,?)", action.ID, raw)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		saveListGoal(t, db, g)
		list := runs.NewRunningList(db, map[string]runs.ListSource{"goal": goalTaskList{}})
		return testing.AllocsPerRun(3, func() {
			snapshot, err := list.Build(context.Background(), "person")
			if err != nil || len(snapshot.Entries) != tasks {
				t.Fatalf("build: %d rows, %v", len(snapshot.Entries), err)
			}
		})
	}
	small, large := allocs(10), allocs(80)
	// Eight times the tasks: linear work allocates about eight times as much,
	// a per-task decode of the whole goal about sixty-four times.
	if large > 20*small {
		t.Fatalf("allocations grew from %.0f to %.0f for 8x the tasks", small, large)
	}
}
