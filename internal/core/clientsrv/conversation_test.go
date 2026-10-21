// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

type conversationStub struct{ calls int }

func (s *conversationStub) History(context.Context, Session, protocol.HistoryGet) (protocol.History, error) {
	s.calls++
	return protocol.History{}, nil
}
func (s *conversationStub) Input(context.Context, Session, protocol.Input) (protocol.ItemDone, error) {
	s.calls++
	return protocol.ItemDone{}, nil
}
func (s *conversationStub) Interrupt(context.Context, Session, protocol.Interrupt) error {
	s.calls++
	return nil
}

func TestConversationOperationsValidateBeforeDispatch(t *testing.T) {
	service := &conversationStub{}
	operations := ConversationOperations(service)
	for _, kind := range []protocol.Kind{protocol.KindInput, protocol.KindHistoryGet, protocol.KindInterrupt} {
		operation, ok := operations[kind]
		if !ok {
			t.Fatalf("missing conversation operation %s", kind)
		}
		if operation.Capability != ConversationCapability {
			t.Fatal("incorrect capability")
		}
	}
	session := Session{PersonID: "person", Epoch: 1, ClientID: "client"}
	operation := operations[protocol.KindInput]
	_, err := operation.Handle(t.Context(), session, protocol.Envelope{Payload: protocol.Input{}})
	if err == nil || service.calls != 0 {
		t.Fatal("empty legacy input dispatched")
	}
	id, text, key := "conversation", "message", "input"
	_, err = operation.Handle(t.Context(), session, protocol.Envelope{Payload: protocol.Input{ConversationID: &id, InputID: &key, Text: &text}})
	if err != nil || service.calls != 1 {
		t.Fatalf("valid input not dispatched: %v", err)
	}
}
