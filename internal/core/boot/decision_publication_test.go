// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestBootPublishesToolApprovalAfterCommitToLiveClient(t *testing.T) {
	r := controlRuntime(t)
	a, err := r.agents.Get(t.Context(), r.agent)
	if err != nil {
		t.Fatal(err)
	}
	auth := grant.Request{GrantID: "outward-test", MainID: r.main, Epoch: r.epoch, TaskID: "test", Host: r.main, OSUser: "core", Person: r.person, Agent: r.agent, Revision: strconv.FormatInt(a.Revision, 10)}
	r.personaMu.Lock()
	r.personaGrants[auth.GrantID] = grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "fixture_send", Target: "document", Write: true}}, Revisions: map[string]string{"document": auth.Revision}}
	r.personaMu.Unlock()
	registry := tools.NewRegistry(r.personaAuthority, r.activities)
	runs := 0
	spec := tools.Spec{Name: "fixture_send", Placement: tools.Core, Conc: 1, Deadline: time.Second, Outward: true, Schema: func(args json.RawMessage) (tools.Input, error) {
		return tools.Input{Arguments: args, Target: "document", Write: true, What: "Send fixture", Where: "recipient@example.invalid", ScopeKey: "send"}, nil
	}}
	if err = registry.Register(spec, func(context.Context, lease.Lease, tools.Input) (tools.Result, error) {
		runs++
		return tools.Result{State: tools.Applied, Body: json.RawMessage(`{"sent":true}`), Revision: "2"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	call := tools.Call{Key: "without-client", Name: spec.Name, Auth: auth, Args: json.RawMessage(`{"body":"fixture"}`)}
	if _, err = registry.Dispatch(t.Context(), call); !errors.Is(err, authority.ErrNoSurface) {
		t.Fatalf("without client=%v", err)
	}
	if r.identity.DecisionPresence == nil {
		t.Fatal("missing eligible-client presentation callback")
	}
	session := clientsrv.Session{ConnectionID: "client", PersonID: r.person, Epoch: r.epoch, CanDecide: true, Capabilities: []protocol.Capability{clientsrv.DecisionsCapability}}
	r.identity.DecisionPresence(session, true)
	defer r.identity.DecisionPresence(session, false)
	_, subscription, err := r.topics.Subscribe("waiting", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	call.Key = "with-client"
	if _, err = registry.Dispatch(t.Context(), call); !errors.Is(err, tools.ErrApprovalRequired) {
		t.Fatalf("live client=%v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	frame, err := subscription.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Event.Kind != protocol.KindApprovalRequest {
		t.Fatalf("publication=%+v", frame)
	}
	var request protocol.ApprovalRequest
	if err = json.Unmarshal(frame.Event.Data, &request); err != nil {
		t.Fatal(err)
	}
	if request.Card == nil || request.Card.ID == "" || runs != 0 {
		t.Fatal("approval not published before effect")
	}
	result, err := r.identity.Operations[protocol.KindApprovalAnswer].Handle(ctx, session, protocol.Envelope{Payload: protocol.ApprovalAnswer{CardID: &request.Card.ID, Revision: &request.Card.Revision, Choice: protocol.ApprovalChoiceOnce}})
	if err != nil || *result.Payload.(protocol.ApprovalAnswer).Outcome != protocol.DecisionOutcomeResolved {
		t.Fatalf("answer=%+v %v", result, err)
	}
	if _, err = registry.Dispatch(ctx, call); err != nil || runs != 1 {
		t.Fatalf("approved dispatch runs=%d err=%v", runs, err)
	}
}
