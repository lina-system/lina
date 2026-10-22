// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"errors"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

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
