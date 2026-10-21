// SPDX-License-Identifier: Apache-2.0

package clientapi_test

import (
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

func TestChannelAndTalkRequestsKeepReceiveCorrelation(t *testing.T) {
	socket, _ := streamServer(t)
	client, err := clientapi.DialWithCapabilities(t.Context(), socket, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	operation, id := "list", "synthetic-exchange"
	for _, send := range []func() (string, error){
		func() (string, error) {
			return client.Channel(t.Context(), protocol.ChannelRequest{Operation: &operation})
		},
		func() (string, error) { return client.Talk(t.Context(), protocol.AgentTalkGet{}) },
		func() (string, error) {
			return client.ExpandTalk(t.Context(), protocol.AgentTalkExpand{ExchangeID: &id})
		},
	} {
		request, err := send()
		if err != nil {
			t.Fatal(err)
		}
		reply, err := client.Receive(t.Context())
		if err != nil || reply.ReplyTo == nil || *reply.ReplyTo != request || reply.Error == nil {
			t.Fatal("unsupported request lost correlated refusal", err)
		}
	}
}
