// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func gathered() budget.Handoff {
	return budget.Handoff{Turn: origin().Turn,
		Items:   []provider.Item{json.RawMessage(`{"role":"user","content":"finish the whole request"}`), json.RawMessage(`{"type":"function_call_output","call_id":"read-1","output":"gathered evidence"}`)},
		Results: []loop.Result{{ID: "read-1", Code: loop.OK, Output: json.RawMessage(`"gathered evidence"`)}},
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
			if err := s.Release(t.Context(), handle.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.Wait(t.Context(), handle.ID); err != nil {
				t.Fatal(err)
			}
			r, err = s.Get(t.Context(), handle.ID)
			if err != nil || requests.Load() != 1 || r.Outcome != runs.Partial || r.EffectsQuestion == nil || r.Promotion.Effects[0].Code != code {
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
