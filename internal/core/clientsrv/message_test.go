// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestMessageAdmissionOrder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sender     protocol.Sender
		epoch      string
		recovering bool
		want       protocol.ErrorCode
		checks     []string
	}{
		{"sender party", protocol.Sender{Party: protocol.PartyCore, ID: "client"}, "1", true, protocol.ErrorNotAuthorized, nil},
		{"sender id", protocol.Sender{Party: protocol.PartyClient, ID: "other"}, "1", true, protocol.ErrorNotAuthorized, nil},
		{"epoch", protocol.Sender{Party: protocol.PartyClient, ID: "client"}, "1", true, protocol.ErrorStaleEpoch, nil},
		{"recovery", protocol.Sender{Party: protocol.PartyClient, ID: "client"}, "2", true, protocol.ErrorUnavailable, []string{"recovery"}},
		{"decider", protocol.Sender{Party: protocol.PartyClient, ID: "client"}, "2", false, protocol.ErrorNotAuthorized, []string{"recovery", "decider"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var checks []string
			c := clientConnection{
				identity: Identity{Epoch: "2", Recovering: func() bool {
					checks = append(checks, "recovery")
					return tc.recovering
				}},
				peer: peerIdentity{remoteDecider: func() bool {
					checks = append(checks, "decider")
					return false
				}},
				session: Session{ClientID: "client"},
			}
			envelope := protocol.Envelope{Kind: protocol.KindApprovalAnswer, Sender: tc.sender, Epoch: tc.epoch}
			answer := reply(envelope, c.identity)
			if c.admit(envelope, &answer) || answer.Error == nil || answer.Error.Code != tc.want || answer.EffectState != protocol.EffectRefused {
				t.Fatalf("admission = %+v, want refusal %s", answer, tc.want)
			}
			if !reflect.DeepEqual(checks, tc.checks) {
				t.Fatalf("checks = %v, want %v", checks, tc.checks)
			}
		})
	}
}

func TestMessageAdmissionRechecksDecider(t *testing.T) {
	for _, kind := range []protocol.Kind{protocol.KindApprovalAnswer, protocol.KindQuestionAnswer, protocol.KindWaitingAnswer, protocol.KindInput, protocol.KindHistoryGet} {
		t.Run(string(kind), func(t *testing.T) {
			eligible, checks := true, 0
			c := clientConnection{
				identity: Identity{Epoch: "2"},
				peer: peerIdentity{remoteDecider: func() bool {
					checks++
					return eligible
				}},
				session: Session{ClientID: "client"},
			}
			envelope := protocol.Envelope{Kind: kind, Sender: protocol.Sender{Party: protocol.PartyClient, ID: "client"}, Epoch: "2"}
			answer := reply(envelope, c.identity)
			if !c.admit(envelope, &answer) {
				t.Fatalf("eligible client refused: %+v", answer)
			}
			eligible = false
			answer = reply(envelope, c.identity)
			admitted := c.admit(envelope, &answer)
			if kind == protocol.KindInput || kind == protocol.KindHistoryGet {
				if !admitted || checks != 0 {
					t.Fatalf("ordinary request admission=%v, decider checks=%d", admitted, checks)
				}
			} else if admitted || checks != 2 || answer.Error == nil || answer.Error.Code != protocol.ErrorNotAuthorized {
				t.Fatalf("revoked decider admission=%v, checks=%d, answer=%+v", admitted, checks, answer)
			}
		})
	}
}

func TestMessageDispatchErrorsAndSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want protocol.ErrorCode
	}{
		{"result", nil, ""},
		{"typed refusal", fmt.Errorf("handler context: %w", &OperationError{Code: protocol.ErrorOutOfScope}), protocol.ErrorOutOfScope},
		{"opaque failure", errors.New("private handler detail"), protocol.ErrorUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capability := protocol.Capability{Name: "conversation", Version: "1.0"}
			c := clientConnection{
				identity: Identity{MainID: "main", Epoch: "2"},
				peer:     peerIdentity{remoteDecider: func() bool { return true }},
				session:  Session{ConnectionID: "connection", PersonID: "owner", Epoch: 2, ClientID: "client", Capabilities: []protocol.Capability{capability}},
			}
			request := protocol.Envelope{Kind: protocol.KindInput, MessageID: "message", Payload: protocol.Input{}}
			calls := 0
			c.identity.Operations = map[protocol.Kind]Operation{
				protocol.KindInput: {Capability: capability, Handle: func(ctx context.Context, session Session, envelope protocol.Envelope) (Result, error) {
					calls++
					want := c.session
					want.CanDecide = true
					if ctx != t.Context() || !reflect.DeepEqual(session, want) || !reflect.DeepEqual(envelope, request) {
						t.Fatalf("handler received wrong context, session or envelope: %+v, %+v", session, envelope)
					}
					return Result{Kind: protocol.KindTurnState, Payload: protocol.TurnState{}, Effect: protocol.EffectCancelled}, tc.err
				}},
			}
			topics := newTopicSession(t.Context(), c.identity, nil, nil, nil, nil)
			defer topics.Close()
			answer := reply(request, c.identity)
			before := answer
			if handled, err := c.dispatch(t.Context(), topics, nil, request, &answer); handled || err != nil || calls != 1 {
				t.Fatalf("dispatch handled=%v, err=%v, calls=%d", handled, err, calls)
			}
			if answer.MessageID != before.MessageID || answer.AttemptID != before.AttemptID || answer.ReplyTo != before.ReplyTo || answer.Sender != before.Sender || answer.Epoch != before.Epoch {
				t.Fatalf("handler changed response identity: %+v", answer)
			}
			if tc.want != "" {
				if answer.Error == nil || answer.Error.Code != tc.want || answer.Error.Detail != string(tc.want) || answer.EffectState != protocol.EffectRefused {
					t.Fatalf("handler error = %+v", answer)
				}
			} else if answer.Kind != protocol.KindTurnState || answer.EffectState != protocol.EffectCancelled || answer.Error != nil {
				t.Fatalf("handler result = %+v", answer)
			}
		})
	}
}
