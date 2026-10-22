// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

func TestAnswersUseCorrelatedValidatedReceive(t *testing.T) {
	for _, kind := range []protocol.Kind{protocol.KindApprovalAnswer, protocol.KindQuestionAnswer} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, server := net.Pipe()
			defer conn.Close()
			defer server.Close()
			c := &Client{conn: conn, Welcome: protocol.Welcome{MainID: "main", Epoch: "1", ProtocolVersion: "1.0"}, clientID: "client", reader: rpc.NewReader(conn), writer: rpc.NewWriter(conn), pending: map[string]struct{}{}}
			done := make(chan error, 1)
			go func() {
				line, err := rpc.NewReader(server).Read()
				if err != nil {
					done <- err
					return
				}
				var req struct {
					JSONRPC, ID, Method string
					Params              protocol.Envelope
				}
				if err = json.Unmarshal(line, &req); err != nil {
					done <- err
					return
				}
				e := req.Params
				e.Sender = protocol.Sender{Party: protocol.PartyCore, ID: "main"}
				e.ReplyTo = &req.ID
				outcome := protocol.DecisionOutcomeResolved
				switch p := e.Payload.(type) {
				case protocol.ApprovalAnswer:
					p.Outcome = &outcome
					e.Payload = p
				case protocol.QuestionAnswer:
					p.Outcome = &outcome
					e.Payload = p
				}
				done <- rpc.NewWriter(server).Write(ctx, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": e})
			}()
			id, rev := "approval", "revision"
			var request string
			var err error
			if kind == protocol.KindApprovalAnswer {
				request, err = c.AnswerApproval(ctx, protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce, CardID: &id, Revision: &rev})
			} else {
				request, err = c.AnswerQuestion(ctx, protocol.QuestionAnswer{Answer: protocol.QuestionAnswerActionDecline, CardID: &id, Revision: &rev})
			}
			if err != nil {
				t.Fatal(err)
			}
			reply, err := c.Receive(ctx)
			if err != nil || reply.Kind != kind || reply.ReplyTo == nil || *reply.ReplyTo != request {
				t.Fatalf("reply=%+v err=%v", reply, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
