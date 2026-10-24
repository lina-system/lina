// SPDX-License-Identifier: Apache-2.0
package loop_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type wire struct {
	events []provider.Event
	err    error
	closed bool
}

func (s *wire) Next(context.Context) (provider.Event, error) {
	if len(s.events) == 0 {
		if s.err != nil {
			return provider.Event{}, s.err
		}
		return provider.Event{}, io.EOF
	}
	e := s.events[0]
	s.events = s.events[1:]
	return e, nil
}
func (*wire) Meta() provider.Meta { return provider.Meta{} }
func (s *wire) Close() error      { s.closed = true; return nil }
func response(calls ...provider.ToolCall) *wire {
	s := &wire{}
	for i, c := range calls {
		b, _ := json.Marshal(map[string]any{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": c.Arguments})
		s.events = append(s.events, provider.Event{Kind: provider.ItemDone, Index: int64(i), Item: b}, provider.Event{Kind: provider.ToolCallReady, Index: int64(i), Call: &c})
	}
	s.events = append(s.events, provider.Event{Kind: provider.Terminal})
	return s
}
func call(id string) provider.ToolCall {
	return provider.ToolCall{ID: id, Name: "edit", Arguments: `{}`}
}
func tool(run func(context.Context, provider.ToolCall) loop.Result) loop.Tool {
	return loop.Tool{Validate: func(c provider.ToolCall) (string, error) {
		if !json.Valid([]byte(c.Arguments)) {
			return "", errors.New("invalid")
		}
		return "", nil
	}, Execute: run}
}
func config(stream loop.StreamFunc, run func(context.Context, provider.ToolCall) loop.Result) loop.Config {
	return loop.Config{Stream: stream, Tools: map[string]loop.Tool{"edit": tool(run)}, Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"edit","parameters":{"type":"object"}}`)}}}
}
func success(c provider.ToolCall) loop.Result {
	return loop.Result{ID: c.ID, Code: loop.OK, Output: json.RawMessage(`{"saved":true}`)}
}

func TestCutOffNeverDispatchesOrRetries(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "cutoff", true: "failure_after_terminal"}[terminal], func(t *testing.T) {
			s := response(call("a"))
			if !terminal {
				s.events = s.events[:len(s.events)-1]
			}
			s.err = io.ErrUnexpectedEOF
			requests, runs := 0, 0
			cfg := config(func(context.Context, provider.Request) (provider.Stream, error) { requests++; return s, nil }, func(_ context.Context, c provider.ToolCall) loop.Result { runs++; return success(c) })
			out, err := loop.Run(t.Context(), cfg)
			if !errors.Is(err, loop.ErrIncomplete) || runs != 0 || requests != 1 || !s.closed || len(out.Items) != 0 || len(out.Results) != 1 || out.Results[0].Code != loop.Incomplete {
				t.Fatalf("out=%+v err=%v runs=%d requests=%d", out, err, runs, requests)
			}
		})
	}
}
func TestValidationHooksParallelResultsAndSteering(t *testing.T) {
	first, second := make(chan struct{}), make(chan struct{})
	var checks []string
	var mu sync.Mutex
	inputs := &loop.Inputs{}
	requests := 0
	transforms := 0
	cfg := config(func(_ context.Context, r provider.Request) (provider.Stream, error) {
		requests++
		if requests == 1 {
			return response(call("a"), call("b")), nil
		}
		if len(r.Items) != 5 {
			t.Errorf("items=%s", r.Items)
		}
		if string(r.Items[len(r.Items)-1]) != `{"role":"user","content":"steer"}` {
			t.Errorf("steering missing: %s", r.Items)
		}
		return response(), nil
	}, func(_ context.Context, c provider.ToolCall) loop.Result {
		mu.Lock()
		n := len(checks)
		mu.Unlock()
		if n != 2 {
			t.Errorf("started before preflight: %d", n)
		}
		if c.ID == "a" {
			close(first)
			<-second
		} else {
			<-first
			inputs.Push(json.RawMessage(`{"role":"user","content":"steer"}`))
			close(second)
		}
		return success(c)
	})
	cfg.Inputs = inputs
	cfg.Transform = func(_ context.Context, r provider.Request) (provider.Request, error) { transforms++; return r, nil }
	cfg.Before = func(_ context.Context, c provider.ToolCall) *loop.Result {
		mu.Lock()
		defer mu.Unlock()
		checks = append(checks, c.ID)
		return nil
	}
	cfg.After = func(_ context.Context, _ provider.ToolCall, r loop.Result) loop.Result {
		r.Output = json.RawMessage(`{"rewritten":true}`)
		return r
	}
	out, err := loop.Run(t.Context(), cfg)
	if err != nil || requests != 2 || transforms != 2 || !reflect.DeepEqual(checks, []string{"a", "b"}) || len(out.Results) != 2 {
		t.Fatalf("out=%+v err=%v requests=%d", out, err, requests)
	}
	for i, id := range []string{"a", "b"} {
		if out.Results[i].ID != id || string(out.Results[i].Output) != `{"rewritten":true}` {
			t.Fatal(out.Results)
		}
	}
}
func TestAbortClosesEveryOpenCall(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	started := make(chan struct{}, 2)
	done := make(chan struct{})
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		return response(call("a"), call("b"), call("c")), nil
	}, func(ctx context.Context, c provider.ToolCall) loop.Result {
		started <- struct{}{}
		<-ctx.Done()
		return loop.Result{ID: c.ID, Code: loop.Aborted}
	})
	cfg.Parallel = 2
	go func() { defer close(done); <-started; <-started; cancel(errors.New("stop")) }()
	out, err := loop.Run(ctx, cfg)
	<-done
	if err == nil || len(out.Results) != 3 {
		t.Fatalf("%+v %v", out, err)
	}
	for i, r := range out.Results {
		if r.ID != []string{"a", "b", "c"}[i] || r.Code != loop.Aborted {
			t.Fatal(out.Results)
		}
	}
}
func TestInvalidDeniedUnavailableAndOfferedSet(t *testing.T) {
	bad := call("invalid")
	bad.Arguments = "{"
	absent := call("absent")
	absent.Name = "hidden"
	runs := 0
	rounds := 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		rounds++
		if rounds == 1 {
			return response(bad, call("denied"), absent), nil
		}
		return response(), nil
	}, func(_ context.Context, c provider.ToolCall) loop.Result { runs++; return success(c) })
	cfg.Tools["hidden"] = tool(func(_ context.Context, c provider.ToolCall) loop.Result { runs++; return success(c) })
	cfg.Before = func(_ context.Context, c provider.ToolCall) *loop.Result {
		r := loop.Result{Code: loop.Denied}
		return &r
	}
	out, err := loop.Run(t.Context(), cfg)
	if err != nil || runs != 0 || len(out.Results) != 3 {
		t.Fatalf("%+v %v runs=%d", out, err, runs)
	}
	for i, code := range []loop.Code{loop.Invalid, loop.Denied, loop.Unavailable} {
		if out.Results[i].Code != code {
			t.Fatal(out.Results)
		}
	}
}
func TestWriteTargetsSerializeWithoutBlockingOtherTargets(t *testing.T) {
	released := make(chan struct{})
	var active atomic.Int32
	var orderMu sync.Mutex
	var order []string
	rounds := 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		rounds++
		if rounds == 1 {
			return response(call("a"), call("b"), call("c")), nil
		}
		return response(), nil
	}, func(_ context.Context, c provider.ToolCall) loop.Result {
		if c.ID == "c" {
			close(released)
			return success(c)
		}
		if active.Add(1) != 1 {
			t.Error("same-target writes overlap")
		}
		defer active.Add(-1)
		orderMu.Lock()
		order = append(order, c.ID)
		orderMu.Unlock()
		if c.ID == "a" {
			<-released
		}
		return success(c)
	})
	v := cfg.Tools["edit"]
	v.Validate = func(c provider.ToolCall) (string, error) {
		if c.ID == "c" {
			return "other", nil
		}
		return "same", nil
	}
	cfg.Tools["edit"] = v
	cfg.Parallel = 2
	if _, err := loop.Run(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"a", "b"}) {
		t.Fatal(order)
	}
}
func TestCommitFailurePreventsDispatch(t *testing.T) {
	runs := 0
	failure := errors.New("commit failed")
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) { return response(call("a")), nil }, func(_ context.Context, c provider.ToolCall) loop.Result { runs++; return success(c) })
	cfg.Journal = loop.Journal{Calls: func(context.Context, int, []provider.ToolCall) error { return failure }}
	if _, err := loop.Run(t.Context(), cfg); !errors.Is(err, failure) || runs != 0 {
		t.Fatalf("%v runs=%d", err, runs)
	}
}

func TestMissingAssistantItemNeverDispatches(t *testing.T) {
	c := call("a")
	runs := 0
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		return &wire{events: []provider.Event{{Kind: provider.ToolCallReady, Call: &c}, {Kind: provider.Terminal}}}, nil
	}, func(_ context.Context, c provider.ToolCall) loop.Result { runs++; return success(c) })
	if _, err := loop.Run(t.Context(), cfg); err == nil || runs != 0 {
		t.Fatalf("err=%v runs=%d", err, runs)
	}
}
func TestParallelismCappedAtEight(t *testing.T) {
	var running, peak atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	rounds := 0
	calls := make([]provider.ToolCall, 16)
	for i := range calls {
		calls[i] = call(string(rune('a' + i)))
	}
	cfg := config(func(context.Context, provider.Request) (provider.Stream, error) {
		rounds++
		if rounds == 1 {
			return response(calls...), nil
		}
		return response(), nil
	}, func(ctx context.Context, c provider.ToolCall) loop.Result {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			p := peak.Load()
			if p >= n || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n == 8 {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return success(c)
	})
	cfg.Parallel = 100
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := loop.Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 8 {
		t.Fatalf("peak=%d", peak.Load())
	}
}

func TestSchedulingKeyBarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var released atomic.Bool
	var early atomic.Int32
	gateEntered := make(chan struct{})
	releaseGate := make(chan struct{})
	otherStarted := make(chan struct{})
	var workStarted atomic.Int32
	bothWork := make(chan struct{})
	keys := map[string]string{"gate": "gate", "other": "other", "a": "gate\nwork-a", "b": "gate\nwork-b"}
	rounds := 0
	cfg := loop.Config{
		Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			rounds++
			if rounds == 1 {
				return response(call("gate"), call("other"), call("a"), call("b")), nil
			}
			return response(), nil
		},
		Tools: map[string]loop.Tool{"edit": {
			Validate: func(c provider.ToolCall) (string, error) {
				if !json.Valid([]byte(c.Arguments)) {
					return "", errors.New("invalid")
				}
				return keys[c.ID], nil
			},
			Execute: func(_ context.Context, c provider.ToolCall) loop.Result {
				switch c.ID {
				case "gate":
					close(gateEntered)
					<-releaseGate
					released.Store(true)
				case "other":
					close(otherStarted)
				default:
					if !released.Load() {
						early.Add(1)
					}
					if workStarted.Add(1) == 2 {
						close(bothWork)
					}
					<-bothWork
				}
				return success(c)
			},
		}},
		Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"edit","parameters":{"type":"object"}}`)}},
	}
	go func() {
		<-gateEntered
		<-otherStarted
		close(releaseGate)
	}()
	if _, err := loop.Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if early.Load() != 0 {
		t.Fatalf("dependent calls started before the earlier key: %d", early.Load())
	}
}
