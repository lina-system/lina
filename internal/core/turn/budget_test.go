// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func budgetConfig(t *testing.T, calls func(int) []provider.ToolCall) (loop.Config, *int) {
	t.Helper()
	executed := 0
	requests := 0
	cfg := loop.Config{Request: provider.Request{Items: []provider.Item{json.RawMessage(`{"role":"user","content":"finish the whole request"}`)}}, Tools: map[string]loop.Tool{}}
	for _, name := range []string{"read", "start_run", "dispatch_work"} {
		decl, _ := json.Marshal(map[string]string{"type": "function", "name": name})
		cfg.Request.Tools = append(cfg.Request.Tools, decl)
		cfg.Tools[name] = loop.Tool{Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(context.Context, provider.ToolCall) loop.Result {
			executed++
			return loop.Result{Code: loop.OK, Output: json.RawMessage(`{"evidence":"retained"}`)}
		}}
	}
	cfg.Parallel = 1
	cfg.Stream = func(_ context.Context, req provider.Request) (provider.Stream, error) {
		requests++
		return reviewResponse(calls(requests)...), nil
	}
	return cfg, &executed
}

func TestToolWallBudgetFinishesEffectAndPromotesItsReceipt(t *testing.T) {
	for _, state := range []tools.State{tools.Applied, tools.Unknown} {
		t.Run(string(state), func(t *testing.T) {
			now := time.Unix(100, 0)
			gauge := (budget.Meter{Now: func() time.Time { return now }}).Start("turn", now)
			executed := 0
			spec := reviewSpec("edit", func(raw json.RawMessage) (tools.Input, error) { return reviewInput(raw), nil })
			db, _, tool := reviewDispatch(t, spec, func(ctx context.Context, _ lease.Lease, _ tools.Input) (tools.Result, error) {
				executed++
				now = now.Add(time.Minute)
				if ctx.Err() != nil {
					t.Error("budget interrupted a running effect")
				}
				return tools.Result{State: state, Revision: "r2"}, nil
			})
			requests := 0
			cfg := loop.Config{Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"edit"}`)}}, Tools: map[string]loop.Tool{"edit": tool}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
				requests++
				if requests > 1 {
					t.Fatal("extra model request")
				}
				return reviewResponse(provider.ToolCall{ID: "effect", Name: "edit", Arguments: "{}"}), nil
			}}
			promoted := false
			_, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Budget: gauge, Loop: cfg, StartFromTurn: func(_ context.Context, h budget.Handoff) (budget.Handle, error) {
				promoted = true
				want := loop.OK
				if state == tools.Unknown {
					want = loop.Unknown
				}
				if len(h.Effects) != 1 || h.Effects[0].Code != want || h.Effects[0].Key != turn.EffectKey("turn", "edit", "document", "effect") {
					t.Errorf("lost receipt: %+v", h.Effects)
				}
				return budget.Handle{ID: "run", State: "working"}, nil
			}})
			if err != nil || !promoted || executed != 1 {
				t.Fatalf("err=%v promoted=%v executed=%d", err, promoted, executed)
			}
		})
	}
}

func TestOutcomeQualitySurvivesReopenAndDoesNotTuneMeter(t *testing.T) {
	db, dbConfig := database(t)
	store := turn.New(db)
	cfg, _ := budgetConfig(t, func(int) []provider.ToolCall { return nil })
	if _, err := store.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Loop: cfg}); err != nil {
		t.Fatal(err)
	}
	want := turn.Quality{Corrections: 2, Repeats: 1, Rework: 3, NotAsked: 1}
	for range 2 {
		if err := store.SetQuality(t.Context(), "turn", want); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetQuality(t.Context(), "turn", turn.Quality{Rework: -1}); !errors.Is(err, turn.ErrInvalid) {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	dbConfig.Epoch++
	result, err := turn.New(open(t, dbConfig)).Outcome(t.Context(), "turn")
	if err != nil || result.Quality != want || result.Meter.Level != budget.Under || result.Meter.Rounds != 0 {
		t.Fatalf("outcome=%+v err=%v", result, err)
	}
}

func TestContinuationRestoresBudgetAfterReopen(t *testing.T) {
	db, dbConfig := database(t)
	gauge := (budget.Meter{}).Start("turn", time.Now())
	for range 6 {
		gauge.Round()
	}
	cfg, _ := budgetConfig(t, func(int) []provider.ToolCall { return nil })
	if _, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Loop: cfg, Budget: gauge}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	dbConfig.Epoch++
	db = open(t, dbConfig)
	id, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: "conversation", Source: "continuation", NativeID: "next"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg, executed := budgetConfig(t, func(request int) []provider.ToolCall {
		if request > 1 {
			return nil
		}
		return []provider.ToolCall{{ID: "a", Name: "read", Arguments: "{}"}}
	})
	store := turn.New(db)
	out, err := store.Run(t.Context(), turn.Options{ID: "next", Conversation: "conversation", InboxID: id.ID, Loop: cfg, Continuation: "turn"})
	if err != nil || *executed != 0 || len(out.Results) != 1 || out.Results[0].Code != turn.BudgetReached {
		t.Fatalf("out=%+v executed=%d err=%v", out, *executed, err)
	}
	result, err := store.Outcome(t.Context(), "next")
	if err != nil || result.Meter.Rounds != 7 || !result.Meter.ReceivedAt.Equal(gauge.Snapshot().ReceivedAt) {
		t.Fatalf("outcome=%+v err=%v", result, err)
	}
}

func TestQueuedCallDoesNotStartAfterToolWallBudget(t *testing.T) {
	for _, elapsed := range []time.Duration{31 * time.Second, time.Minute} {
		t.Run(elapsed.String(), func(t *testing.T) {
			now := time.Unix(100, 0)
			gauge := (budget.Meter{Now: func() time.Time { return now }}).Start("turn", now)
			executed := 0
			spec := reviewSpec("edit", func(raw json.RawMessage) (tools.Input, error) { return reviewInput(raw), nil })
			db, _, tool := reviewDispatch(t, spec, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
				executed++
				now = now.Add(elapsed)
				return tools.Result{State: tools.Applied, Revision: "r2"}, nil
			})
			requests := 0
			cfg := loop.Config{Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"edit"}`)}}, Tools: map[string]loop.Tool{"edit": tool}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
				requests++
				if requests > 1 {
					return reviewResponse(), nil
				}
				return reviewResponse(provider.ToolCall{ID: "a", Name: "edit", Arguments: "{}"}, provider.ToolCall{ID: "b", Name: "edit", Arguments: "{}"}), nil
			}}
			out, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Budget: gauge, Loop: cfg, StartFromTurn: func(context.Context, budget.Handoff) (budget.Handle, error) {
				return budget.Handle{ID: "run", State: "working"}, nil
			}})
			if err != nil || executed != 1 || len(out.Results) != 2 || out.Results[1].Code != turn.BudgetReached {
				t.Fatalf("out=%+v executions=%d err=%v", out, executed, err)
			}
		})
	}
}

