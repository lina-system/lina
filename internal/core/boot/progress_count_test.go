// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestProgressCountFollowsLinesNotRows publishes three 일 that hold four steps
// each and one background run outside any 일. The wire count is four lines by
// state while the rows are thirteen, so a client that counted rows would show a
// different number from Core.
func TestProgressCountFollowsLinesNotRows(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	steps := func(prefix string) []goal.Step {
		out := make([]goal.Step, 0, 4)
		for i := range 4 {
			out = append(out, goal.Step{ID: fmt.Sprintf("%s-%d", prefix, i), Name: fmt.Sprintf("%s step %d", prefix, i)})
		}
		return out
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		for _, id := range []string{"day-a", "day-b", "day-c"} {
			graph := steps(id)
			g := goal.Goal{ID: id, Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn:" + id, State: goal.GoalRunning, CreatedAt: 1_700_000_000,
				StepGraph: &goal.StepGraph{GoalID: id, Revision: 1, Steps: graph}}
			for _, step := range graph {
				g.Tasks = append(g.Tasks, goal.Task{ID: step.ID, Name: step.Name, Stage: goal.TaskBuilding})
			}
			raw, err := json.Marshal(g)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", id, raw); err != nil {
				return err
			}
			for _, step := range graph {
				// The step's worker action dispatches the work, and the card
				// carries the dispatch's id, as runGoalWorker leaves it.
				action := goal.Action{ID: "assign:" + step.ID, GoalID: id, TaskID: step.ID, Kind: goal.Assign, State: "running"}
				body, err := json.Marshal(action)
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('action',?,1,?)", action.ID, body); err != nil {
					return err
				}
				card := work.DispatchID(g.Turn, action.ID)
				task := work.Task{ID: card, PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: step.Name}
				run := work.Run{ID: card + ":run", TaskID: card, Generation: 1, Worker: "fixture", ThreadID: card, TurnID: card, NodeID: "local"}
				events := []work.Event{
					{Key: card + ":create", TaskID: card, Kind: "created", Task: &task},
					{Key: card + ":start", TaskID: card, Kind: "started", Run: &run},
				}
				projected, err := work.Project(events)
				if err != nil {
					return err
				}
				if body, err = json.Marshal(projected); err != nil {
					return err
				}
				if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_cards(task_id,record) VALUES(?,?)", card, body); err != nil {
					return err
				}
				for _, event := range events {
					line, err := json.Marshal(event)
					if err != nil {
						return err
					}
					if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_ledger(task_id,event_key,record) VALUES(?,?,?)", card, event.Key, line); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := r.ownerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.Rows == nil || len(*p.Rows) != 15 {
		t.Fatalf("rows = %d, want the three 일 lines and their twelve step rows: %+v", len(*p.Rows), p.Rows)
	}
	rows := 0
	for _, row := range *p.Rows {
		if row.Line.Row != nil && *row.Line.Row {
			rows++
		}
	}
	if rows != 12 {
		t.Fatalf("step rows = %d, want the twelve steps marked as rows", rows)
	}
	if p.Counts == nil {
		t.Fatal("progress carries no count")
	}
	if running, waiting := p.Counts.Running.String(), p.Counts.Waiting.String(); running != "3" || waiting != "0" {
		t.Fatalf("count = %s running, %s waiting; want the three 일 lines", running, waiting)
	}
	// The same count on the topic, so a client that only subscribes sees it.
	if err = r.publishOwnerViews(t.Context()); err != nil {
		t.Fatal(err)
	}
	start, sub, err := r.topics.Subscribe("progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if len(start.Snapshot.Items) != 1 {
		t.Fatalf("progress snapshot items = %d", len(start.Snapshot.Items))
	}
	var update protocol.ProgressUpdate
	if err = json.Unmarshal(start.Snapshot.Items[0].Data, &update); err != nil {
		t.Fatal(err)
	}
	if update.Counts == nil || update.Counts.Running != "3" || update.Counts.Waiting != "0" {
		t.Fatalf("published count = %+v", update.Counts)
	}
}
