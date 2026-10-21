// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"slices"
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

func TestSharedKindRequiresSelectedCapability(t *testing.T) {
	agents := protocol.Capability{Name: "agents", Version: "1.0"}
	growth := protocol.Capability{Name: "growth", Version: "1.0"}
	for _, offered := range [][]protocol.Capability{{agents}, {growth}, {agents, growth}} {
		t.Run(fmt.Sprint(offered), func(t *testing.T) {
			client, server := net.Pipe()
			var calls atomic.Int32
			row := LocalCombinations()[0]
			row.Capabilities = []protocol.Capability{agents, growth}
			identity := Identity{MainID: "main-test", Epoch: "1", Supported: []Combination{row}, Operations: map[protocol.Kind]Operation{
				protocol.KindAgentRequest: {
					Capability: agents, AdditionalCapabilities: []protocol.Capability{growth},
					CapabilityFor: func(e protocol.Envelope) protocol.Capability {
						if *e.Payload.(protocol.AgentRequest).Operation == "growth_notices" {
							return growth
						}
						return agents
					},
					Handle: func(context.Context, Session, protocol.Envelope) (Result, error) {
						calls.Add(1)
						return Result{Kind: protocol.KindAgentResult, Payload: protocol.AgentResult{}}, nil
					},
				},
			}}
			done := make(chan struct{})
			go func() { defer close(done); defer server.Close(); serveConn(server, identity) }()
			t.Cleanup(func() { client.Close(); server.Close(); <-done })
			client.SetDeadline(time.Now().Add(5 * time.Second))
			reader := bufio.NewReader(client)
			req := testHello(t)
			hello := req.Payload.(protocol.Hello)
			hello.Capabilities = offered
			req.Payload = hello
			welcome := exchange(t, client, reader, req)
			if welcome.Kind != protocol.KindWelcome || !slices.Equal(welcome.Payload.(protocol.Welcome).Capabilities, offered) {
				t.Fatalf("negotiation: %+v", welcome)
			}
			req.Epoch, req.Kind = welcome.Epoch, protocol.KindAgentRequest
			for _, operation := range []string{"list", "growth_notices"} {
				req.Payload = protocol.AgentRequest{Operation: &operation}
				before := calls.Load()
				answer := exchange(t, client, reader, req)
				required := agents
				if operation == "growth_notices" {
					required = growth
				}
				if slices.Contains(offered, required) {
					if answer.Kind != protocol.KindAgentResult || calls.Load() != before+1 {
						t.Fatalf("admitted %s: %+v", operation, answer)
					}
				} else if answer.Error == nil || answer.Error.Code != protocol.ErrorCapabilityNotNegotiated || calls.Load() != before {
					t.Fatalf("unnegotiated %s: %+v", operation, answer)
				}
			}
		})
	}
}
