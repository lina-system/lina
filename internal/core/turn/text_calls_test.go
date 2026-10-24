// SPDX-License-Identifier: Apache-2.0
package turn_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestTextCallCannotBypassApproval(t *testing.T) {
	db, cfg := database(t)
	auth := grant.Request{GrantID: "grant", MainID: "main", Epoch: cfg.Epoch, TaskID: "turn", Host: "host", OSUser: "user", Person: "person", Agent: "agent"}
	svc, err := authority.Open(t.Context(), db, authority.Config{Epoch: cfg.Epoch, CanPresent: func() bool { return true }, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "notify", Target: "destination"}}}, grant.Environment{Epoch: cfg.Epoch, Now: time.Now(), OSPermitted: true}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	spec := tools.Spec{Name: "notify", Plugin: "fixture", Outward: true, Conc: 1, Deadline: time.Second, Placement: tools.Core, Schema: func(raw json.RawMessage) (tools.Input, error) {
		return tools.Input{Arguments: raw, Target: "destination", What: "notify", Where: "destination", ScopeKey: "notify"}, nil
	}}
	reg := tools.NewRegistry(svc, leaser{})
	executions := 0
	if err = reg.Register(spec, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
		executions++
		return tools.Result{State: tools.Applied}, nil
	}); err != nil {
		t.Fatal(err)
	}
	requests := 0
	out, err := turn.New(db).Run(t.Context(), turn.Options{ID: "turn", Conversation: "conversation", InboxID: admit(t, db), Loop: loop.Config{
		Request: provider.Request{Tools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"notify","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}}`)}},
		Tools:   map[string]loop.Tool{"notify": turn.Tool(reg, spec, tools.Call{Auth: auth}, "turn")},
		Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			requests++
			if requests > 1 {
				return reviewResponse(), nil
			}
			raw, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": "[tool_use name=_notify]\n{\"text\":\"original\"}"}}})
			return &stream{[]provider.Event{{Kind: provider.ItemDone, Item: raw}, {Kind: provider.Terminal}}}, nil
		},
	}})
	if err != nil || executions != 0 || requests != 2 || len(out.Results) != 1 || out.Results[0].Code != loop.AwaitingPerson {
		t.Fatalf("out=%+v err=%v requests=%d executions=%d", out, err, requests, executions)
	}
	pending, err := svc.Pending(t.Context())
	if err != nil || len(pending) != 1 || string(pending[0].Arguments) != `{"text":"original"}` {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}
