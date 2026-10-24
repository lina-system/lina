// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
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
