// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/work/goal"
)

// A goal row replaces the native run, so the wait that run was showing has to
// stay on the replacement. A task admitted with no name is summarized by its
// done condition.
func TestGoalRowKeepsWaitingState(t *testing.T) {
	r, _ := turnRuntime(t)
	const goalID = "holds"
	origin := runs.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn:holds", Epoch: r.epoch}
	type runWait struct {
		task, action, run, reason, want string
	}
	waits := []runWait{
		{"quota", "act-quota", "0123456789abcdef0123456789abcde1", "usage_capped", "quota"},
		{"signin", "act-signin", "0123456789abcdef0123456789abcde2", "needs_sign_in", "person"},
		{"budget", "act-budget", "0123456789abcdef0123456789abcde3", "budget", "budget"},
		{"limit", "act-limit", "0123456789abcdef0123456789abcde4", "", "run_limit"},
	}
	tasks := []goal.Task{
		{ID: "writing", Name: "Write the note", Kind: goal.TaskStep, Stage: goal.TaskBuilding, Attempt: 1, Done: "The note is written"},
		{ID: "blocked", Kind: goal.TaskStep, Stage: goal.TaskReady, Attempt: 1, Done: "Wait for the note"},
		{ID: "area", Kind: goal.TaskStep, Stage: goal.TaskReady, Attempt: 1, Done: "Stay in the area"},
		{ID: "workers", Kind: goal.TaskStep, Stage: goal.TaskReady, Attempt: 1, Done: "Wait for a worker"},
		{ID: "capacity", Kind: goal.TaskStep, Stage: goal.TaskReady, Attempt: 1, Done: "Wait for room"},
		{ID: "review", Kind: goal.TaskStep, Stage: goal.TaskDelivered, Attempt: 1, Done: "Wait for review"},
		{ID: "slots", Kind: goal.TaskStep, Stage: goal.TaskReady, Attempt: 1, Done: "Wait for a slot"},
		{ID: "decision", Kind: goal.TaskStep, Stage: goal.TaskConflicted, Attempt: 1, Done: "Ask the person"},
		{ID: "active", Name: "Read the notes", Kind: goal.BackgroundStep, Stage: goal.TaskBuilding, Attempt: 1, Done: "The notes are read"},
	}
	var actions []goal.Action
	var records []runs.Record
	for _, wait := range waits {
		tasks = append(tasks, goal.Task{
			ID: wait.task, Kind: goal.BackgroundStep, Stage: goal.TaskBuilding, Attempt: 1,
			Done: "Read the " + wait.task + " note",
		})
		actions = append(actions, goal.Action{ID: wait.action, GoalID: goalID, TaskID: wait.task, Kind: goal.Assign, Attempt: 1, State: "queued"})
		records = append(records, runs.Record{
			ID: wait.run, Origin: origin, State: runs.Waiting, Reason: wait.reason, Noticed: true,
			StartKey: runs.CallKey(origin, wait.action),
			Brief:    runs.Brief{Goal: "Read the " + wait.task + " note"},
		})
	}
	actions = append(actions, goal.Action{ID: "act-active", GoalID: goalID, TaskID: "active", Kind: goal.Assign, Attempt: 1, State: "queued"})
	records = append(records, runs.Record{
		ID: "0123456789abcdef0123456789abcde5", Origin: origin, State: runs.Working, Noticed: true,
		StartKey: runs.CallKey(origin, "act-active"),
		Brief:    runs.Brief{Goal: "Read the notes"},
	})
	g := goal.Goal{
		ID: goalID, Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: origin.Turn,
		State: goal.GoalRunning, Tasks: tasks,
		Waits: []goal.Wait{
			{GoalID: goalID, TaskID: "writing", Reason: "worker"},
			{GoalID: goalID, TaskID: "blocked", Reason: "dependency"},
			{GoalID: goalID, TaskID: "area", Reason: "area"},
			{GoalID: goalID, TaskID: "workers", Reason: "worker_quota"},
			{GoalID: goalID, TaskID: "capacity", Reason: "host_capacity"},
			{GoalID: goalID, TaskID: "review", Reason: "ci_review"},
			{GoalID: goalID, TaskID: "slots", Reason: "run_limit"},
			{GoalID: goalID, TaskID: "decision", Reason: "person"},
			{GoalID: goalID, TaskID: "quota", Reason: "worker"},
		},
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
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
		for _, record := range records {
			raw, err = json.Marshal(record)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(t.Context(), "INSERT INTO background_runs(id,record) VALUES(?,?)", record.ID, raw); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := r.background.list.Build(t.Context(), r.person)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]runs.RunningEntry{}
	for _, entry := range snapshot.Entries {
		if entry.Ref.Source == "goal" {
			got[entry.Ref.ID] = entry
		}
		if entry.Ref.Source == "run" {
			t.Fatalf("native run still published: %+v", entry.Ref)
		}
	}
	want := map[string]struct{ summary, state, reason string }{
		goalTaskKey(goalID, "quota"):    {"Read the quota note", "waiting", "quota"},
		goalTaskKey(goalID, "signin"):   {"Read the signin note", "waiting", "person"},
		goalTaskKey(goalID, "budget"):   {"Read the budget note", "waiting", "budget"},
		goalTaskKey(goalID, "limit"):    {"Read the limit note", "waiting", "run_limit"},
		goalTaskKey(goalID, "blocked"):  {"Wait for the note", "waiting", "dependency"},
		goalTaskKey(goalID, "area"):     {"Stay in the area", "waiting", "area"},
		goalTaskKey(goalID, "workers"):  {"Wait for a worker", "waiting", "quota"},
		goalTaskKey(goalID, "capacity"): {"Wait for room", "waiting", "host_capacity"},
		goalTaskKey(goalID, "review"):   {"Wait for review", "waiting", "ci_pending"},
		goalTaskKey(goalID, "slots"):    {"Wait for a slot", "waiting", "run_limit"},
		goalTaskKey(goalID, "decision"): {"Ask the person", "waiting", "person"},
		goalTaskKey(goalID, "writing"):  {"Write the note", "in_progress", ""},
		goalTaskKey(goalID, "active"):   {"Read the notes", "in_progress", ""},
	}
	for id, row := range want {
		entry, ok := got[id]
		if !ok {
			t.Errorf("missing %s", id)
			continue
		}
		if entry.View.Summary != row.summary || entry.View.State != row.state || entry.View.Reason != row.reason {
			t.Errorf("%s: got %q %s %s", id, entry.View.Summary, entry.View.State, entry.View.Reason)
		}
	}
}
