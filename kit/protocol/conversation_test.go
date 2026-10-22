// SPDX-License-Identifier: Apache-2.0

package protocol

import "testing"

func TestConversationPayloadRoundTrips(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		payload map[string]any
	}{
		{"input", map[string]any{"conversation_id": "conversation", "input_id": "input", "text": "한글", "mode": "steer"}},
		{"interrupt", map[string]any{"conversation_id": "conversation"}},
		{"history_get", map[string]any{"agent_id": "agent"}},
		{"history", map[string]any{"conversation_id": "conversation"}},
		{"item_done", map[string]any{"text": "message", "echo_key": "input", "speaker": "person", "agent_id": "agent-example", "person_id": "person-example"}},
		{"item_done", map[string]any{"text": "forwarded message", "speaker": "forwarded", "agent_id": "agent-example"}},
		{"turn_state", map[string]any{"active": true}},
	} {
		t.Run(tc.kind, func(t *testing.T) { roundTrip(t, clientMessage(t, tc.kind, tc.payload)) })
	}
}
