// SPDX-License-Identifier: Apache-2.0
package loop_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func named(id, name string) provider.ToolCall {
	c := call(id)
	c.Name = name
	return c
}

func declare(cfg *loop.Config, names ...string) {
	cfg.Request.Tools = nil
	for _, name := range names {
		raw, _ := json.Marshal(map[string]any{"type": "function", "name": name, "parameters": map[string]any{"type": "object"}})
		cfg.Request.Tools = append(cfg.Request.Tools, raw)
	}
}

// A work call that names the binding call's target must start only after that
// call committed its result.
func TestGateRunsAfterTheCallItNames(t *testing.T) {
	var committed atomic.Bool
	started := make(chan struct{}, 1)
	rounds := 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		rounds++
		if rounds == 1 {
			return response(named("bind", "bind"), named("work", "work")), nil
		}
		return response(), nil
	}, func(context.Context, provider.ToolCall) loop.Result { return success(provider.ToolCall{}) })
	declare(&cfg, "bind", "work")
	cfg.Tools["bind"] = loop.Tool{
		Validate: func(provider.ToolCall) (string, error) { return "input-binding:one", nil },
		Execute: func(context.Context, provider.ToolCall) loop.Result {
			select {
			case <-started:
				t.Error("work ran before the bind call finished")
			case <-time.After(500 * time.Millisecond):
			}
			committed.Store(true)
			return success(provider.ToolCall{ID: "bind"})
		},
	}
	cfg.Tools["work"] = loop.Tool{
		Validate: func(provider.ToolCall) (string, error) { return "work:one", nil },
		Gate:     func(provider.ToolCall) string { return "input-binding:one" },
		Execute: func(context.Context, provider.ToolCall) loop.Result {
			started <- struct{}{}
			if !committed.Load() {
				t.Error("work ran before the bind call committed")
			}
			return success(provider.ToolCall{ID: "work"})
		},
	}
	if _, err := loop.Run(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
}

// A shared gate orders every call behind the binding call without serializing
// the calls with each other, and another input's work does not wait for it.
func TestGateKeepsOtherTargetsAndInputsParallel(t *testing.T) {
	crossed := make(chan struct{})
	var started atomic.Int32
	both := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	rounds := 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		rounds++
		if rounds == 1 {
			return response(named("bind-a", "bind-a"), named("work-a", "work-a"), named("work-b", "work-b"), named("work-c", "work-c")), nil
		}
		return response(), nil
	}, func(context.Context, provider.ToolCall) loop.Result { return success(provider.ToolCall{}) })
	declare(&cfg, "bind-a", "work-a", "work-b", "work-c")
	cfg.Tools["bind-a"] = loop.Tool{
		Validate: func(provider.ToolCall) (string, error) { return "input-binding:a", nil },
		Execute: func(context.Context, provider.ToolCall) loop.Result {
			select {
			case <-crossed:
			case <-ctx.Done():
				t.Error("another input's work waited for this binding call")
			}
			return success(provider.ToolCall{ID: "bind-a"})
		},
	}
	work := func(id string) loop.Tool {
		return loop.Tool{
			Validate: func(provider.ToolCall) (string, error) { return "work:" + id, nil },
			Gate:     func(provider.ToolCall) string { return "input-binding:a" },
			Execute: func(context.Context, provider.ToolCall) loop.Result {
				if started.Add(1) == 2 {
					close(both)
				}
				select {
				case <-both:
				case <-ctx.Done():
					t.Error("calls behind the same gate did not run in parallel")
				}
				return success(provider.ToolCall{ID: "work-" + id})
			},
		}
	}
	cfg.Tools["work-a"] = work("a")
	cfg.Tools["work-b"] = work("b")
	cfg.Tools["work-c"] = loop.Tool{
		Validate: func(provider.ToolCall) (string, error) { return "work:c", nil },
		Gate:     func(provider.ToolCall) string { return "input-binding:b" },
		Execute: func(context.Context, provider.ToolCall) loop.Result {
			close(crossed)
			return success(provider.ToolCall{ID: "work-c"})
		},
	}
	cfg.Parallel = 4
	if _, err := loop.Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

// The gate works in either direction: a work call that appears before the
// binding call still waits for it.
func TestGateWaitsForACallLaterInTheResponse(t *testing.T) {
	var committed atomic.Bool
	var order []string
	var mu sync.Mutex
	rounds := 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		rounds++
		if rounds == 1 {
			return response(named("work", "work"), named("bind", "bind")), nil
		}
		return response(), nil
	}, func(context.Context, provider.ToolCall) loop.Result { return success(provider.ToolCall{}) })
	declare(&cfg, "bind", "work")
	cfg.Tools["bind"] = loop.Tool{
		Validate: func(provider.ToolCall) (string, error) { return "input-binding:one", nil },
		Execute: func(context.Context, provider.ToolCall) loop.Result {
			committed.Store(true)
			mu.Lock()
			order = append(order, "bind")
			mu.Unlock()
			return success(provider.ToolCall{ID: "bind"})
		},
	}
	cfg.Tools["work"] = loop.Tool{
		Validate: func(provider.ToolCall) (string, error) { return "work:one", nil },
		Gate:     func(provider.ToolCall) string { return "input-binding:one" },
		Execute: func(context.Context, provider.ToolCall) loop.Result {
			if !committed.Load() {
				t.Error("work ran before the later binding call")
			}
			mu.Lock()
			order = append(order, "work")
			mu.Unlock()
			return success(provider.ToolCall{ID: "work"})
		},
	}
	if _, err := loop.Run(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"bind", "work"}) {
		t.Fatal(order)
	}
}
