// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"bufio"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestRecoveryAdmissionAndEpoch(t *testing.T) {
	client, server := net.Pipe()
	var recovering atomic.Bool
	recovering.Store(true)
	var writes, reads atomic.Int32
	capability := protocol.Capability{Name: "conversation", Version: "1.0"}
	row := LocalCombinations()[0]
	row.Capabilities = []protocol.Capability{capability}
	identity := Identity{MainID: "main-test", Epoch: "18446744073709551615", Recovering: recovering.Load, Supported: []Combination{row},
		Operations: map[protocol.Kind]Operation{
			protocol.KindInput: {Capability: capability, Handle: func(context.Context, Session, protocol.Envelope) (Result, error) {
				writes.Add(1)
				return Result{Kind: protocol.KindTurnState, Payload: protocol.TurnState{}}, nil
			}},
			protocol.KindHistoryGet: {Capability: capability, Handle: func(_ context.Context, s Session, _ protocol.Envelope) (Result, error) {
				if s.Epoch != ^uint64(0) || s.ClientID != "client-example" {
					return Result{}, context.Canceled
				}
				reads.Add(1)
				return Result{Kind: protocol.KindHistory, Payload: protocol.History{}}, nil
			}},
		}}
	done := make(chan struct{})
	go func() { defer close(done); defer server.Close(); serveConn(server, identity) }()
	t.Cleanup(func() { client.Close(); server.Close(); <-done })
	client.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(client)
	req := testHello(t)
	hello := req.Payload.(protocol.Hello)
	hello.Capabilities = []protocol.Capability{capability}
	req.Payload = hello
	welcome := exchange(t, client, reader, req)
	if welcome.Kind != protocol.KindWelcome || len(welcome.Payload.(protocol.Welcome).Capabilities) != 1 {
		t.Fatal(welcome)
	}
	req.Epoch = welcome.Epoch
	req.Kind, req.Payload = protocol.KindInput, protocol.Input{}
	answer := exchange(t, client, reader, req)
	if answer.Error == nil || answer.Error.Code != protocol.ErrorUnavailable || writes.Load() != 0 {
		t.Fatalf("recovery mutation: %+v, writes=%d", answer, writes.Load())
	}
	req.Kind, req.Payload = protocol.KindHistoryGet, protocol.HistoryGet{}
	if answer := exchange(t, client, reader, req); answer.Kind != protocol.KindHistory || reads.Load() != 1 {
		t.Fatalf("recovery read: %+v", answer)
	}
	recovering.Store(false)
	req.Kind, req.Payload = protocol.KindInput, protocol.Input{}
	if answer := exchange(t, client, reader, req); answer.Kind != protocol.KindTurnState || writes.Load() != 1 {
		t.Fatalf("ready mutation: %+v", answer)
	}
	req.Epoch = "18446744073709551614"
	if answer := exchange(t, client, reader, req); answer.Error == nil || answer.Error.Code != protocol.ErrorStaleEpoch || writes.Load() != 1 {
		t.Fatalf("stale mutation: %+v", answer)
	}
}
