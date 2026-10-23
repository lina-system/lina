// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestPlanningInNewConversationView(t *testing.T) {
	r, _, _, agent := managementFixture(t)
	var err error
	r.records, err = records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.records.Close() })
	r.materials = &conversationMaterials{}
	if err = r.initPlanning(); err != nil {
		t.Fatal(err)
	}
	view := newConversationView(r, agent, "main:"+agent)
	scope := view.planScope("new-agent-plan")
	root, err := view.plans.Accept(t.Context(), scope, "goal", "Deliver workshop", "Workshop delivered")
	if err != nil {
		t.Fatal(err)
	}
	input, err := view.plans.Input(t.Context(), scope, root.ID, "", 0, "Prepare a plan")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(jobapi.PlanDraft{Title: "Workshop plan", Summary: "Prepare workshop", Entries: []jobapi.PlanEntry{{ID: "project", Kind: "project", Title: "Workshop", Done: "Ready"}}})
	if err != nil {
		t.Fatal(err)
	}
	var card result.Card
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		var err error
		card, err = view.plans.DraftTx(t.Context(), tx, scope, input, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	preview, err := view.planResults.Read(t.Context(), scope, result.ReadRequest{ID: card.ID})
	if err != nil || preview.ID != card.ID || preview.Revision != card.Revision {
		t.Fatalf("new conversation cannot read its draft: %+v %v", preview, err)
	}
	if head, err := r.records.Head(t.Context()); err != nil || head != root.Revision {
		t.Fatalf("new conversation published unconfirmed plan records: %q %v", head, err)
	}
}

func TestPlanningRequiresPersonDecisionAuthority(t *testing.T) {
	r, session, agent, _ := managementFixture(t)
	r.agent = agent
	r.conversation = "main:" + agent
	r.router = &conversationRouter{root: r, views: map[string]*conversationRuntime{agent: r}}
	for _, operation := range []string{"accept_goal", "accept_project", "confirm"} {
		_, err := r.planOperation(t.Context(), session, protocol.Envelope{Payload: protocol.PlanRequest{AgentID: agent, Operation: operation}})
		var refusal *clientsrv.OperationError
		if !errors.As(err, &refusal) || refusal.Code != protocol.ErrorNotAuthorized {
			t.Fatal(operation, err)
		}
	}
	session.PersonID = "other"
	session.CanDecide = true
	_, err := r.planOperation(t.Context(), session, protocol.Envelope{Payload: protocol.PlanRequest{AgentID: agent, Operation: "accept_goal"}})
	var refusal *clientsrv.OperationError
	if !errors.As(err, &refusal) || refusal.Code != protocol.ErrorNotAuthorized {
		t.Fatal("foreign owner", err)
	}
}
