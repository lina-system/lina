// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func gathered() budget.Handoff {
	return budget.Handoff{Turn: origin().Turn, Input: "finish the whole request",
		Items:   []provider.Item{json.RawMessage(`{"role":"user","content":"finish the whole request"}`), json.RawMessage(`{"type":"function_call_output","call_id":"read-1","output":"gathered evidence"}`)},
		Results: []loop.Result{{ID: "read-1", Code: loop.OK, Output: json.RawMessage(`"gathered evidence"`)}},
	}
}

func TestHardTurnStartsRealBackgroundRunFromDurableInput(t *testing.T) {
	var requests atomic.Int64
	service, db, _ := setupWith(t, func(_ context.Context, req provider.Request) (provider.Stream, error) {
		if requests.Add(1) == 1 {
			var message struct{ Content string }
			if len(req.Items) != 2 || json.Unmarshal(req.Items[1], &message) != nil {
				t.Errorf("missing promotion: %+v", req)
				return nil, errors.New("missing promotion")
			}
			var h budget.Handoff
			if json.Unmarshal([]byte(message.Content), &h) != nil || h.Input != "whole request" || len(h.Results) != 1 || h.Results[0].Code != turn.BudgetReached {
				t.Errorf("lost original input/results: %+v", h)
			}
			return call("background-read"), nil
		}
		return answer(runs.Artifact{Body: "whole result", Done: true}), nil
	}, func(c *runs.Config) { c.PublishPromotion = turn.PublishPromotion })
	o := origin()
	id, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: o.Conversation, Source: "client", NativeID: "input"}, Body: []byte("whole request")})
	if err != nil {
		t.Fatal(err)
	}
	gauge := (budget.Meter{}).Start(o.Turn, time.Now())
	for range 9 {
		gauge.Round()
	}
	turnRequests := 0
	store := turn.New(db)
	_, err = store.Run(t.Context(), turn.Options{ID: o.Turn, Conversation: o.Conversation, InboxID: id.ID, Budget: gauge,
		StartFromTurn: func(ctx context.Context, h budget.Handoff) (budget.Handle, error) {
			return service.StartFromTurn(ctx, o, brief(), h)
		},
		Loop: loop.Config{Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"read"}`)}}, Tools: map[string]loop.Tool{"read": {Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(context.Context, provider.ToolCall) loop.Result {
			t.Error("turn read ran at soft budget")
			return loop.Result{Code: loop.OK}
		}}}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			turnRequests++
			if turnRequests > 1 {
				t.Fatal("hard turn made another model request")
			}
			return call("turn-read"), nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := store.Outcome(t.Context(), o.Turn)
	if err != nil || outcome.Handle.ID == "" || requests.Load() != 0 {
		t.Fatalf("handle not committed before execution: %+v requests=%d err=%v", outcome, requests.Load(), err)
	}
	// The composition root consumes the durable handle, then releases its run.
	release(t, service, outcome.Handle.ID)
	r := wait(t, service, outcome.Handle.ID)
	if r.Outcome != runs.Complete || requests.Load() != 2 || len(r.Calls) != 1 || r.Calls[0] != "read" {
		t.Fatalf("background did not finish the request: %+v", r)
	}
}

func TestSoftTurnCanUseRealStartRunTool(t *testing.T) {
	service, db, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("start tool ran background work before receipt")
		return answer(runs.Artifact{}), nil
	}, func(c *runs.Config) { c.PublishPromotion = turn.PublishPromotion })
	o := origin()
	id, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: o.Conversation, Source: "client", NativeID: "input"}, Body: []byte("whole request")})
	if err != nil {
		t.Fatal(err)
	}
	gauge := (budget.Meter{}).Start(o.Turn, time.Now())
	for range 6 {
		gauge.Round()
	}
	requests := 0
	args, _ := json.Marshal(brief())
	raw, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": "start", "name": "start_run", "arguments": string(args)})
	out, err := turn.New(db).Run(t.Context(), turn.Options{ID: o.Turn, Conversation: o.Conversation, InboxID: id.ID, Budget: gauge,
		Loop: loop.Config{Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"start_run"}`)}}, Tools: map[string]loop.Tool{"start_run": service.StartTool(o)}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			requests++
			if requests == 1 {
				return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: raw}, {Kind: provider.Terminal}}}, nil
			}
			return &stream{[]provider.Event{{Kind: provider.Terminal}}}, nil
		}},
	})
	if err != nil || len(out.Results) != 1 || out.Results[0].Code != loop.OK {
		t.Fatalf("start_run blocked: %+v %v", out, err)
	}
	var receipt runs.Receipt
	if err := json.Unmarshal(out.Results[0].Output, &receipt); err != nil || receipt.ID == "" {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestStartFromTurnPreservesGatheredStateAndReturnsBeforeModel(t *testing.T) {
	var calls atomic.Int64
	h := gathered()
	s, _, _ := setup(t, func(_ context.Context, req provider.Request) (provider.Stream, error) {
		calls.Add(1)
		if req.Purpose != provider.Background || len(req.Items) != 2 || len(req.Tools) != 1 {
			t.Errorf("background request: %+v", req)
		}
		var message struct{ Content string }
		if err := json.Unmarshal(req.Items[1], &message); err != nil {
			t.Error(err)
		}
		var restored budget.Handoff
		if err := json.Unmarshal([]byte(message.Content), &restored); err != nil || len(restored.Items) != 2 || string(restored.Items[0]) != string(h.Items[0]) || string(restored.Results[0].Output) != string(h.Results[0].Output) {
			t.Errorf("gathered state missing: %+v %v", restored, err)
		}
		return answer(runs.Artifact{Body: "completed result", Done: true}), nil
	})
	handle, err := s.StartFromTurn(t.Context(), origin(), brief(), h)
	if err != nil || handle.ID == "" || handle.State != string(runs.Working) || calls.Load() != 0 {
		t.Fatalf("handle=%+v calls=%d err=%v", handle, calls.Load(), err)
	}
	again, err := s.StartFromTurn(t.Context(), origin(), brief(), h)
	if err != nil || again.ID != handle.ID {
		t.Fatalf("promotion was not idempotent: %+v %v", again, err)
	}
	if err := s.Release(t.Context(), handle.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(t.Context(), handle.ID); err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(t.Context(), handle.ID)
	if err != nil || r.Outcome != runs.Complete || r.Promotion == nil || len(r.Promotion.Results) != 1 || r.EffectsQuestion != nil {
		t.Fatalf("record=%+v err=%v", r, err)
	}
}

func TestPromotedEffectsKeepQuestionAndUnknownReceiptWhileReadsContinue(t *testing.T) {
	for _, code := range []loop.Code{loop.OK, loop.Unknown} {
		t.Run(string(code), func(t *testing.T) {
			h := gathered()
			h.Effects = []budget.Effect{{CallID: "sent", Key: "effect-key", Tool: "send", Code: code}}
			var requests atomic.Int64
			s, _, _ := setup(t, func(_ context.Context, req provider.Request) (provider.Stream, error) {
				requests.Add(1)
				if len(req.Tools) != 1 || string(req.Tools[0]) != `{"type":"function","name":"read"}` {
					t.Errorf("promotion widened tool scope: %s", req.Tools)
				}
				return answer(runs.Artifact{Body: "remaining analysis", Done: true}), nil
			})
			handle, err := s.StartFromTurn(t.Context(), origin(), brief(), h)
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.Get(t.Context(), handle.ID)
			if err != nil || r.EffectsQuestion == nil || r.EffectsQuestion.State != "awaiting_person" || r.EffectsQuestion.Effects[0].Code != code {
				t.Fatalf("question missing: %+v %v", r, err)
			}
			questions, err := s.EffectQuestions(t.Context(), origin().Conversation)
			if err != nil || len(questions) != 1 || questions[0].RunID != handle.ID {
				t.Fatalf("question not deliverable: %+v %v", questions, err)
			}
			if err := s.AcknowledgeEffectQuestion(t.Context(), handle.ID); err != nil {
				t.Fatal(err)
			}
			questions, err = s.EffectQuestions(t.Context(), origin().Conversation)
			if err != nil || len(questions) != 0 {
				t.Fatalf("question delivery acknowledgement lost: %+v %v", questions, err)
			}
			if err := s.Release(t.Context(), handle.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.Wait(t.Context(), handle.ID); err != nil {
				t.Fatal(err)
			}
			r, err = s.Get(t.Context(), handle.ID)
			if err != nil || requests.Load() != 1 || r.Outcome != runs.Partial || r.EffectsQuestion == nil || r.EffectsQuestion.State != "awaiting_person" || r.Promotion.Effects[0].Code != code {
				t.Fatalf("question/evidence lost or whole request marked complete: %+v %v", r, err)
			}
		})
	}
}

func TestStartFromTurnRejectsMismatchedIdentityAndChangedReplay(t *testing.T) {
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Fatal("unreleased promotion called a model")
		return nil, nil
	})
	h := gathered()
	h.Turn = "different-turn"
	if _, err := s.StartFromTurn(t.Context(), origin(), brief(), h); !errors.Is(err, runs.ErrInvalid) {
		t.Fatal(err)
	}
	h = gathered()
	if _, err := s.StartFromTurn(t.Context(), origin(), brief(), h); err != nil {
		t.Fatal(err)
	}
	h.Results = nil
	if _, err := s.StartFromTurn(t.Context(), origin(), brief(), h); !errors.Is(err, runs.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestCommittedPromotionSurvivesLaterBudgetHold(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "restart"}[restart], func(t *testing.T) {
			var held atomic.Bool
			var authorizations, publications atomic.Int64
			var cfg runs.Config
			s, db, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
				t.Error("receipt replay started execution")
				return nil, nil
			}, func(c *runs.Config) {
				c.Authorize = func(context.Context, runs.Origin, runs.Brief) error {
					authorizations.Add(1)
					if held.Load() {
						return runs.ErrBudget
					}
					return nil
				}
				c.PublishPromotion = func(context.Context, *canon.Tx, string, budget.Handle, budget.Handoff) error {
					publications.Add(1)
					return nil
				}
				cfg = *c
			})
			h := gathered()
			first, err := s.StartFromTurn(t.Context(), origin(), brief(), h)
			if err != nil {
				t.Fatal(err)
			}
			held.Store(true)
			if restart {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = runs.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { s.Close() })
			}
			again, err := s.StartFromTurn(t.Context(), origin(), brief(), h)
			if err != nil || again != first {
				t.Errorf("committed promotion blocked by later budget: first=%+v replay=%+v err=%v", first, again, err)
			}
			changed := h
			changed.Results = nil
			if _, err := s.StartFromTurn(t.Context(), origin(), brief(), changed); !errors.Is(err, runs.ErrInvalid) {
				t.Errorf("changed gathered state: %v", err)
			}
			changedBrief := brief()
			changedBrief.Done = "different completion condition"
			if _, err := s.StartFromTurn(t.Context(), origin(), changedBrief, h); !errors.Is(err, runs.ErrInvalid) {
				t.Errorf("changed brief: %v", err)
			}
			if got := authorizations.Load(); got != 1 {
				t.Errorf("receipt replay authorized new work: calls=%d", got)
			}
			other := origin()
			other.Turn = "new-turn"
			h.Turn = other.Turn
			if _, err := s.StartFromTurn(t.Context(), other, brief(), h); !errors.Is(err, runs.ErrBudget) {
				t.Errorf("new promotion bypassed hold: %v", err)
			}
			var count int
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM background_runs").Scan(&count)
			}); err != nil || count != 1 || publications.Load() != 1 || authorizations.Load() != 2 {
				t.Fatalf("runs=%d publications=%d authorizations=%d err=%v", count, publications.Load(), authorizations.Load(), err)
			}
		})
	}
}

