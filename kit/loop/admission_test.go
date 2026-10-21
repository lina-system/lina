// SPDX-License-Identifier: Apache-2.0
package loop_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestAdmissionRechecksQueuedCallsWithoutRewritingRefusal(t *testing.T) {
	var reached atomic.Bool
	requests, executed, after := 0, 0, 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		if requests == 1 {
			return response(call("a"), call("b")), nil
		}
		return response(), nil
	}, func(_ context.Context, c provider.ToolCall) loop.Result {
		executed++
		reached.Store(true)
		return success(c)
	})
	registered := cfg.Tools["edit"]
	registered.Validate = func(provider.ToolCall) (string, error) { return "same-target", nil }
	cfg.Tools["edit"] = registered
	cfg.Admit = func(context.Context, provider.ToolCall) *loop.Result {
		if reached.Load() {
			return &loop.Result{Code: loop.BudgetReached}
		}
		return nil
	}
	cfg.After = func(_ context.Context, _ provider.ToolCall, result loop.Result) loop.Result { after++; return result }
	out, err := loop.Run(t.Context(), cfg)
	if err != nil || executed != 1 || after != 1 || len(out.Results) != 2 || out.Results[1].ID != "b" || out.Results[1].Code != loop.BudgetReached {
		t.Fatalf("out=%+v executed=%d after=%d err=%v", out, executed, after, err)
	}
}

func TestAdmissionPrecedesSchemaAndAvailability(t *testing.T) {
	requests := 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		if requests > 1 {
			return response(), nil
		}
		return response(provider.ToolCall{ID: "invalid", Name: "edit", Arguments: "invalid JSON"}, provider.ToolCall{ID: "missing", Name: "missing", Arguments: "{}"}), nil
	}, func(context.Context, provider.ToolCall) loop.Result {
		t.Error("blocked executor ran")
		return loop.Result{Code: loop.OK}
	})
	cfg.Admit = func(context.Context, provider.ToolCall) *loop.Result { return &loop.Result{Code: loop.BudgetReached} }
	out, err := loop.Run(t.Context(), cfg)
	if err != nil || len(out.Results) != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	for _, result := range out.Results {
		if result.Code != loop.BudgetReached {
			t.Fatal(result)
		}
	}
}
