// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

func (r *conversationRuntime) prepareWebTools(turnID string, _ *toolPreparation, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	p, err := r.selectedWebProvider()
	if err != nil {
		return nil, err
	}
	return r.bindWebTools(turnID, p, bindings)
}

func (r *conversationRuntime) bindWebTools(turnID string, p coreexec.WebProvider, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	if p == nil {
		return nil, nil
	}
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	specs, err := coreexec.RegisterWeb(registry, p)
	if err != nil {
		return nil, err
	}
	for _, spec := range specs {
		validated := turn.Tool(registry, spec, tools.Call{}, turnID)
		bindings[spec.Name] = loop.Tool{Validate: validated.Validate, Execute: func(ctx context.Context, call provider.ToolCall) loop.Result {
			if _, err := validated.Validate(call); err != nil {
				return loop.Result{ID: call.ID, Code: loop.Invalid}
			}
			in, err := spec.Schema(json.RawMessage(call.Arguments))
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Invalid}
			}
			auth := grant.Request{GrantID: turn.EffectKey(turnID, spec.Name, in.Target, call.ID) + ":web", MainID: r.main, Epoch: r.epoch, TaskID: turnID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "web"}
			// The ordinary authenticated turn permits these read-only tools. Network
			// permission is separate and is never minted here.
			r.personaMu.Lock()
			r.personaGrants[auth.GrantID] = grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: spec.Name, Target: in.Target}}}
			r.personaMu.Unlock()
			defer func() { r.personaMu.Lock(); delete(r.personaGrants, auth.GrantID); r.personaMu.Unlock() }()
			key := "web:" + turnID + ":" + call.ID
			perCall := tools.NewRegistry(r.personaAuthority, r.activities)
			if _, err := coreexec.RegisterWeb(perCall, webActivity{r: r, p: p, key: key, name: spec.Name}); err != nil {
				return loop.Result{ID: call.ID, Code: loop.Failed}
			}
			tool := turn.Tool(perCall, spec, tools.Call{Auth: auth}, turnID)
			// Subscribe before dispatch to cover an answer arriving immediately after
			// the approval is published. Waiting holds no registry deadline or lease.
			_, sub, err := r.topics.Subscribe("waiting", nil)
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Failed}
			}
			defer sub.Close()
			result := tool.Execute(ctx, call)
			for result.Code == loop.AwaitingPerson {
				if _, err := sub.Next(ctx); err != nil {
					return loop.Result{ID: call.ID, Code: loop.Aborted}
				}
				result = tool.Execute(ctx, call)
			}
			return result
		}}
	}
	return coreexec.WebDeclarations(), nil
}

// Provider entry occurs only after the registry has admitted both permissions.
type webActivity struct {
	r         *conversationRuntime
	p         coreexec.WebProvider
	key, name string
}

func (w webActivity) publish(ctx context.Context, state string) {
	text := state + ": " + w.name
	raw, _ := json.Marshal(protocol.RunningUpdate{Text: &text})
	_ = w.r.topics.Publish("running", clientsrv.TopicEvent{Kind: protocol.KindRunningUpdate, Key: w.key, Data: raw})
}
func (w webActivity) start(ctx context.Context) func(error) {
	w.publish(ctx, "running")
	return func(err error) {
		state := "completed"
		if err != nil {
			state = "failed"
		}
		if ctx.Err() != nil {
			state = "cancelled"
		}
		w.publish(context.WithoutCancel(ctx), state)
	}
}
func (w webActivity) Search(ctx context.Context, q string, n int) (hits []coreexec.WebHit, err error) {
	finish := w.start(ctx)
	defer func() { finish(err) }()
	return w.p.Search(ctx, q, n)
}
func (w webActivity) Extract(ctx context.Context, url string) (text string, err error) {
	finish := w.start(ctx)
	defer func() { finish(err) }()
	return w.p.Extract(ctx, url)
}
