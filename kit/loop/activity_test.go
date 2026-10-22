// SPDX-License-Identifier: Apache-2.0
package loop_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestJournalStartFollowsCallsAndPrecedesExecution(t *testing.T) {
	var order []string
	requests := 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		if requests == 1 {
			return response(call("a")), nil
		}
		return response(), nil
	}, func(_ context.Context, c provider.ToolCall) loop.Result {
		order = append(order, "execute")
		return success(c)
	})
	cfg.Journal.Calls = func(context.Context, int, []provider.ToolCall) error { order = append(order, "calls"); return nil }
	cfg.Before = func(context.Context, provider.ToolCall) *loop.Result { order = append(order, "preflight"); return nil }
	cfg.Journal.Start = func(_ context.Context, round, index int, c provider.ToolCall) error {
		if round != 0 || index != 0 || c != call("a") {
			t.Errorf("start coordinates: %d %d %+v", round, index, c)
		}
		order = append(order, "start")
		return nil
	}
	cfg.Journal.Result = func(context.Context, int, int, loop.Result) error { order = append(order, "result"); return nil }
	if _, err := loop.Run(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if want := []string{"calls", "preflight", "start", "execute", "result"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order=%v want=%v", order, want)
	}
}

func TestJournalStartFailureAbortsUnexecutedCallsAndPropagates(t *testing.T) {
	startErr, saveErr := errors.New("start failed"), errors.New("save failed")
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		return response(call("a"), call("b")), nil
	}, func(context.Context, provider.ToolCall) loop.Result {
		t.Error("executor ran after failed start")
		return loop.Result{Code: loop.OK}
	})
	tool := cfg.Tools["edit"]
	tool.Validate = func(provider.ToolCall) (string, error) { return "same-target", nil }
	cfg.Tools["edit"] = tool
	var starts []string
	cfg.Journal.Start = func(_ context.Context, _, _ int, c provider.ToolCall) error {
		starts = append(starts, c.ID)
		return startErr
	}
	saved := make(chan loop.Result, 2)
	cfg.Journal.Result = func(ctx context.Context, _, _ int, r loop.Result) error {
		if ctx.Err() != nil {
			t.Errorf("result cleanup context: %v", ctx.Err())
		}
		saved <- r
		return saveErr
	}
	out, err := loop.Run(t.Context(), cfg)
	if !errors.Is(err, startErr) || !errors.Is(err, saveErr) || !reflect.DeepEqual(starts, []string{"a"}) || len(out.Results) != 2 || len(saved) != 2 {
		t.Fatalf("out=%+v err=%v starts=%v saved=%d", out, err, starts, len(saved))
	}
	for range 2 {
		if r := <-saved; r.Code != loop.Aborted {
			t.Errorf("synthetic result=%+v", r)
		}
	}
}

func TestJournalStartSkipsPreflightAndQueuedAdmissionRefusals(t *testing.T) {
	requests, executions := 0, 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		if requests == 1 {
			return response(call("a"), call("b"), call("denied"), provider.ToolCall{ID: "invalid", Name: "edit", Arguments: "bad"}, provider.ToolCall{ID: "missing", Name: "missing", Arguments: "{}"}), nil
		}
		return response(), nil
	}, func(_ context.Context, c provider.ToolCall) loop.Result { executions++; return success(c) })
	tool := cfg.Tools["edit"]
	validate := tool.Validate
	tool.Validate = func(c provider.ToolCall) (string, error) { _, err := validate(c); return "same-target", err }
	cfg.Tools["edit"] = tool
	cfg.Before = func(_ context.Context, c provider.ToolCall) *loop.Result {
		if c.ID == "denied" {
			return &loop.Result{Code: loop.Denied}
		}
		return nil
	}
	cfg.Admit = func(_ context.Context, c provider.ToolCall) *loop.Result {
		if c.ID == "b" && executions > 0 {
			return &loop.Result{Code: loop.BudgetReached}
		}
		return nil
	}
	var starts []string
	cfg.Journal.Start = func(_ context.Context, _, _ int, c provider.ToolCall) error {
		starts = append(starts, c.ID)
		return nil
	}
	out, err := loop.Run(t.Context(), cfg)
	if err != nil || executions != 1 || !reflect.DeepEqual(starts, []string{"a"}) {
		t.Fatalf("out=%+v err=%v executions=%d starts=%v", out, err, executions, starts)
	}
	want := []loop.Code{loop.OK, loop.BudgetReached, loop.Denied, loop.Invalid, loop.Unavailable}
	if len(out.Results) != len(want) {
		t.Fatalf("results=%+v", out.Results)
	}
	for i, code := range want {
		if out.Results[i].Code != code {
			t.Errorf("result[%d]=%+v want=%s", i, out.Results[i], code)
		}
	}
}

func TestJournalStartRequiresCommittedCompleteCalls(t *testing.T) {
	for _, failure := range []string{"calls", "incomplete"} {
		t.Run(failure, func(t *testing.T) {
			cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
				s := response(call("a"))
				if failure == "incomplete" {
					s.events = s.events[:len(s.events)-1]
				}
				return s, nil
			}, func(context.Context, provider.ToolCall) loop.Result { t.Error("executor ran"); return loop.Result{} })
			cfg.Journal.Calls = func(context.Context, int, []provider.ToolCall) error {
				if failure == "calls" {
					return errors.New("call commit failed")
				}
				return nil
			}
			cfg.Journal.Start = func(context.Context, int, int, provider.ToolCall) error {
				t.Error("start without durable complete calls")
				return nil
			}
			if _, err := loop.Run(t.Context(), cfg); err == nil {
				t.Fatal("missing failure")
			}
		})
	}
}
