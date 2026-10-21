// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

type channelStub struct {
	calls   int
	session Session
	request protocol.ChannelRequest
}

func (s *channelStub) Channel(_ context.Context, session Session, p protocol.ChannelRequest) (protocol.ChannelResult, error) {
	s.calls++
	s.session = session
	s.request = p
	rows := []protocol.ChannelConnection{}
	return protocol.ChannelResult{Operation: p.Operation, Connections: &rows}, nil
}
func TestChannelControlRejectsMixedFormsAndIneligibleMutations(t *testing.T) {
	service := &channelStub{}
	op := ChannelOperations(service)[protocol.KindChannelRequest]
	owner := Session{PersonID: "owner", Epoch: 42, CanDecide: true}
	for _, p := range []protocol.ChannelRequest{
		{}, {Operation: stringPointer("list"), Token: stringPointer("synthetic")},
		{Operation: stringPointer("connect"), Token: stringPointer("synthetic"), Account: stringPointer("bot")},
		{Operation: stringPointer("pair")},
		{Operation: stringPointer("bind"), Account: stringPointer("bot"), AgentID: stringPointer("agent")},
		{Operation: stringPointer("unbind"), Account: stringPointer("bot"), Chat: stringPointer("chat"), AgentID: stringPointer("agent")},
	} {
		_, err := op.Handle(t.Context(), owner, protocol.Envelope{Payload: p})
		var refusal *OperationError
		if !errors.As(err, &refusal) || refusal.Code != protocol.ErrorMalformed || service.calls != 0 {
			t.Fatal("mixed or incomplete form dispatched", err)
		}
	}
	p := protocol.ChannelRequest{Operation: stringPointer("pair"), Account: stringPointer("bot")}
	ineligible := owner
	ineligible.CanDecide = false
	if _, err := op.Handle(t.Context(), ineligible, protocol.Envelope{Payload: p}); err == nil || service.calls != 0 {
		t.Fatal("descendant mutated channel controls")
	}
	result, err := op.Handle(t.Context(), owner, protocol.Envelope{Payload: p})
	if err != nil || service.calls != 1 || service.session.PersonID != "owner" || service.session.Epoch != 42 || result.Effect != protocol.EffectApplied {
		t.Fatal("trusted identity or mutation result lost", err)
	}
	result, err = op.Handle(t.Context(), ineligible, protocol.Envelope{Payload: protocol.ChannelRequest{Operation: stringPointer("list")}})
	if err != nil || result.Effect != protocol.EffectNone {
		t.Fatal("read requires decider eligibility", err)
	}
}

type talkStub struct {
	calls   int
	session Session
	id      string
}

func (s *talkStub) Talk(_ context.Context, session Session, p protocol.AgentTalkGet) (protocol.AgentTalk, error) {
	s.calls++
	s.session = session
	return protocol.AgentTalk{}, nil
}
func (s *talkStub) ExpandTalk(_ context.Context, session Session, p protocol.AgentTalkExpand) (protocol.AgentTalk, error) {
	s.calls++
	s.session = session
	s.id = *p.ExchangeID
	return protocol.AgentTalk{ExchangeID: p.ExchangeID}, nil
}
func TestTalkControlPassesTrustedSessionAndExactExpansion(t *testing.T) {
	service := &talkStub{}
	ops := TalkOperations(service)
	session := Session{PersonID: "owner", Epoch: 42}
	_, err := ops[protocol.KindAgentTalkExpand].Handle(t.Context(), session, protocol.Envelope{Payload: protocol.AgentTalkExpand{}})
	if err == nil || service.calls != 0 {
		t.Fatal("empty expansion dispatched")
	}
	result, err := ops[protocol.KindAgentTalkExpand].Handle(t.Context(), session, protocol.Envelope{Payload: protocol.AgentTalkExpand{ExchangeID: stringPointer("exchange-one")}})
	if err != nil || service.id != "exchange-one" || service.session.PersonID != "owner" || result.Kind != protocol.KindAgentTalk {
		t.Fatal("expansion identity lost", err)
	}
	if !readOnlyKinds[protocol.KindAgentTalkGet] || !readOnlyKinds[protocol.KindAgentTalkExpand] {
		t.Fatal("log reads disabled during recovery")
	}
}
