// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestRecoveringRefusesEveryMutationBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		kind    protocol.Kind
		payload protocol.Payload
	}{
		{protocol.KindInput, protocol.Input{}},
		{protocol.KindApprovalAnswer, protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce}},
		{protocol.KindQuestionAnswer, protocol.QuestionAnswer{Answer: protocol.QuestionAnswerActionDecline}},
		{protocol.KindWaitingAnswer, protocol.WaitingAnswer{}},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				serveConn(server, Identity{MainID: "main-test", Epoch: "1", Recovering: func() bool { return true }})
			}()
			t.Cleanup(func() { client.Close(); server.Close(); <-done })
			client.SetDeadline(time.Now().Add(5 * time.Second))
			reader := bufio.NewReader(client)
			env := testHello(t)
			call := func() protocol.Envelope {
				data, err := json.Marshal(env)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.NewEncoder(client).Encode(request{JSONRPC: "2.0", ID: json.RawMessage(`"request"`), Method: string(env.Kind), Params: data}); err != nil {
					t.Fatal(err)
				}
				line, err := reader.ReadBytes('\n')
				if err != nil {
					t.Fatal(err)
				}
				var reply response
				if err := json.Unmarshal(line, &reply); err != nil {
					t.Fatal(err)
				}
				return reply.Result
			}
			if got := call(); got.Kind != protocol.KindWelcome {
				t.Fatalf("handshake=%+v", got)
			}
			env.Kind = tc.kind
			env.Payload = tc.payload
			env.MessageID = "mutation"
			env.Epoch = "1"
			got := call()
			if got.Error == nil || got.Error.Code != protocol.ErrorUnavailable {
				t.Fatalf("recovering %s=%+v", tc.kind, got.Error)
			}
		})
	}
}
