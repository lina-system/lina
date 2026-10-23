// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"strings"
)

// fixtureTurnInput identifies the current event before the prepared packet.
// Packet evidence also has role=user, so the last user message is not enough.
// The packet starts with Core's clock reference; earlier packets may remain in
// history, and tool continuations may append results after the current packet.
func fixtureTurnInput(input []json.RawMessage) json.RawMessage {
	for i := len(input) - 1; i > 0; i-- {
		var item struct{ Role, Content string }
		if json.Unmarshal(input[i], &item) != nil || item.Role != "developer" {
			continue
		}
		_, references, ok := strings.Cut(item.Content, "\nReferences: ")
		var refs []struct{ ID string }
		if !ok || json.Unmarshal([]byte(references), &refs) != nil || len(refs) == 0 || !strings.HasPrefix(refs[0].ID, "clock:") {
			continue
		}
		return input[i-1]
	}
	return nil
}

// Scripted operations consume the latest tool result, otherwise the current
// event. This reads the actual request without reordering or filtering it.
func fixtureLatestInput(input []json.RawMessage) json.RawMessage {
	if len(input) > 0 {
		var item struct{ Type string }
		last := input[len(input)-1]
		if json.Unmarshal(last, &item) == nil && item.Type == "function_call_output" {
			return last
		}
	}
	return fixtureTurnInput(input)
}