func TestPromotionReplaySurvivesPersistenceMasking(t *testing.T) {
	var cfg runs.Config
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("unreleased promotion called a model")
		return answer(runs.Artifact{}), nil
	}, func(c *runs.Config) { cfg = *c })
	h := gathered()
	h.Input = "api_key=synthetic-credential-value"
	first, err := s.StartFromTurn(t.Context(), origin(), brief(), h)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Get(t.Context(), first.ID)
	if err != nil || strings.Contains(r.Promotion.Input, "synthetic-credential-value") {
		t.Fatalf("promotion input was not masked: %+v %v", r.Promotion, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runs.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	again, err := reopened.StartFromTurn(t.Context(), origin(), brief(), h)
	if err != nil || again.ID != first.ID {
		t.Fatalf("masked promotion replay rejected or duplicated: first=%+v again=%+v err=%v", first, again, err)
	}
	h.Input = "api_key=different-synthetic-credential"
	if _, err := reopened.StartFromTurn(t.Context(), origin(), brief(), h); !errors.Is(err, runs.ErrInvalid) {
		t.Fatalf("different inputs with the same mask were accepted: %v", err)
	}
}

func TestPromotionPublicationFailureRollsBackRunAdmission(t *testing.T) {
	failure := errors.New("publication failed")
	service, db, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("failed promotion reached a model")
		return answer(runs.Artifact{}), nil
	}, func(c *runs.Config) {
		c.PublishPromotion = func(context.Context, *canon.Tx, string, budget.Handle, budget.Handoff) error { return failure }
	})
	if _, err := service.StartFromTurn(t.Context(), origin(), brief(), gathered()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var count int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM background_runs").Scan(&count)
	}); err != nil || count != 0 {
		t.Fatalf("failed publication left an orphan run: count=%d err=%v", count, err)
	}
}
