// SPDX-License-Identifier: Apache-2.0
package waiting_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting"
)

func conversationQuestion(id, turn string) waiting.ConversationQuestion {
	return waiting.ConversationQuestion{ID: id, Person: "person", Agent: "agent", Conversation: "conversation", Turn: turn, Input: "input-" + turn, AskedAt: now,
		QuestionDraft: waiting.QuestionDraft{Question: "Which format?", Recommendation: "Use a table", Choices: []string{"table", "list"}, Default: "table"}}
}

func TestConversationQuestionLimitIsDurableAndAtomic(t *testing.T) {
	db, cfg := database(t)
	s := waiting.NewConversationQuestions(db)
	q := conversationQuestion("first", "turn")
	for range 2 {
		if err := s.Ask(t.Context(), q, 2); err != nil {
			t.Fatal(err)
		}
	}
	changed := q
	changed.Default = "list"
	if err := s.Ask(t.Context(), changed, 2); !errors.Is(err, waiting.ErrStale) {
		t.Fatalf("changed retry: %v", err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.Ask(t.Context(), conversationQuestion(fmt.Sprint("parallel-", i), "turn"), 2)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, waiting.ErrQuestionLimit) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted=%d, want one remaining slot", accepted.Load())
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s = waiting.NewConversationQuestions(open(t, cfg))
	if err := s.Ask(t.Context(), conversationQuestion("after-restart", "turn"), 2); !errors.Is(err, waiting.ErrQuestionLimit) {
		t.Fatalf("restart reset limit: %v", err)
	}
	if err := s.Ask(t.Context(), conversationQuestion("next-turn", "next"), 2); err != nil {
		t.Fatal(err)
	}
}

func TestConversationQuestionDefaultAndAnswerRemainDistinct(t *testing.T) {
	db, cfg := database(t)
	s := waiting.NewConversationQuestions(db)
	q := conversationQuestion("q", "turn")
	if err := s.Ask(t.Context(), q, 1); err != nil {
		t.Fatal(err)
	}
	d := waiting.QuestionResolution{Kind: "default_applied", Input: "next-person-event", At: now.Add(1), Value: "invented"}
	if err := s.Resolve(t.Context(), "intruder", q.Agent, q.ID, d); !errors.Is(err, waiting.ErrPerson) {
		t.Fatalf("owner check: %v", err)
	}
	if err := s.Resolve(t.Context(), q.Person, "other-agent", q.ID, d); !errors.Is(err, waiting.ErrPerson) {
		t.Fatalf("agent check: %v", err)
	}
	if err := s.Resolve(t.Context(), q.Person, q.Agent, q.ID, d); !errors.Is(err, waiting.ErrInvalid) {
		t.Fatalf("invented default: %v", err)
	}
	d.Value = q.Default
	for range 2 {
		if err := s.Resolve(t.Context(), q.Person, q.Agent, q.ID, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s = waiting.NewConversationQuestions(open(t, cfg))
	got, err := s.Get(t.Context(), q.ID)
	if err != nil || got.Resolution == nil || *got.Resolution != d {
		t.Fatalf("default lost: %+v %v", got, err)
	}
	d.Kind, d.Value = "answered", "list"
	if err := s.Resolve(t.Context(), q.Person, q.Agent, q.ID, d); !errors.Is(err, waiting.ErrAnswered) {
		t.Fatalf("late answer rewrote default: %v", err)
	}
	pending, err := s.Pending(t.Context(), q.Person, q.Agent)
	if err != nil || len(pending) != 0 {
		t.Fatalf("closed question still pending: %+v %v", pending, err)
	}
	q = conversationQuestion("answered", "second")
	if err := s.Ask(t.Context(), q, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(t.Context(), q.Person, q.Agent, q.ID, d); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(t.Context(), q.ID)
	if err != nil || got.Resolution.Kind != "answered" || got.Resolution.Value != "list" {
		t.Fatalf("answer: %+v %v", got, err)
	}
}

func TestConversationQuestionNoticeRollback(t *testing.T) {
	db, _ := database(t)
	s := waiting.NewConversationQuestions(db)
	q := conversationQuestion("q", "turn")
	if err := s.Ask(t.Context(), q, 1); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("notice unavailable")
	d := waiting.QuestionResolution{Kind: "default_applied", Input: "next", Value: q.Default, At: now.Add(1)}
	if err := s.ResolveWith(t.Context(), q.Person, q.Agent, q.ID, d, func(context.Context, *canon.Tx, waiting.ConversationQuestion, waiting.QuestionResolution) error {
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("notice error: %v", err)
	}
	got, err := s.Get(t.Context(), q.ID)
	if err != nil || got.Resolution != nil {
		t.Fatalf("partial default survived rollback: %+v %v", got, err)
	}
	other := conversationQuestion("other-agent", "turn")
	other.Agent = "other"
	if err := s.Ask(t.Context(), other, 1); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending(t.Context(), q.Person, q.Agent)
	if err != nil || len(pending) != 1 || pending[0].ID != q.ID {
		t.Fatalf("cross-agent pending: %+v %v", pending, err)
	}
}
