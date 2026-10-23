// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestSurfaceNegotiationAndProjection(t *testing.T) {
	cap := protocol.SurfaceResultsCapability()
	offered := protocol.SurfaceResultsCapability()
	*offered.Catalogs = append(*offered.Catalogs, "unknown")
	rows := []Combination{{Connection: "client", Peer: "test", Platform: "linux/amd64", ProtocolVersion: "1.1", Capabilities: []protocol.Capability{cap}, Evidence: []string{"test"}}}
	version, granted, ok := negotiate(rows, "test", "linux/amd64", []string{"1.1"}, []protocol.Capability{offered})
	if !ok || version != "1.1" || len(granted) != 1 || len(*granted[0].Catalogs) != 1 {
		t.Fatalf("negotiation: %s %+v %t", version, granted, ok)
	}
	raw, err := os.ReadFile("../../../protocol/fixtures/client/1.0/normal/surface.json")
	if err != nil {
		t.Fatal(err)
	}
	var frame protocol.Envelope
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatal(err)
	}
	original := frame.Payload.(protocol.Result)
	for _, tc := range []struct {
		name, version string
		cap           *protocol.Capability
		kind          protocol.Kind
		body          bool
	}{
		{"full", "1.1", &cap, protocol.KindResult, true},
		{"text", "1.1", &protocol.Capability{Name: "results", Version: "1.0"}, protocol.KindResult, false},
		{"no capability", "1.1", nil, protocol.KindItemDone, false},
		{"legacy", "1.0", nil, protocol.KindItemDone, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Session{ProtocolVersion: tc.version}
			if tc.cap != nil {
				s.Capabilities = []protocol.Capability{*tc.cap}
			}
			got := projectResults(frame, s)
			if got.Kind != tc.kind {
				t.Fatalf("kind %s", got.Kind)
			}
			if p, ok := got.Payload.(protocol.Result); ok && (p.Body != nil) != tc.body {
				t.Fatalf("body=%v", p.Body)
			}
			if _, err := json.Marshal(got); err != nil {
				t.Fatal(err)
			}
			item, _ := json.Marshal(original)
			snap := frame
			snap.Kind = protocol.KindSnapshot
			snap.Payload = protocol.Snapshot{Items: []protocol.StreamItem{{Kind: protocol.KindResult, Payload: item}}}
			projected := projectResults(snap, s).Payload.(protocol.Snapshot)
			if projected.Items[0].Kind != tc.kind {
				t.Fatal("snapshot projection differs")
			}
			if original.Body == nil || frame.Payload.(protocol.Result).Body == nil {
				t.Fatal("projection mutated retained frame")
			}
		})
	}
}

func TestSurfaceMinorNegotiatesThroughListener(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	identity := Identity{MainID: "main-test", Epoch: "1", Operations: map[protocol.Kind]Operation{
		protocol.KindSurfaceAction: {Capability: ResultsCapability, Handle: func(_ context.Context, s Session, _ protocol.Envelope) (Result, error) {
			if s.ProtocolVersion != "1.1" || !protocol.HasCapability(s.Capabilities, ResultsCapability) {
				t.Error("lost negotiated session")
			}
			return Result{Kind: protocol.KindItemDone, Payload: protocol.ItemDone{}}, nil
		}},
	}}
	go func() { defer close(done); defer server.Close(); serveConn(server, identity) }()
	t.Cleanup(func() { client.Close(); server.Close(); <-done })
	client.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(client)
	request := testHello(t)
	request.Protocol.Version = "1.1"
	hello := request.Payload.(protocol.Hello)
	hello.ProtocolVersions = []string{"1.1", "1.0"}
	hello.Capabilities = []protocol.Capability{protocol.SurfaceResultsCapability()}
	request.Payload = hello
	response := exchange(t, client, reader, request)
	welcome, ok := response.Payload.(protocol.Welcome)
	if !ok || welcome.ProtocolVersion != "1.1" || len(welcome.Capabilities) != 1 || welcome.Capabilities[0].Catalogs == nil {
		t.Fatalf("welcome: %+v", response)
	}
	request.Epoch = response.Epoch
	request.Kind = protocol.KindSurfaceAction
	request.Payload = protocol.SurfaceAction{ResultID: "card", Revision: "1", Action: "choose", InputID: "press", Values: map[string]json.RawMessage{}}
	response = exchange(t, client, reader, request)
	if response.Kind != protocol.KindItemDone || response.Error != nil || response.Protocol.Version != "1.1" {
		t.Fatalf("action dispatch: %+v", response)
	}
}

func TestResultsCapabilityCanReadWithoutConversationGrant(t *testing.T) {
	client, server := net.Pipe()
	done := make(chan struct{})
	var read atomic.Bool
	identity := Identity{MainID: "main-test", Epoch: "1", Operations: map[protocol.Kind]Operation{
		protocol.KindResultGet: {Capability: ConversationCapability, Alternatives: []protocol.Capability{ResultsCapability}, Handle: func(context.Context, Session, protocol.Envelope) (Result, error) {
			read.Store(true)
			return Result{Kind: protocol.KindItemDone, Payload: protocol.ItemDone{}}, nil
		}},
	}}
	go func() { defer close(done); defer server.Close(); serveConn(server, identity) }()
	t.Cleanup(func() { client.Close(); server.Close(); <-done })
	client.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(client)
	request := testHello(t)
	request.Protocol.Version = "1.1"
	hello := request.Payload.(protocol.Hello)
	hello.ProtocolVersions = []string{"1.1"}
	hello.Capabilities = []protocol.Capability{protocol.SurfaceResultsCapability()}
	request.Payload = hello
	response := exchange(t, client, reader, request)
	welcome, ok := response.Payload.(protocol.Welcome)
	if !ok || len(welcome.Capabilities) != 1 {
		t.Fatalf("welcome: %+v", response)
	}
	request.Epoch = response.Epoch
	request.Kind = protocol.KindResultGet
	request.Payload = protocol.ResultGet{ResultID: "card"}
	response = exchange(t, client, reader, request)
	if response.Error != nil || !read.Load() {
		t.Fatalf("results-only read: %+v", response)
	}
}
