// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestWorkerApprovalCardCarriesOriginConversation(t *testing.T) {
	t.Parallel()
	f := setup(t)
	a := requestWorker(t, f)
	d := clientsrv.Decisions{Authority: f.service, Person: a.Person}
	cards, err := d.ApprovalCards(t.Context())
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards: %+v %v", cards, err)
	}
	data, err := json.Marshal(cards[0])
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["conversation_id"] != a.Conversation {
		t.Fatalf("origin missing from card: %s", data)
	}
	if cards[0].Card.ID != a.ID || len(cards[0].Choices) != 2 {
		t.Fatalf("card changed: %+v", cards[0])
	}
}

func TestWorkerApprovalConversationProtocolCompatibility(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../../protocol/fixtures/client/1.0/normal/approval_request.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name           string
		conversation   any
		present, valid bool
	}{
		{name: "existing fixture", valid: true},
		{name: "origin", conversation: "conversation", present: true, valid: true},
		{name: "empty origin", conversation: "", present: true},
		{name: "null origin", present: true},
		{name: "wrong type", conversation: 7, present: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var wire map[string]any
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatal(err)
			}
			if test.present {
				wire["payload"].(map[string]any)["conversation_id"] = test.conversation
			}
			raw, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var envelope protocol.Envelope
			err = json.Unmarshal(raw, &envelope)
			if (err == nil) != test.valid {
				t.Fatalf("protocol valid=%v: %v", test.valid, err)
			}
			if test.valid {
				encoded, err := json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				var roundTrip map[string]any
				if err := json.Unmarshal(encoded, &roundTrip); err != nil {
					t.Fatal(err)
				}
				got, present := roundTrip["payload"].(map[string]any)["conversation_id"]
				if present != test.present || got != test.conversation {
					t.Fatalf("origin changed: %s", encoded)
				}
			}
		})
	}
}