func TestSoftBudgetRejectsCallsAndKeepsHandoffToolsAndPrefix(t *testing.T) {
	db, _ := database(t)
	gauge := (budget.Meter{}).Start("turn", time.Now())
	for range 6 {
		gauge.Round()
	}
	cfg, executed := budgetConfig(t, func(request int) []provider.ToolCall {
		if request > 1 {
			return nil
		}
		return []provider.ToolCall{{ID: "a", Name: "read", Arguments: "{}"}, {ID: "b", Name: "start_run", Arguments: "{}"}, {ID: "c", Name: "dispatch_work", Arguments: "{}"}}
	})
	original := cfg.Stream
	cfg.Stream = func(ctx context.Context, req provider.Request) (provider.Stream, error) {
		if !reflect.DeepEqual(req.Tools, cfg.Request.Tools) {
			t.Error("soft budget changed tool declarations")
		}
		return original(ctx, req)
	}
	out, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Loop: cfg, Budget: gauge})
	if err != nil || *executed != 2 || len(out.Results) != 3 || out.Results[0].Code != turn.BudgetReached || out.Results[1].Code != loop.OK || out.Results[2].Code != loop.OK || gauge.Snapshot().Rounds != 7 {
		t.Fatalf("out=%+v executions=%d gauge=%+v err=%v", out, *executed, gauge.Snapshot(), err)
	}
}

func TestHardBudgetPromotesAllGatheredStateWithoutAnotherModelRequest(t *testing.T) {
	db, _ := database(t)
	cfg, executed := budgetConfig(t, func(request int) []provider.ToolCall {
		if request > 10 {
			t.Fatal("hard budget made another model request")
		}
		return []provider.ToolCall{{ID: fmt.Sprint(request), Name: "read", Arguments: "{}"}}
	})
	promotions := 0
	store := turn.New(db)
	out, err := store.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Loop: cfg, StartFromTurn: func(_ context.Context, h budget.Handoff) (budget.Handle, error) {
		promotions++
		if len(h.Results) != 10 || len(h.Items) != 21 || string(h.Items[0]) != string(cfg.Request.Items[0]) || len(h.Effects) != 0 {
			t.Errorf("lost gathered state: %+v", h)
		}
		return budget.Handle{ID: "run", State: "working"}, nil
	}})
	if err != nil || promotions != 1 || *executed != 6 || len(out.Results) != 10 {
		t.Fatalf("out=%+v promoted=%d executed=%d err=%v", out, promotions, *executed, err)
	}
	result, err := store.Outcome(t.Context(), "turn")
	if err != nil || result.Handle.ID != "run" || result.Meter.Rounds != 10 {
		t.Fatalf("outcome=%+v err=%v", result, err)
	}
}

func TestReportTurnNeverExecutesOrReturnsToModelForTools(t *testing.T) {
	for _, attemptsTool := range []bool{false, true} {
		t.Run(fmt.Sprint(attemptsTool), func(t *testing.T) {
			db, _ := database(t)
			cfg, executed := budgetConfig(t, func(request int) []provider.ToolCall {
				if request > 1 {
					t.Fatal("report made a tool round trip")
				}
				if attemptsTool {
					return []provider.ToolCall{{ID: "a", Name: "start_run", Arguments: "{}"}}
				}
				return nil
			})
			stream := cfg.Stream
			cfg.Stream = func(ctx context.Context, req provider.Request) (provider.Stream, error) {
				if len(req.Tools) != 0 {
					t.Error("report declared tools")
				}
				return stream(ctx, req)
			}
			store := turn.New(db)
			_, err := store.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Loop: cfg, Report: true})
			if *executed != 0 || attemptsTool && !errors.Is(err, turn.ErrReportTools) || !attemptsTool && err != nil {
				t.Fatalf("executed=%d err=%v", *executed, err)
			}
			result, err := store.Outcome(t.Context(), "turn")
			if err != nil || result.Meter.Rounds != 0 {
				t.Fatalf("outcome=%+v err=%v", result, err)
			}
		})
	}
}
