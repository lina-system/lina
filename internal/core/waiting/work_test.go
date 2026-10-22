// SPDX-License-Identifier: Apache-2.0
package waiting_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
)

func TestWaitingRoundGatesOnlyDependentsAndRevisesGoal(t *testing.T) {
	db, _ := database(t)
	s := work.New(db)
	rounds := waiting.NewWorkRounds(db, s)
	goal := taskFixture()
	goal.ID = "goal"
	goal.PersonID = "person"
	if err := s.Create(t.Context(), goal); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"draft", "send", "independent"} {
		task := goal
		task.ID = id
		task.GoalID = goal.ID
		if err := s.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := waiting.Round{ID: "r", Goal: goal.ID, Person: goal.PersonID, Agent: goal.AgentID, Conversation: goal.ConversationID, Deadline: deadline, Questions: []waiting.Question{
		{ID: "route", Prompt: "Route?", Recommendation: "A", Reason: waiting.SignificantChoice, Choices: []string{"A", "B"}, Default: "A", Dependents: []string{"draft"}},
		{ID: "send", Prompt: "Send?", Recommendation: waiting.DoNotAct, Reason: waiting.OutsideEffect, Choices: []string{waiting.DoNotAct, "send"}, Default: waiting.DoNotAct, Dependents: []string{"send"}},
	}}
	if err := rounds.Open(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	start := func(id string) error {
		run := runFixture()
		run.ID = id
		run.TaskID = id
		return s.Start(t.Context(), run)
	}
	if err := start("draft"); !errors.Is(err, work.ErrState) {
		t.Fatalf("pending dependent started: %v", err)
	}
	if err := start("independent"); err != nil {
		t.Fatal(err)
	}
	if err := rounds.Expire(t.Context(), deadline); err != nil {
		t.Fatal(err)
	}
	if err := start("draft"); err != nil {
		t.Fatal(err)
	}
	if err := start("send"); !errors.Is(err, work.ErrState) {
		t.Fatalf("outward default started: %v", err)
	}
	before, _ := s.Get(t.Context(), "draft")
	a := waiting.RoundAnswer{MessageID: "late", Message: "Use B", Values: map[string]string{"route": "B", "send": "send"}}
	if err := rounds.Answer(t.Context(), "person", "r", a, deadline); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), "goal")
	if got.Revision != 2 || len(got.Decisions) != 2 || got.Decisions[1].Decision.Message != "Use B" || got.State != "working" {
		t.Fatalf("goal not revised: %+v", got)
	}
	after, _ := s.Get(t.Context(), "draft")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("late answer changed already applied inputs")
	}
	if err := start("send"); !errors.Is(err, work.ErrState) {
		t.Fatal("late answer granted old effect")
	}
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	rebuilt, _ := s.Get(t.Context(), "goal")
	if !reflect.DeepEqual(got, rebuilt) {
		t.Fatal("revision not in ledger")
	}
}

func TestStoppedGoalRoundCannotBlockOtherExpiryOrRestart(t *testing.T) {
	db, _ := database(t)
	s := work.New(db)
	rounds := waiting.NewWorkRounds(db, s)
	deadline := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"stopped", "unrelated"} {
		goal := taskFixture()
		goal.ID = id
		goal.PersonID = "person"
		if err := s.Create(t.Context(), goal); err != nil {
			t.Fatal(err)
		}
		child := goal
		child.ID = id + ":child"
		child.GoalID = id
		if err := s.Create(t.Context(), child); err != nil {
			t.Fatal(err)
		}
		r := waiting.Round{ID: id, Goal: id, Person: goal.PersonID, Agent: goal.AgentID, Conversation: goal.ConversationID, Deadline: deadline, Questions: []waiting.Question{{ID: "q", Prompt: "Route?", Recommendation: "A", Reason: waiting.SignificantChoice, Choices: []string{"A", "B"}, Default: "A", Dependents: []string{child.ID}}}}
		if err := rounds.Open(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	run := runFixture()
	run.TaskID = "stopped"
	if err := s.Start(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(t.Context(), "stopped", run.ID, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := rounds.Expire(t.Context(), deadline); err != nil {
		t.Fatalf("stopped goal blocked expiry: %v", err)
	}
	for _, id := range []string{"stopped", "unrelated"} {
		r, err := rounds.Get(t.Context(), id)
		if err != nil || len(r.Decisions) != 1 {
			t.Fatalf("unexpired %s: %+v %v", id, r, err)
		}
	}
	if err := rounds.Answer(t.Context(), "person", "stopped", waiting.RoundAnswer{MessageID: "late", Message: "Use B", Values: map[string]string{"q": "B"}}, deadline); err != nil {
		t.Fatal(err)
	}
	goal, _ := s.Get(t.Context(), "stopped")
	child, _ := s.Get(t.Context(), "stopped:child")
	unrelated, _ := s.Get(t.Context(), "unrelated:child")
	if goal.State != "stopped" || goal.Revision != 1 || child.State != "input_needed" || unrelated.State != "working" {
		t.Fatalf("stopped goal resurrected or unrelated blocked: %+v %+v %+v", goal, child, unrelated)
	}
	if err := s.Recover(t.Context(), "2"); err != nil {
		t.Fatal(err)
	}
	if err := rounds.Expire(t.Context(), deadline); err != nil {
		t.Fatal(err)
	}
}

func taskFixture() work.Task {
	return work.Task{ID: "task-1", AgentID: "agent-1", ConversationID: "conversation-1", Title: "Produce report", Done: "report exists"}
}
func runFixture() work.Run {
	return work.Run{ID: "run-1", TaskID: "task-1", Generation: 1, Worker: "fixture", ThreadID: "thread-1", TurnID: "turn-1", NodeID: "node-1"}
}
