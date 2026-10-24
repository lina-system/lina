// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

type workControlSpy struct {
	calls   int
	last    protocol.WorkControlRequest
	result  protocol.WorkControlResult
	applied bool
}

func (s *workControlSpy) WorkControl(_ context.Context, _ Session, p protocol.WorkControlRequest, _ string) (protocol.WorkControlResult, bool, error) {
	s.calls++
	s.last = p
	return s.result, s.applied, nil
}

func TestWorkControlRequiresAnEligibleDecider(t *testing.T) {
	spy := &workControlSpy{applied: true, result: protocol.WorkControlResult{GoalID: "g", Operation: "pause", State: "running", Paused: true, Rank: "1"}}
	op := WorkControlOperations(spy)[protocol.KindWorkControlRequest]
	if op.Capability != WorkCapability {
		t.Fatalf("capability = %+v, want the work capability", op.Capability)
	}
	confirm := true
	e := protocol.Envelope{Kind: protocol.KindWorkControlRequest, Payload: protocol.WorkControlRequest{Operation: "stop", GoalID: "g", Confirm: &confirm}}
	if _, err := op.Handle(t.Context(), Session{PersonID: "person"}, e); err == nil || spy.calls != 0 {
		t.Fatal("a non-decider connection committed a control")
	}
	result, err := op.Handle(t.Context(), Session{PersonID: "person", CanDecide: true}, e)
	if err != nil || spy.calls != 1 {
		t.Fatalf("eligible control refused: %v", err)
	}
	if result.Kind != protocol.KindWorkControlResult || result.Effect != protocol.EffectApplied {
		t.Fatalf("result = %+v", result)
	}
	if spy.last.GoalID != "g" || spy.last.Operation != "stop" {
		t.Fatalf("control payload = %+v", spy.last)
	}
	e.Payload = protocol.WorkSwitchRequest{Operation: "switch_status", SwitchID: "s"}
	if _, err := op.Handle(t.Context(), Session{PersonID: "person", CanDecide: true}, e); err == nil || spy.calls != 1 {
		t.Fatal("a foreign payload reached the control service")
	}
	// A choice that recorded nothing is not answered as an applied effect.
	spy.applied = false
	e.Payload = protocol.WorkControlRequest{Operation: "pause", GoalID: "g"}
	if result, err := op.Handle(t.Context(), Session{PersonID: "person", CanDecide: true}, e); err != nil || result.Effect != protocol.EffectNone {
		t.Fatalf("no-op effect = %+v %v", result, err)
	}
}
