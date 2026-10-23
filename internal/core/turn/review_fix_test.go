// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func reviewDispatch(t *testing.T, spec tools.Spec, handler tools.Handler) (*canon.Store, *authority.Service, loop.Tool) {
	t.Helper()
	db, cfg := database(t)
	auth := grant.Request{GrantID: "grant", MainID: "main", Epoch: cfg.Epoch, TaskID: "turn", Host: "host", OSUser: "user", Person: "person", Agent: "agent", Revision: "r1"}
	svc, err := authority.Open(t.Context(), db, authority.Config{Epoch: cfg.Epoch, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: spec.Name, Target: "document", Write: true}}, Revisions: map[string]string{"document": "r1"}}, grant.Environment{Epoch: cfg.Epoch, Now: time.Now(), OSPermitted: true, Revision: "r1"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(svc, leaser{})
	if err = reg.Register(spec, handler); err != nil {
		t.Fatal(err)
	}
	return db, svc, turn.Tool(reg, spec, tools.Call{Auth: auth}, "turn")
}
func reviewSpec(name string, schema tools.Schema) tools.Spec {
	return tools.Spec{Name: name, Conc: 2, Deadline: 10 * time.Second, Placement: tools.Core, Schema: schema}
}
func reviewInput(args json.RawMessage) tools.Input {
	return tools.Input{Arguments: args, Target: "document", Write: true, What: "edit", Where: "document", ScopeKey: "edit", Undoable: true}
}
func reviewResponse(calls ...provider.ToolCall) provider.Stream {
	var events []provider.Event
	for i, c := range calls {
		item := map[string]string{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": c.Arguments}
		if c.Custom {
			item["type"] = "custom_tool_call"
			item["input"] = c.Arguments
			delete(item, "arguments")
		}
		raw, _ := json.Marshal(item)
		events = append(events, provider.Event{Kind: provider.ItemDone, Index: int64(i), Item: raw}, provider.Event{Kind: provider.ToolCallReady, Index: int64(i), Call: &c})
	}
	return &stream{append(events, provider.Event{Kind: provider.Terminal})}
}

func TestCancellationPreservesCommittedDispatchResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		state tools.State
		code  loop.Code
	}{{tools.Applied, loop.OK}, {tools.Unknown, loop.Unknown}} {
		t.Run(string(tc.state), func(t *testing.T) {
			runs := 0
			spec := reviewSpec("edit", func(raw json.RawMessage) (tools.Input, error) { return reviewInput(raw), nil })
			db, svc, tool := reviewDispatch(t, spec, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
				runs++
				return tools.Result{State: tc.state, Body: json.RawMessage(`{"receipt":true}`), Revision: "r2"}, nil
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			dispatch := tool.Execute
			tool.Execute = func(ctx context.Context, c provider.ToolCall) loop.Result {
				result := dispatch(ctx, c)
				// The registry receipt is committed before cancellation reaches the loop.
				cancel()
				return result
			}
			calls := []provider.ToolCall{{ID: "a", Name: "edit", Arguments: `{}`}, {ID: "b", Name: "edit", Arguments: `{}`}}
			cfg := loop.Config{Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"edit"}`)}}, Tools: map[string]loop.Tool{"edit": tool}, Stream: func(context.Context, provider.Request) (provider.Stream, error) { return reviewResponse(calls...), nil }}
			store := turn.New(db)
			out, err := store.Run(ctx, turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Loop: cfg})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("run error: %v", err)
			}
			receipt, err := svc.Effect(t.Context(), turn.EffectKey("turn", "edit", "document", "a"))
			if err != nil || receipt.Result.State != tc.state {
				t.Fatalf("receipt: %+v %v", receipt, err)
			}
			row, err := store.Read(t.Context(), "turn")
			if err != nil || runs != 1 || len(row.Results) != 2 || len(out.Results) != 2 {
				t.Fatalf("row=%+v out=%+v runs=%d err=%v", row, out, runs, err)
			}
			for _, results := range [][]loop.Result{row.Results, out.Results} {
				if results[0].Code != tc.code || string(results[0].Output) != string(receipt.Result.Body) {
					t.Errorf("loop result disagrees with committed receipt: %+v receipt=%+v", results[0], receipt.Result)
				}
				if results[1].ID != "b" || results[1].Code != loop.Aborted {
					t.Errorf("unstarted call: %+v", results[1])
				}
			}
		})
	}
}

func TestCustomToolNormalizesRawInputBeforeDispatch(t *testing.T) {
	t.Parallel()
	const patch = "*** Begin Patch\n*** Update File: document\n-old\n+new\n*** End Patch"
	sawRaw := false
	spec := reviewSpec("apply_patch", func(raw json.RawMessage) (tools.Input, error) {
		var args struct {
			Patch string `json:"patch"`
		}
		if json.Valid(raw) {
			if err := json.Unmarshal(raw, &args); err != nil {
				return tools.Input{}, err
			}
		} else {
			args.Patch = string(raw)
			sawRaw = true
		}
		if args.Patch == "" {
			return tools.Input{}, tools.ErrInvalid
		}
		normalized, err := json.Marshal(args)
		return reviewInput(normalized), err
	})
	runs := 0
	var received string
	db, svc, tool := reviewDispatch(t, spec, func(_ context.Context, _ lease.Lease, in tools.Input) (tools.Result, error) {
		runs++
		var args struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil {
			return tools.Result{State: tools.Failed}, err
		}
		received = args.Patch
		return tools.Result{State: tools.Applied, Revision: "r2"}, nil
	})
	c := provider.ToolCall{ID: "patch", Name: "apply_patch", Arguments: patch, Custom: true}
	requests := 0
	cfg := loop.Config{Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"custom","name":"apply_patch"}`)}}, Tools: map[string]loop.Tool{"apply_patch": tool}, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		requests++
		if requests == 1 {
			return reviewResponse(c), nil
		}
		return reviewResponse(), nil
	}}
	store := turn.New(db)
	out, err := store.Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Loop: cfg})
	if err != nil || !sawRaw || runs != 1 || received != patch || len(out.Results) != 1 || out.Results[0].Code != loop.OK {
		t.Fatalf("out=%+v err=%v raw=%v runs=%d received=%q", out, err, sawRaw, runs, received)
	}
	receipt, err := svc.Effect(t.Context(), turn.EffectKey("turn", "apply_patch", "document", "patch"))
	if err != nil || receipt.State != tools.Applied {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestCustomNormalizationKeepsFunctionAndOutputJSONValidation(t *testing.T) {
	t.Parallel()
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid_function_input", true: "invalid_normalized_custom_input"}[custom], func(t *testing.T) {
			schemaCalls, runs := 0, 0
			spec := reviewSpec("edit", func(raw json.RawMessage) (tools.Input, error) { schemaCalls++; return reviewInput(raw), nil })
			_, _, tool := reviewDispatch(t, spec, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
				runs++
				return tools.Result{State: tools.Applied, Revision: "r2"}, nil
			})
			c := provider.ToolCall{ID: "a", Name: "edit", Arguments: "plain input", Custom: custom}
			if _, err := tool.Validate(c); err == nil {
				t.Error("invalid input accepted")
			}
			if result := tool.Execute(t.Context(), c); result.Code != loop.Invalid || runs != 0 {
				t.Fatalf("result=%+v runs=%d", result, runs)
			}
			if custom && schemaCalls == 0 || !custom && schemaCalls != 0 {
				t.Fatalf("custom=%v schema calls=%d", custom, schemaCalls)
			}
		})
	}
}
