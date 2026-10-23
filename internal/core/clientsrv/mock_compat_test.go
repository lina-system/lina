// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"encoding/json"
	"github.com/thisisjun786/lina/kit/protocol"
	"os"
	"strings"
	"testing"
)

func TestMockProjectionRetainsFallbackAndGatesBody(t *testing.T) {
	raw, err := os.ReadFile("../../../protocol/fixtures/client/1.1/mock.json")
	if err != nil {
		t.Fatal(err)
	}
	var e protocol.Envelope
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	old := projectMocks(e, Session{ProtocolVersion: "1.0"})
	item, ok := old.Payload.(protocol.ItemDone)
	if !ok || item.Text == nil || !strings.Contains(*item.Text, "Export PNG:") {
		t.Fatal("legacy fallback missing")
	}
	if _, err = json.Marshal(old); err != nil {
		t.Fatal(err)
	}
	plain := projectMocks(e, Session{ProtocolVersion: "1.1"})
	if plain.Payload.(protocol.Result).Body != nil {
		t.Fatal("body sent without rendering capability")
	}
	full := projectMocks(e, Session{ProtocolVersion: "1.1", Capabilities: []protocol.Capability{MocksCapability}})
	if full.Payload.(protocol.Result).Body == nil {
		t.Fatal("body missing for capable client")
	}
	payload, _ := json.Marshal(e.Payload)
	e.Kind = protocol.KindSnapshot
	e.Payload = protocol.Snapshot{Topic: "conversation:main:agent", Cursor: protocol.StreamCursor{Epoch: "1", Seq: "1"}, Items: []protocol.StreamItem{{Kind: protocol.KindResult, Key: "mock", Revision: "1", Payload: payload}}}
	projected := projectMocks(e, Session{ProtocolVersion: "1.0"}).Payload.(protocol.Snapshot)
	if projected.Items[0].Kind != protocol.KindItemDone {
		t.Fatal("snapshot bypassed fallback")
	}
	if e.Payload.(protocol.Snapshot).Items[0].Kind != protocol.KindResult {
		t.Fatal("projection mutated retained snapshot")
	}
}
