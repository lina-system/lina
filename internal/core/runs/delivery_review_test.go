// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func reusedCall(argument string) provider.Stream {
	data, _ := json.Marshal(map[string]string{"type": "function_call", "call_id": "reused-id", "name": "read", "arguments": argument})
	return &stream{events: []provider.Event{{Kind: provider.ItemDone, Item: data}, {Kind: provider.Terminal}}}
}
func TestRetriedRunMayReuseCallIDForDifferentArguments(t *testing.T) {
	t.Parallel()
	for _, kind := range []runs.ToolKind{runs.Read, runs.Scratch} {
		t.Run(map[runs.ToolKind]string{runs.Read: "read", runs.Scratch: "scratch"}[kind], func(t *testing.T) {
			var rounds, executions atomic.Int64
			s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
				switch rounds.Add(1) {
				case 1:
					return reusedCall(`{"source":"first"}`), nil
				case 2:
					return nil, errors.New("interrupted stream")
				case 3:
					return reusedCall(`{"source":"second"}`), nil
				default:
					return answer(runs.Artifact{Body: "compared", Done: true}), nil
				}
			}, func(cfg *runs.Config) {
				tool := cfg.Tools["read"]
				tool.Kind = kind
				tool.Handler.Execute = func(_ context.Context, c provider.ToolCall) loop.Result {
					executions.Add(1)
					return loop.Result{ID: c.ID, Code: loop.OK, Output: json.RawMessage(`"evidence"`)}
				}
				cfg.Tools["read"] = tool
			})
			id := start(t, s)
			release(t, s, id)
			r := wait(t, s, id)
			if r.Outcome != runs.Complete || executions.Load() != 2 {
				t.Fatalf("reused id blocked new call: outcome=%s executions=%d", r.Outcome, executions.Load())
			}
		})
	}
}
func TestAnsweredQuestionDoesNotReuseUnstartedReceipt(t *testing.T) {
	t.Parallel()
	var allowed atomic.Bool
	var rounds, executions atomic.Int64
	s, _, _ := setupWith(t, func(context.Context, provider.Request) (provider.Stream, error) {
		if rounds.Add(1) <= 2 {
			return reusedCall(`{"source":"named"}`), nil
		}
		return answer(runs.Artifact{Body: "read", Done: true}), nil
	}, func(cfg *runs.Config) {
		tool := cfg.Tools["read"]
		tool.Kind = runs.Scratch
		tool.Authorize = func(context.Context, runs.Record, provider.ToolCall) error {
			if !allowed.Load() {
				return runs.ErrNeedsInput
			}
			return nil
		}
		tool.Handler.Execute = func(_ context.Context, c provider.ToolCall) loop.Result {
			executions.Add(1)
			return loop.Result{ID: c.ID, Code: loop.OK, Output: json.RawMessage(`"evidence"`)}
		}
		cfg.Tools["read"] = tool
	})
	id := start(t, s)
	release(t, s, id)
	if r := wait(t, s, id); r.State != runs.NeedsInput {
		t.Fatalf("state=%s", r.State)
	}
	allowed.Store(true)
	if err := s.Follow(t.Context(), id, "continue"); err != nil {
		t.Fatal(err)
	}
	r := wait(t, s, id)
	if r.Outcome != runs.Complete || executions.Load() != 1 {
		t.Fatalf("old unstarted receipt suppressed new call: outcome=%s executions=%d", r.Outcome, executions.Load())
	}
}

func TestCompletedReadReceiptsAreRetainedOnlyInCheckpoint(t *testing.T) {
	t.Parallel()
	var rounds atomic.Int64
	s, _, _ := setup(t, func(context.Context, provider.Request) (provider.Stream, error) {
		n := rounds.Add(1)
		if n <= 8 {
			return call(fmt.Sprint(n)), nil
		}
		return answer(runs.Artifact{Body: "read all evidence", Done: true}), nil
	})
	id := start(t, s)
	release(t, s, id)
	r := wait(t, s, id)
	if len(r.Receipts) != 0 || len(r.ScratchReceipts) != 0 {
		t.Fatalf("completed reads retained duplicate receipt payloads: receipts=%d scratch=%d", len(r.Receipts), len(r.ScratchReceipts))
	}
	outputs := 0
	for _, item := range r.Checkpoint {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "function_call_output" {
			outputs++
		}
	}
	if outputs != 8 || r.Outcome != runs.Complete {
		t.Fatalf("checkpoint lost results: outputs=%d outcome=%s", outputs, r.Outcome)
	}
}
