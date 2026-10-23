// SPDX-License-Identifier: Apache-2.0
package fixture

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCurrentInputTracksSteeringAfterContext(t *testing.T) {
	message := func(role, content string) json.RawMessage {
		raw, err := json.Marshal(map[string]string{"role": role, "content": content})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	old := message("user", "earlier request")
	current := message("user", "worker completion evidence")
	clock := message("developer", "2026-10-23T00:00:00Z\nReferences: [{\"id\":\"clock:current\"}]")
	context := message("user", "External evidence follows.\n<external_evidence origin=\"context:record\">Context data\nReferences: &#91;{&quot;id&quot;:&quot;record&quot;}&#93;</external_evidence>")
	packet := []json.RawMessage{old, current, clock, context}
	toolResult := json.RawMessage(`{"type":"function_call_output","call_id":"read","output":"ok"}`)
	steering := message("user", "Observe the landed foundation.")
	for _, tc := range []struct {
		name          string
		items         []json.RawMessage
		current, last json.RawMessage
	}{
		{"initial", packet, current, current},
		{"tool-followup", append(append([]json.RawMessage{}, packet...), toolResult), current, toolResult},
		{"steering", append(append([]json.RawMessage{}, packet...), toolResult, steering), steering, steering},
		{"steering-then-tool", append(append([]json.RawMessage{}, packet...), steering, toolResult), steering, toolResult},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CurrentInput(tc.items); !bytes.Equal(got, tc.current) {
				t.Fatalf("current = %s, want %s", got, tc.current)
			}
			if got := LastInput(tc.items); !bytes.Equal(got, tc.last) {
				t.Fatalf("last = %s, want %s", got, tc.last)
			}
		})
	}
}
