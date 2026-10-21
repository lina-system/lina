// SPDX-License-Identifier: Apache-2.0

package clientapi_test

import (
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

func TestManagementRequestsUseReceiveCorrelation(t *testing.T) {
	socket, _ := streamServer(t)
	client, err := clientapi.DialWithCapabilities(t.Context(), socket, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	op, agent := "list", "agent"
	for _, send := range []func() (string, error){func() (string, error) { return client.Agent(t.Context(), protocol.AgentRequest{Operation: &op}) }, func() (string, error) {
		return client.Memory(t.Context(), protocol.MemoryRequest{Operation: &op, AgentID: &agent})
	}} {
		id, err := send()
		if err != nil {
			t.Fatal(err)
		}
		reply, err := client.Receive(t.Context())
		if err != nil || reply.ReplyTo == nil || *reply.ReplyTo != id {
			t.Fatal("uncorrelated management reply", err)
		}
	}
}
