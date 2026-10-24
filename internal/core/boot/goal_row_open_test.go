// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Opening a goal-task row must read the result of the worker card or background
// run that row replaced. The running list no longer publishes those sources.
func TestOpenGoalRowReadsOwningTaskResult(t *testing.T) {
	r, _ := turnRuntime(t)
	const (
		goalID       = "trip"
		workerTask   = "note"
		workerAction = "write-note"
		runTask      = "research"
		runAction    = "read-guide"
		runID        = "0123456789abcdef0123456789abcdef"
	)
	g := goal.Goal{
		ID: goalID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn:trip",
		State: goal.GoalRunning,
		Tasks: []goal.Task{
			{ID: workerTask, Name: "Write the note", Kind: goal.TaskStep, Stage: goal.TaskDone, Attempt: 1, Done: "The note is written"},
			{ID: runTask, Name: "Read the guide", Kind: goal.BackgroundStep, Stage: goal.TaskDone, Attempt: 1, Done: "The guide is read"},
		},
	}
	actions := []goal.Action{
		{ID: "old-note", GoalID: goalID, TaskID: workerTask, Kind: goal.Assign, Attempt: 1, State: "finished", Superseded: true},
		{ID: workerAction, GoalID: goalID, TaskID: workerTask, Kind: goal.Assign, Attempt: 1, State: "finished"},
		{ID: "resume-note", GoalID: goalID, TaskID: workerTask, Kind: goal.Resume, Attempt: 1, Sequence: 2, State: "queued"},
		{ID: runAction, GoalID: goalID, TaskID: runTask, Kind: goal.Assign, Attempt: 1, State: "finished"},
	}
	cardID := work.DispatchID(g.Turn, workerAction)
	card := work.Card{
		Task:   work.Task{ID: cardID, PersonID: r.person, AgentID: r.agent, Title: "Write the note"},
		State:  "done",
		Run:    &work.Run{ID: cardID + ":run", Generation: 2},
		Stages: []work.Stage{{Name: "draft", Source: "core", Summary: "The note is ready"}},
	}
	origin := runs.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: g.Turn, Epoch: r.epoch}
	run := runs.Record{
		ID: runID, Origin: origin, State: runs.Landed, Outcome: runs.Complete, Noticed: true,
		StartKey: runs.CallKey(origin, runAction),
		Brief:    runs.Brief{Goal: "Read the guide"},
		Material: runs.Ref{ID: runID, Revision: 1},
	}
	body, err := json.Marshal(struct{ Artifact runs.Artifact }{runs.Artifact{Body: "guide findings", Done: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.assets.SaveRunResult(t.Context(), runID, body, assets.Actor{PersonID: r.person, AgentID: r.agent}); err != nil {
		t.Fatal(err)
	}
	dispatchRecord := dispatch.Record{ID: cardID, Origin: dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: g.Turn}, Goal: true, GoalAction: workerAction, State: "finished", Definition: dispatch.Definition{Repository: "/tmp/goal-row-open"}}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		raw, err := json.Marshal(g)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", g.ID, raw); err != nil {
			return err
		}
		for _, action := range actions {
			raw, err = json.Marshal(action)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('action',?,1,?)", action.ID, raw); err != nil {
				return err
			}
		}
		raw, err = json.Marshal(card)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO work_cards(task_id,record) VALUES(?,?)", cardID, raw); err != nil {
			return err
		}
		raw, err = json.Marshal(dispatchRecord)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", cardID, g.Turn, dispatchRecord.Definition.Repository, dispatchRecord.State, raw); err != nil {
			return err
		}
		raw, err = json.Marshal(run)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), "INSERT INTO background_runs(id,record) VALUES(?,?)", runID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	other := goal.Goal{ID: "other-goal", Person: "someone-else", Agent: r.agent, State: goal.GoalRunning, Tasks: []goal.Task{{ID: "step", Name: "Other work", Stage: goal.TaskDone, Done: "Left alone"}}}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		raw, err := json.Marshal(other)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), "INSERT INTO work_records(kind,id,version,body) VALUES('goal',?,1,?)", other.ID, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := r.background.list.Build(t.Context(), r.person)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[runs.ListRef]bool{}
	for _, entry := range snapshot.Entries {
		seen[entry.Ref] = true
	}
	if !seen[runs.ListRef{Source: "goal", ID: goalTaskKey(goalID, workerTask)}] || !seen[runs.ListRef{Source: "goal", ID: goalTaskKey(goalID, runTask)}] {
		t.Fatalf("goal rows missing: %+v", snapshot.Entries)
	}
	if seen[runs.ListRef{Source: "work", ID: cardID + ":2"}] || seen[runs.ListRef{Source: "run", ID: runID}] {
		t.Fatalf("replaced rows still published: %+v", snapshot.Entries)
	}

	note, err := openRunningRow(t, r, "goal:"+goalTaskKey(goalID, workerTask))
	if err != nil || !strings.Contains(note, "The note is ready") {
		t.Fatalf("worker result: %q %v", note, err)
	}
	guide, err := openRunningRow(t, r, "goal:"+goalTaskKey(goalID, runTask))
	if err != nil || !strings.Contains(guide, "guide findings") {
		t.Fatalf("run result: %q %v", guide, err)
	}
	if _, err = openRunningRow(t, r, "goal:"+goalTaskKey(other.ID, "step")); !errors.Is(err, runs.ErrScope) {
		t.Fatalf("other person's goal: %v", err)
	}
}

func openRunningRow(t *testing.T, r *conversationRuntime, id string) (string, error) {
	t.Helper()
	action := "open"
	result, err := r.identity.Operations[protocol.KindRunningGet].Handle(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{Payload: protocol.RunningGet{RunID: &id, Action: &action}})
	if err != nil {
		return "", err
	}
	text := result.Payload.(protocol.Running).Text
	if text == nil {
		return "", errors.New("missing result text")
	}
	return *text, nil
}
