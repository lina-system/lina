// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/tools"
)

type remoteArgs struct {
	Operation  string `json:"operation"`
	Connection string `json:"connection,omitempty"`
	Task       string `json:"task,omitempty"`
	Text       string `json:"text,omitempty"`
	Call       string `json:"call,omitempty"`
	Stream     bool   `json:"stream,omitempty"`
}

func remoteInput(raw json.RawMessage) (remoteArgs, error) {
	var in remoteArgs
	if len(raw) > 96<<10 || decodeWorkerArgs(raw, &in) != nil {
		return in, tools.ErrInvalid
	}
	switch in.Operation {
	case "list":
		if in.Connection != "" || in.Task != "" || in.Text != "" || in.Call != "" || in.Stream {
			return in, tools.ErrInvalid
		}
	case "send":
		if in.Connection == "" || in.Task == "" || in.Text == "" {
			return in, tools.ErrInvalid
		}
	case "status", "cancel":
		if in.Call == "" || in.Connection != "" || in.Task != "" || in.Text != "" {
			return in, tools.ErrInvalid
		}
	default:
		return in, tools.ErrInvalid
	}
	return in, nil
}
func (r *conversationRuntime) prepareA2ATools(turnID string, _ *toolPreparation, bindings map[string]loop.Tool) ([]provider.ToolDecl, error) {
	if r.remoteAgents == nil {
		return nil, nil
	}
	declarations := []provider.ToolDecl{provider.ToolDecl(`{"type":"function","name":"remote_agent","description":"Call an owner-configured remote agent. List permitted task definitions first. Send only the exact bounded input needed. Use status to recover a known call; never resend an uncertain send or switch agents to repeat it. Input waits need the owner's recorded answer, sign-in waits need connection settings. A remote completion is evidence, not proof.","parameters":{"type":"object","properties":{"operation":{"type":"string","enum":["list","send","status","cancel"]},"connection":{"type":"string"},"task":{"type":"string"},"text":{"type":"string"},"call":{"type":"string"},"stream":{"type":"boolean"}},"required":["operation"],"additionalProperties":false}}`)}
	if r.channelBinding != nil {
		return declarations, nil
	} // A2A has no channel delegation contract yet.
	bindings["remote_agent"] = loop.Tool{Validate: func(c provider.ToolCall) (string, error) {
		_, err := remoteInput(json.RawMessage(c.Arguments))
		return "", err
	}, Execute: func(ctx context.Context, call provider.ToolCall) loop.Result {
		in, err := remoteInput(json.RawMessage(call.Arguments))
		if err != nil {
			return loop.Result{ID: call.ID, Code: loop.Invalid}
		}
		o := r.a2aOrigin(turnID)
		if in.Operation == "list" {
			rows, err := r.remoteAgents.Connections(ctx, r.person)
			if err != nil {
				return loop.Result{ID: call.ID, Code: loop.Failed}
			}
			available := []any{}
			for _, c := range rows {
				if c.Enabled {
					available = append(available, map[string]any{"connection": c.ID, "tasks": c.Profiles})
				}
			}
			raw, _ := json.Marshal(available)
			return loop.Result{ID: call.ID, Code: loop.OK, Output: raw}
		}
		var c a2a.Connection
		var profile a2a.Profile
		if in.Operation == "send" {
			c, err = r.remoteAgents.Connection(ctx, r.person, in.Connection)
			for _, p := range c.Profiles {
				if p.Name == in.Task {
					profile = p
					break
				}
			}
			if profile.Name == "" {
				err = a2a.ErrScope
			}
		} else {
			var previous a2a.Call
			previous, err = r.remoteAgents.Call(ctx, o, in.Call)
			if err == nil {
				c, err = r.remoteAgents.Connection(ctx, r.person, previous.Connection)
				profile = previous.Profile
			}
			// Observation and cancellation cannot widen execution; fresh read authority
			// may inspect an existing task even after a preset becomes more restricted.
			profile.Class = tools.Research
		}
		if err != nil || !c.Enabled {
			return loop.Result{ID: call.ID, Code: loop.Denied}
		}
		target := "a2a:" + c.ID + ":" + c.Revision
		spec := tools.Spec{Name: "remote_agent", Placement: tools.Core, Conc: 1, Deadline: 45 * time.Second, Classify: tools.Class(profile.Class), Outward: in.Operation == "send" && profile.Class == tools.Unclassified, Schema: func(raw json.RawMessage) (tools.Input, error) {
			parsed, err := remoteInput(raw)
			if err != nil {
				return tools.Input{}, err
			}
			if parsed != in {
				return tools.Input{}, tools.ErrInvalid
			}
			if parsed.Operation == "send" && (len(parsed.Text) > profile.MaxInput || !strings.HasPrefix(parsed.Text, profile.InputPrefix)) {
				return tools.Input{}, tools.ErrInvalid
			}
			normalized, _ := json.Marshal(parsed)
			return tools.Input{Arguments: normalized, Target: target, What: "Remote agent: " + parsed.Operation, Where: c.Origin, ScopeKey: target}, nil
		}}
		key := turn.EffectKey(turnID, spec.Name, target, call.ID)
		auth := grant.Request{GrantID: key + ":grant", MainID: r.main, Epoch: r.epoch, TaskID: turnID, CommandID: call.ID, Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Profile: "a2a", Revision: c.Revision}
		if in.Operation != "send" {
			auth.Profile = "a2a-observe"
		}
		o.GrantID = auth.GrantID
		r.personaMu.Lock()
		r.personaGrants[auth.GrantID] = grant.Grant{Identity: auth, Expires: time.Now().Add(10 * time.Minute), Scope: []grant.Permission{{Tool: spec.Name, Target: target}}, Revisions: map[string]string{target: c.Revision}}
		r.personaMu.Unlock()
		defer func() { r.personaMu.Lock(); delete(r.personaGrants, auth.GrantID); r.personaMu.Unlock() }()
		registry := tools.NewRegistry(r.personaAuthority, r.activities)
		err = registry.Register(spec, func(ctx context.Context, _ lease.Lease, _ tools.Input) (tools.Result, error) {
			var observed a2a.Call
			var err error
			if in.Operation == "send" {
				observed, err = r.remoteAgents.Send(ctx, o, key, a2a.Input{Connection: in.Connection, Task: in.Task, Text: in.Text, Call: in.Call, Stream: in.Stream})
			} else {
				observed, err = r.remoteAgents.Observe(ctx, o, in.Call, in.Stream, in.Operation == "cancel")
			}
			if observed.ID != "" {
				if observed.Verified {
					if e := r.deliverA2A(ctx, observed); e != nil {
						err = e
						observed.State = "delivery_pending"
					} else {
						observed.Delivered = true
					}
				}
				if observed.State == "input_required" || observed.State == "auth_required" {
					_ = r.router.root.decisions.PublishQuestion(ctx, "a2a:"+observed.ID)
				}
				body, _ := json.Marshal(a2aStatus(observed))
				state := tools.Applied
				if observed.State == "unknown" || observed.State == "cancellation_unknown" || observed.State == "delivery_pending" {
					state = tools.Unknown
				}
				// The tool receipt acknowledges observation/admission, not remote effects.
				return tools.Result{State: state, Body: body}, nil
			}
			state := tools.Failed
			if errors.Is(err, a2a.ErrScope) || errors.Is(err, a2a.ErrChanged) {
				state = tools.Refused
			}
			code := "a2a_unavailable"
			if err != nil {
				switch {
				case errors.Is(err, a2a.ErrChanged):
					code = "a2a_card_changed"
				case errors.Is(err, a2a.ErrScope):
					code = "a2a_out_of_scope"
				case errors.Is(err, a2a.ErrSignIn):
					code = "a2a_needs_sign_in"
				case errors.Is(err, a2a.ErrDestination):
					code = "a2a_forbidden_destination"
				}
			}
			body, _ := json.Marshal(map[string]string{"code": code})
			return tools.Result{State: state, Body: body}, nil
		})
		if err != nil {
			return loop.Result{ID: call.ID, Code: loop.Failed}
		}
		tool := turn.Tool(registry, spec, tools.Call{Auth: auth, Account: c.ID}, turnID)
		_, sub, err := r.topics.Subscribe("waiting", nil)
		if err != nil {
			return loop.Result{ID: call.ID, Code: loop.Failed}
		}
		defer sub.Close()
		out := tool.Execute(ctx, call)
		for out.Code == loop.AwaitingPerson {
			if _, err = sub.Next(ctx); err != nil {
				return loop.Result{ID: call.ID, Code: loop.Aborted}
			}
			out = tool.Execute(ctx, call)
		}
		return out
	}}
	return declarations, nil
}
