// SPDX-License-Identifier: Apache-2.0
package work

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/waiting"
)

func TestWaitingRoundGatesOnlyDependentsAndRevisesGoal(t *testing.T) {
	s, _ := testStore(t)
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
	if err := s.Waiting().Open(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	start := func(id string) error {
		run := runFixture()
		run.ID = id
		run.TaskID = id
		return s.Start(t.Context(), run)
	}
	if err := start("draft"); !errors.Is(err, ErrState) {
		t.Fatalf("pending dependent started: %v", err)
	}
	if err := start("independent"); err != nil {
		t.Fatal(err)
	}
	if err := s.Waiting().Expire(t.Context(), deadline); err != nil {
		t.Fatal(err)
	}
	if err := start("draft"); err != nil {
		t.Fatal(err)
	}
	if err := start("send"); !errors.Is(err, ErrState) {
		t.Fatalf("outward default started: %v", err)
	}
	before, _ := s.Get(t.Context(), "draft")
	a := waiting.RoundAnswer{MessageID: "late", Message: "Use B", Values: map[string]string{"route": "B", "send": "send"}}
	if err := s.Waiting().Answer(t.Context(), "person", "r", a, deadline); err != nil {
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
	if err := start("send"); !errors.Is(err, ErrState) {
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
