// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"encoding/json"
	"os"
	"testing"

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
