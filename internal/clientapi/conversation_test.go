// SPDX-License-Identifier: Apache-2.0

package clientapi_test

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestConversationRequestsUseReceiveCorrelation(t *testing.T) {
	socket, _ := streamServer(t)
	client, err := clientapi.DialWithCapabilities(t.Context(), socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	api, ok := any(client).(interface {
		Input(context.Context, protocol.Input) (string, error)
		Interrupt(context.Context, protocol.Interrupt) (string, error)
		History(context.Context, protocol.HistoryGet) (string, error)
	})
	if !ok {
		t.Fatal("client has no typed conversation requests")
	}
	conversation, text, id := "conversation", "message", "input"
	for _, send := range []func() (string, error){
		func() (string, error) {
			return api.Input(t.Context(), protocol.Input{ConversationID: &conversation, Text: &text, InputID: &id})
		},
		func() (string, error) {
			return api.Interrupt(t.Context(), protocol.Interrupt{ConversationID: &conversation})
		},
		func() (string, error) {
			return api.History(t.Context(), protocol.HistoryGet{ConversationID: &conversation})
		},
	} {
		id, err := send()
		if err != nil {
			t.Fatal(err)
		}
		reply, err := client.Receive(t.Context())
		if err != nil || reply.ReplyTo == nil || *reply.ReplyTo != id {
			t.Fatalf("uncorrelated response: %+v %v", reply, err)
		}
	}
}
