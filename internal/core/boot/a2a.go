// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/tools"
)

func (r *conversationRuntime) initA2A(relay *clientsrv.SignInRelay) error {
	r.remoteAgents = &a2a.Service{DB: r.db, Vault: r.vault, Epoch: r.epoch, Check: func(ctx context.Context, o a2a.Origin, c a2a.Connection, p a2a.Profile) error {
		g, env, err := r.resolvePersonaGrant(ctx, o.GrantID)
		if err != nil {
			return err
		}
		if g.Identity.Person != o.Person || g.Identity.Agent != o.Agent || g.Identity.TaskID != o.Turn || (g.Identity.Profile != "a2a" && g.Identity.Profile != "a2a-observe") || g.Identity.Revision != c.Revision {
			return a2a.ErrScope
		}
		request := g.Identity
		request.Tool = "remote_agent"
		request.Target = g.Scope[0].Target
		request.Write = g.Scope[0].Write
		if err = grant.Check(g, request, env); err != nil {
			return err
		}
		if g.Identity.Profile == "a2a-observe" {
			p.Class = tools.Research
		}
		raw, _ := json.Marshal(map[string]string{"connection": c.ID, "revision": c.Revision})
		return authority.CheckPreset(ctx, r.db, r.epoch, authority.PolicyRequest{Key: o.GrantID + ":ceiling", Person: o.Person, Agent: o.Agent, TaskID: o.Turn, Tool: "remote_agent", Target: request.Target, Class: p.Class, Arguments: raw})
	}}
	return addOperations(&r.identity, map[protocol.Kind]clientsrv.Operation{protocol.KindA2aRequest: {Capability: protocol.Capability{Name: "a2a", Version: "1.0"}, Handle: func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
		return r.a2aOperation(ctx, s, e, relay)
	}}})
}
func (r *conversationRuntime) a2aOrigin(turn string) a2a.Origin {
	return a2a.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: turn, Main: r.main, Epoch: r.epoch}
}
func a2aError(err error) error {
	if err == nil {
		return nil
	}
	code := protocol.ErrorUnavailable
	if errors.Is(err, a2a.ErrScope) {
		code = protocol.ErrorOutOfScope
	}
	if errors.Is(err, a2a.ErrInvalid) || errors.Is(err, a2a.ErrDestination) {
		code = protocol.ErrorMalformed
	}
	if errors.Is(err, a2a.ErrChanged) {
		code = protocol.ErrorStaleRevision
	}
	return &clientsrv.OperationError{Code: code}
}
func value(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func (r *conversationRuntime) a2aOperation(ctx context.Context, s clientsrv.Session, e protocol.Envelope, relay *clientsrv.SignInRelay) (clientsrv.Result, error) {
	if err := r.authorize(s, nil); err != nil {
		return clientsrv.Result{}, err
	}
	if !s.CanDecide || s.Epoch != r.epoch {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorNotAuthorized}
	}
	p, ok := e.Payload.(protocol.A2aRequest)
	if !ok {
		return clientsrv.Result{}, malformedManagement()
	}
	out := protocol.A2aResult{Operation: p.Operation}
	effect := protocol.EffectNone
	var err error
	// Reject misplaced credential and configuration fields rather than accepting
	// an ambiguous settings request with ignored private data.
	if p.Credential != nil && p.Operation != "configure" && p.Operation != "credential" || p.Connection != nil && p.Operation != "configure" || p.Answer != nil && p.Operation != "answer" {
		return clientsrv.Result{}, malformedManagement()
	}
	switch p.Operation {
	case "configure":
		if p.Connection == nil {
			return clientsrv.Result{}, malformedManagement()
		}
		raw, _ := json.Marshal(p.Connection)
		var c a2a.Connection
		if json.Unmarshal(raw, &c) != nil {
			return clientsrv.Result{}, malformedManagement()
		}
		c.Person = s.PersonID
		credential := []byte(value(p.Credential))
		defer clear(credential)
		c, err = r.remoteAgents.Configure(ctx, c, credential)
		if err == nil {
			rows := []protocol.A2aConnectionStatus{{ID: c.ID, Enabled: c.Enabled, Revision: c.Revision}}
			out.Connections = &rows
			effect = protocol.EffectApplied
		}
	case "list":
		var rows []a2a.Connection
		rows, err = r.remoteAgents.Connections(ctx, s.PersonID)
		statuses := []protocol.A2aConnectionStatus{}
		for _, c := range rows {
			statuses = append(statuses, protocol.A2aConnectionStatus{ID: c.ID, Enabled: c.Enabled, Revision: c.Revision})
		}
		out.Connections = &statuses
	case "revoke":
		err = r.remoteAgents.Revoke(ctx, s.PersonID, value(p.ConnectionID))
		effect = protocol.EffectApplied
	case "credential":
		credential := []byte(value(p.Credential))
		defer clear(credential)
		err = r.remoteAgents.SetCredential(ctx, s.PersonID, value(p.ConnectionID), credential)
		effect = protocol.EffectApplied
	case "sign_in":
		id := value(p.ConnectionID)
		return relay.StartExternal(s, clientsrv.ExternalSignIn{Begin: func(ctx context.Context, port int) (string, error) {
			return r.remoteAgents.BeginOAuth(ctx, s.PersonID, id, s.ConnectionID, port)
		}, Complete: func(ctx context.Context, p protocol.SignInRedirect) error {
			if p.URL != nil && p.Code == nil && p.State == nil && p.Iss == nil && p.Error == nil {
				return r.remoteAgents.CompleteOAuthURL(ctx, s.ConnectionID, *p.URL)
			}
			if p.Error != nil || p.URL != nil {
				return a2a.ErrSignIn
			}
			return r.remoteAgents.CompleteOAuth(ctx, s.ConnectionID, value(p.Code), value(p.State), value(p.Iss))
		}, Disconnect: func() { r.remoteAgents.DisconnectOAuth(s.ConnectionID) }, Kind: protocol.KindA2aResult, Result: out})
	case "calls":
		var calls []a2a.Call
		calls, err = r.remoteAgents.Calls(ctx, s.PersonID)
		statuses := []protocol.A2aCallStatus{}
		for _, c := range calls {
			statuses = append(statuses, a2aStatus(c))
		}
		out.Calls = &statuses
	case "answer":
		o := r.a2aOrigin("owner-answer")
		o.Agent = value(p.AgentID)
		o.Conversation = "main:" + o.Agent
		err = r.remoteAgents.Answer(ctx, o, value(p.CallID), value(p.Answer))
		effect = protocol.EffectApplied
	default:
		return clientsrv.Result{}, malformedManagement()
	}
	return clientsrv.Result{Kind: protocol.KindA2aResult, Payload: out, Effect: effect}, a2aError(err)
}
func a2aStatus(c a2a.Call) protocol.A2aCallStatus {
	out := protocol.A2aCallStatus{ID: c.ID, Connection: c.Connection, AgentID: c.Origin.Agent, ConversationID: c.Origin.Conversation, State: c.State, Verified: c.Verified}
	if c.TaskID != "" {
		out.RemoteTask = &c.TaskID
	}
	if c.Question != "" {
		out.Question = &c.Question
	}
	if c.Verified && c.Delivered {
		id := "a2a:" + c.ID
		out.ResultID = &id
	}
	return out
}
func (r *conversationRuntime) a2aEnvironment(ctx context.Context, g grant.Grant) (grant.Environment, error) {
	parts := strings.Split(g.Scope[0].Target, ":")
	if len(parts) < 2 {
		return grant.Environment{}, tools.ErrInvalid
	}
	c, err := r.remoteAgents.Connection(ctx, g.Identity.Person, parts[1])
	if err != nil || !c.Enabled {
		return grant.Environment{}, a2a.ErrScope
	}
	return grant.Environment{Epoch: r.epoch, Now: time.Now(), OSPermitted: true, Revision: c.Revision}, nil
}
