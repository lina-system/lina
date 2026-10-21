// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/result"
	"github.com/thisisjun786/lina/kit/tools"
)

type service struct{ presents, saves int }

func (s *service) Present(_ context.Context, _ result.Scope, p result.PresentRequest) (result.Card, error) {
	s.presents++
	return result.Card{ID: "result", Revision: 1, Type: p.Type, Title: p.Title, Summary: p.Summary, Text: "rendered"}, nil
}
func (s *service) Read(context.Context, result.Scope, result.ReadRequest) (result.Result, error) {
	return result.Result{ID: "result", Revision: 1, Body: json.RawMessage(`{"example":true}`)}, nil
}
func (s *service) Save(context.Context, result.Scope, result.SaveRequest) (result.SaveReceipt, error) {
	s.saves++
	return result.SaveReceipt{State: "applied", SavedAs: &result.SavedRef{ID: "record", Version: 1}}, nil
}

type gate struct{ keys []string }

func (g *gate) Begin(_ context.Context, c tools.Call, _ tools.Spec, _ tools.Input) (tools.Ticket, error) {
	g.keys = append(g.keys, c.Key)
	return tools.Ticket{Key: c.Key}, nil
}
func (*gate) Finish(context.Context, tools.Ticket, tools.Result) error { return nil }

type leaser struct{}

func (leaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}
func TestMalformedOnlyReturnsToAgent(t *testing.T) {
	g := &gate{}
	s := &service{}
	r := tools.NewRegistry(g, leaser{})
	bindings, err := coreexec.ResultTools(r, s, result.Scope{PersonID: "person", AgentID: "agent", ConversationID: "conversation", TurnID: "turn"}, authorize, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := provider.ToolCall{ID: "call", Name: "present_result", Arguments: `{"type":"table","title":"Test","summary":"Test","body":{}}`}
	out := bindings[c.Name].Execute(t.Context(), c)
	if !strings.Contains(string(out.Output), `"malformed"`) || s.presents != 0 || len(g.keys) != 0 {
		t.Fatal(out, s.presents, g.keys)
	}
}
func TestSaveNeedsCurrentTurnRequestAndStableKey(t *testing.T) {
	scope := result.Scope{PersonID: "person", AgentID: "agent", ConversationID: "conversation", TurnID: "turn"}
	req := result.SaveRequest{ID: "result", Revision: 1, Destination: result.Destination{Kind: "resource"}}
	raw, _ := json.Marshal(req)
	c := provider.ToolCall{ID: "one", Name: "save_result", Arguments: string(raw)}
	s := &service{}
	g := &gate{}
	bindings, err := coreexec.ResultTools(tools.NewRegistry(g, leaser{}), s, scope, authorize, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := bindings[c.Name].Execute(t.Context(), c)
	if !strings.Contains(string(out.Output), `"not_authorized"`) || s.saves != 0 || len(g.keys) != 0 {
		t.Fatal(out, s.saves, g.keys)
	}
	bindings, err = coreexec.ResultTools(tools.NewRegistry(g, leaser{}), s, scope, authorize, &req)
	if err != nil {
		t.Fatal(err)
	}
	out = bindings[c.Name].Execute(t.Context(), c)
	if !strings.Contains(string(out.Output), `"applied"`) {
		t.Fatal(out)
	}
	c.ID = "two"
	bindings[c.Name].Execute(t.Context(), c)
	if len(g.keys) != 2 || g.keys[0] != g.keys[1] {
		t.Fatal(g.keys)
	}
	c.Arguments = strings.Replace(c.Arguments, `"revision":1`, `"revision":2`, 1)
	out = bindings[c.Name].Execute(t.Context(), c)
	if !strings.Contains(string(out.Output), `"not_authorized"`) || s.saves != 2 {
		t.Fatal(out, s.saves)
	}
}

func authorize(context.Context, string, tools.Input) (tools.Call, error) { return tools.Call{}, nil }
