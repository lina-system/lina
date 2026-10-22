// SPDX-License-Identifier: Apache-2.0
package waiting_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting"
)

func round() waiting.Round {
	return waiting.Round{ID: "round", Goal: "goal", Person: "person", Agent: "agent", Conversation: "conversation", Deadline: now.Add(time.Hour), Questions: []waiting.Question{
		{ID: "destination", Prompt: "Which destination?", Recommendation: "A", Reason: waiting.SignificantChoice, Choices: []string{"A", "B"}, Default: "A", Dependents: []string{"draft"}},
		{ID: "send", Prompt: "Send the itinerary?", Recommendation: waiting.DoNotAct, Reason: waiting.OutsideEffect, Choices: []string{waiting.DoNotAct, "send"}, Default: waiting.DoNotAct, Dependents: []string{"send-task"}},
	}}
}
func rounds(t *testing.T) (*waiting.Rounds, *canon.Store, canon.Config) {
	t.Helper()
	db, cfg := database(t)
	return waiting.NewRounds(db, waiting.RoundHooks{}), db, cfg
}
func TestWorkRoundRequiresDeadlineAndSafeDefault(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*waiting.Round)
	}{
		{"deadline", func(r *waiting.Round) { r.Deadline = time.Time{} }},
		{"default", func(r *waiting.Round) { r.Questions[0].Default = "" }},
		{"outside", func(r *waiting.Round) { r.Questions[1].Default = "send" }},
		{"irreversible", func(r *waiting.Round) { r.Questions[0].Irreversible = true }},
		{"invalid default", func(r *waiting.Round) { r.Questions[0].Default = "invented" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, _ := rounds(t)
			r := round()
			test.change(&r)
			if err := s.Open(t.Context(), r); !errors.Is(err, waiting.ErrInvalid) {
				t.Fatalf("unsafe round admitted: %v", err)
			}
		})
	}
}
func TestWorkRoundExpiresOnceAndLateAnswerIsNewDecision(t *testing.T) {
	s, db, cfg := rounds(t)
	r := round()
	if err := s.Open(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if err := s.Open(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	other := r
	other.ID = "second"
	if err := s.Open(t.Context(), other); !errors.Is(err, waiting.ErrRoundOpen) {
		t.Fatalf("independent interruption: %v", err)
	}
	if err := s.Expire(t.Context(), r.Deadline.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(t.Context(), r.ID)
	if len(before.Decisions) != 0 {
		t.Fatal("early expiry")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Expire(t.Context(), r.Deadline); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s = waiting.NewRounds(open(t, cfg), waiting.RoundHooks{})
	if err := s.Expire(t.Context(), r.Deadline.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	expired, err := s.Get(t.Context(), r.ID)
	if err != nil || len(expired.Decisions) != 1 || expired.Decisions[0].Kind != "default" {
		t.Fatalf("expiry: %+v %v", expired, err)
	}
	answer := waiting.RoundAnswer{MessageID: "message", Message: "Use B; do not send", Values: map[string]string{"destination": "B", "send": waiting.DoNotAct}}
	if err := s.Answer(t.Context(), "intruder", r.ID, answer, r.Deadline); !errors.Is(err, waiting.ErrPerson) {
		t.Fatalf("owner: %v", err)
	}
	for range 2 {
		if err := s.Answer(t.Context(), r.Person, r.ID, answer, r.Deadline); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(t.Context(), r.ID)
	if err != nil || len(got.Decisions) != 2 || got.Decisions[1].Kind != "late_answer" || got.Decisions[1].Message != answer.Message || !reflect.DeepEqual(got.Decisions[0], expired.Decisions[0]) {
		t.Fatalf("late decision: %+v %v", got, err)
	}
}
func TestWorkRoundAnswerAtDeadlineExpiresBeforeRecordingAnswer(t *testing.T) {
	s, _, _ := rounds(t)
	r := round()
	if err := s.Open(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	a := waiting.RoundAnswer{MessageID: "m", Message: "Choose B", Values: map[string]string{"destination": "B", "send": waiting.DoNotAct}}
	if err := s.Answer(t.Context(), r.Person, r.ID, a, r.Deadline); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), r.ID)
	if len(got.Decisions) != 2 || got.Decisions[0].Kind != "default" || got.Decisions[1].Kind != "late_answer" {
		t.Fatalf("boundary: %+v", got)
	}
}
func TestWorkRoundHookFailureRollsBackExpiry(t *testing.T) {
	_, db, _ := rounds(t)
	fail := true
	s := waiting.NewRounds(db, waiting.RoundHooks{Decided: func(context.Context, *canon.Tx, waiting.Round, waiting.RoundDecision) error {
		if fail {
			return errors.New("unavailable")
		}
		return nil
	}})
	r := round()
	if err := s.Open(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if err := s.Expire(t.Context(), r.Deadline); err == nil {
		t.Fatal("missing failure")
	}
	got, _ := s.Get(t.Context(), r.ID)
	if len(got.Decisions) != 0 {
		t.Fatal("partial commit")
	}
	fail = false
	if err := s.Expire(t.Context(), r.Deadline); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(t.Context(), r.ID)
	if len(got.Decisions) != 1 {
		t.Fatal("retry failed")
	}
}

// Synchronize with the service's publication, not a sleep or polling loop.
func TestWorkRoundRunningTimerWakesOnAdmission(t *testing.T) {
	s, _, _ := rounds(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	published := make(chan struct{}, 8)
	go func() {
		defer close(done)
		s.Run(ctx, func(context.Context) error {
			select {
			case published <- struct{}{}:
			default:
			}
			return nil
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("timer did not start")
	}
	r := round()
	r.Deadline = now
	if err := s.Open(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case <-published:
			got, err := s.Get(t.Context(), r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Decisions) == 1 {
				return
			}
		case <-timeout.C:
			t.Fatal("running timer did not expire admitted row")
		}
	}
}
func TestWorkRoundTimelyAnswerPreventsDefault(t *testing.T) {
	s, _, _ := rounds(t)
	r := round()
	if err := s.Open(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	a := waiting.RoundAnswer{MessageID: "timely", Message: "Choose B", Values: map[string]string{"destination": "B", "send": waiting.DoNotAct}}
	if err := s.Answer(t.Context(), r.Person, r.ID, a, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Expire(t.Context(), r.Deadline); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), r.ID)
	if len(got.Decisions) != 1 || got.Decisions[0].Kind != "answer" {
		t.Fatalf("timely answer defaulted: %+v", got)
	}
}
