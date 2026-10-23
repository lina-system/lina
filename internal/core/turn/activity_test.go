// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func activityConfig(tool loop.Tool, calls ...provider.ToolCall) loop.Config {
	requests := 0
	return loop.Config{
		Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"read_skill"}`)}},
		Tools:   map[string]loop.Tool{"read_skill": tool},
		Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			requests++
			if requests == 1 {
				return reviewResponse(calls...), nil
			}
			return reviewResponse(), nil
		},
	}
}

func assertActivityCommitted(t *testing.T, db *canon.Store, ctx context.Context, a turn.ToolActivity) {
	t.Helper()
	err := db.Read(ctx, func(tx *canon.Tx) error {
		var raw, result []byte
		if err := tx.QueryRowContext(ctx, "SELECT call,result FROM turn_calls WHERE turn_id='turn' AND call_id=?", a.CallID).Scan(&raw, &result); err != nil {
			return err
		}
		var c provider.ToolCall
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		if a.ToolName != c.Name {
			t.Errorf("tool name=%q registered=%q", a.ToolName, c.Name)
		}
		if a.Started {
			if len(result) != 0 || a.Code != "" || a.Duration != 0 {
				t.Errorf("start=%+v result=%s", a, result)
			}
		} else {
			var r loop.Result
			if err := json.Unmarshal(result, &r); err != nil {
				return err
			}
			if a.Code != r.Code || a.Duration < 0 {
				t.Errorf("finish=%+v result=%+v", a, r)
			}
		}
		return nil
	})
	if err != nil {
		t.Errorf("activity before commit: %+v: %v", a, err)
	}
}

func TestToolActivityConcurrentDurableBoundaries(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	phases := map[string]int{}
	observedAt := map[string]time.Time{}
	executedAt := map[string]time.Time{}
	entered := make(chan struct{}, 2)
	allStarted := make(chan struct{})
	var starts atomic.Int32
	var notified atomic.Int32
	tool := loop.Tool{Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(ctx context.Context, c provider.ToolCall) loop.Result {
		mu.Lock()
		if phases[c.ID] != 1 {
			t.Errorf("execution before start: %s", c.ID)
		}
		phases[c.ID] = 2
		mu.Unlock()
		entered <- struct{}{}
		if c.ID == "a" {
			for range 2 {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Error("tools did not execute concurrently")
				}
			}
		}
		mu.Lock()
		executedAt[c.ID] = time.Now()
		mu.Unlock()
		return loop.Result{Code: loop.OK, Output: json.RawMessage(`{"private":"output"}`)}
	}}
	gauge := (budget.Meter{}).Start("turn", time.Now())
	out, err := turn.New(db).Run(ctx, turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Budget: gauge,
		Loop:            activityConfig(tool, provider.ToolCall{ID: "a", Name: "read_skill", Arguments: `{"private":"args"}`}, provider.ToolCall{ID: "b", Name: "read_skill", Arguments: `{}`}),
		ResultCommitted: func() { notified.Add(1) },
		ToolActivity: func(ctx context.Context, a turn.ToolActivity) error {
			assertActivityCommitted(t, db, ctx, a)
			mu.Lock()
			want := 2
			if a.Started {
				want = 0
				observedAt[a.CallID] = time.Now()
			} else if elapsed := executedAt[a.CallID].Sub(observedAt[a.CallID]); a.Duration < elapsed {
				t.Errorf("duration %v excludes invocation time %v", a.Duration, elapsed)
			}
			if phases[a.CallID] != want {
				t.Errorf("activity order: %+v phase=%d", a, phases[a.CallID])
			}
			phases[a.CallID]++
			mu.Unlock()
			if a.Started {
				if starts.Add(1) == 2 {
					close(allStarted)
				}
				select {
				case <-allStarted:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
	})
	if err != nil || len(out.Results) != 2 || notified.Load() != 2 || gauge.Snapshot().Rounds != 1 || !reflect.DeepEqual(phases, map[string]int{"a": 3, "b": 3}) {
		t.Fatalf("out=%+v err=%v phases=%v notified=%d meter=%+v", out, err, phases, notified.Load(), gauge.Snapshot())
	}
}

func TestToolActivityFailedStartPreventsExecutionAndClosesActivity(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	startErr := errors.New("activity start failed")
	var events []turn.ToolActivity
	var notified atomic.Int32
	tool := loop.Tool{Validate: func(provider.ToolCall) (string, error) { return "same-target", nil }, Execute: func(context.Context, provider.ToolCall) loop.Result {
		t.Error("execute after failed activity start")
		return loop.Result{Code: loop.OK}
	}}
	gauge := (budget.Meter{}).Start("turn", time.Now())
	store := turn.New(db)
	out, err := store.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Budget: gauge,
		Loop:            activityConfig(tool, provider.ToolCall{ID: "a", Name: "read_skill", Arguments: `{}`}, provider.ToolCall{ID: "b", Name: "read_skill", Arguments: `{}`}),
		ResultCommitted: func() { notified.Add(1) }, ToolActivity: func(ctx context.Context, a turn.ToolActivity) error {
			assertActivityCommitted(t, db, ctx, a)
			events = append(events, a)
			if a.Started {
				return startErr
			}
			return nil
		},
	})
	if !errors.Is(err, startErr) || len(events) != 2 || events[0].CallID != "a" || !events[0].Started || events[1].CallID != "a" || events[1].Started || events[1].Code != loop.Aborted || notified.Load() != 2 || gauge.Snapshot().Rounds != 1 {
		t.Fatalf("out=%+v err=%v events=%+v notified=%d meter=%+v", out, err, events, notified.Load(), gauge.Snapshot())
	}
	row, err := store.Read(t.Context(), "turn")
	if err != nil || len(row.Results) != 2 || !reflect.DeepEqual(row.Results, out.Results) {
		t.Fatalf("row=%+v out=%+v err=%v", row, out, err)
	}
	for _, r := range row.Results {
		if r.Code != loop.Aborted {
			t.Errorf("result=%+v", r)
		}
	}
}

func TestToolActivityFinishFailureAndCancellationPreserveReceipts(t *testing.T) {
	t.Parallel()
	for _, state := range []tools.State{tools.Applied, tools.Unknown} {
		for _, cancelled := range []bool{false, true} {
			t.Run(string(state)+"/cancel="+map[bool]string{false: "false", true: "true"}[cancelled], func(t *testing.T) {
				spec := reviewSpec("read_skill", func(raw json.RawMessage) (tools.Input, error) { return reviewInput(raw), nil })
				db, svc, tool := reviewDispatch(t, spec, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
					return tools.Result{State: state, Revision: "r2", Body: json.RawMessage(`{"receipt":true}`)}, nil
				})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				execute := tool.Execute
				tool.Execute = func(ctx context.Context, c provider.ToolCall) loop.Result {
					r := execute(ctx, c)
					if cancelled {
						cancel()
					}
					return r
				}
				finishErr := errors.New("activity finish failed")
				var events []turn.ToolActivity
				var notified atomic.Int32
				gauge := (budget.Meter{}).Start("turn", time.Now())
				store := turn.New(db)
				out, err := store.Run(ctx, turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Budget: gauge,
					Loop:            activityConfig(tool, provider.ToolCall{ID: "a", Name: "read_skill", Arguments: `{}`}, provider.ToolCall{ID: "queued", Name: "read_skill", Arguments: `{}`}),
					ResultCommitted: func() { notified.Add(1) }, ToolActivity: func(ctx context.Context, a turn.ToolActivity) error {
						assertActivityCommitted(t, db, ctx, a)
						events = append(events, a)
						if !a.Started {
							if ctx.Err() != nil {
								t.Errorf("finish uses cancelled context: %v", ctx.Err())
							}
							if _, ok := ctx.Deadline(); !ok {
								t.Error("finish lacks cleanup deadline")
							}
							return finishErr
						}
						return nil
					},
				})
				if !errors.Is(err, finishErr) || cancelled && !errors.Is(err, context.Canceled) || len(events) != 2 || events[0].CallID != "a" || events[1].CallID != "a" || notified.Load() != 2 || gauge.Snapshot().Rounds != 1 {
					t.Fatalf("out=%+v err=%v events=%+v notified=%d meter=%+v", out, err, events, notified.Load(), gauge.Snapshot())
				}
				receipt, err := svc.Effect(t.Context(), turn.EffectKey("turn", "read_skill", "document", "a"))
				if err != nil || receipt.Result.State != state {
					t.Fatalf("receipt=%+v err=%v", receipt, err)
				}
				row, err := store.Read(t.Context(), "turn")
				if err != nil || len(row.Results) != 2 || !reflect.DeepEqual(row.Results, out.Results) {
					t.Fatalf("row=%+v out=%+v err=%v", row, out, err)
				}
				code := loop.OK
				if state == tools.Unknown {
					code = loop.Unknown
				}
				if row.Results[0].Code != code || string(row.Results[0].Output) != string(receipt.Result.Body) || row.Results[1].Code != loop.Aborted {
					t.Fatalf("results=%+v receipt=%+v", row.Results, receipt)
				}
			})
		}
	}
}

func TestToolActivityDoesNotFinishBeforeResultCommit(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	var events []turn.ToolActivity
	tool := loop.Tool{Validate: func(provider.ToolCall) (string, error) { return "", nil }, Execute: func(context.Context, provider.ToolCall) loop.Result { return loop.Result{Code: loop.OK} }}
	_, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db),
		Prepare: func(ctx context.Context) error {
			return db.Write(ctx, func(tx *canon.Tx) error {
				_, err := tx.ExecContext(ctx, "CREATE TRIGGER reject_result BEFORE UPDATE OF result ON turn_calls BEGIN SELECT RAISE(ABORT, 'result rejected'); END")
				return err
			})
		},
		Loop:         activityConfig(tool, provider.ToolCall{ID: "a", Name: "read_skill", Arguments: `{}`}),
		ToolActivity: func(_ context.Context, a turn.ToolActivity) error { events = append(events, a); return nil },
	})
	if err == nil || len(events) != 1 || !events[0].Started {
		t.Fatalf("err=%v events=%+v", err, events)
	}
}
