// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"errors"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

func TestStopRunIsOwnedTypedIdempotentAndNeedsNoApproval(t *testing.T) {
	r := runRuntime(t)
	brief := runs.Brief{Goal: "inspect", Scope: "reads", NotTouch: "writes", Done: "report", Result: "summary", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}}
	id, _, err := r.background.service.Start(t.Context(), r.runOrigin("stop"), brief)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.Envelope{Kind: protocol.KindStop, Payload: protocol.Stop{RunID: &id}}
	foreign := clientsrv.Session{PersonID: "different", CanDecide: true}
	if _, err = r.stopRun(t.Context(), foreign, request); err == nil {
		t.Fatal("other person stopped run")
	}
	origin := r.runOrigin("foreign-stop")
	origin.Person = "different"
	foreignID, _, err := r.background.service.Start(t.Context(), origin, brief)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.stopRun(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{Payload: protocol.Stop{RunID: &foreignID}}); err == nil {
		t.Fatal("owner stopped a foreign run")
	}
	for range 2 {
		answer, err := r.stopRun(t.Context(), clientsrv.Session{PersonID: r.person, CanDecide: false}, request)
		result, ok := answer.Payload.(protocol.StopResult)
		if err != nil || !ok || result.RunID != id || string(result.Outcome) != "cancelled" || answer.Kind != protocol.KindStopResult || answer.Effect != protocol.EffectApplied {
			t.Fatalf("stop: %+v %v", answer, err)
		}
	}
	for _, id := range []string{"", "absent"} {
		_, err := r.stopRun(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{Payload: protocol.Stop{RunID: &id}})
		var refusal *clientsrv.OperationError
		if !errors.As(err, &refusal) {
			t.Fatalf("missing typed refusal: %v", err)
		}
	}
}
