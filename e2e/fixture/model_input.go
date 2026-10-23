// SPDX-License-Identifier: Apache-2.0
// Package fixture provides observations for synthetic models in acceptance tests.
package fixture

import (
	"encoding/json"
	"strings"
)

// CurrentInput locates the turn input immediately before the mandatory clock
// item of Core's ContextPacket. Packet evidence can also have the user role;
// selecting the last user message would mistake that evidence for new input.
// Requests without an assembled packet (such as summaries/exchanges) retain
// their existing final item. The provider request itself is never rewritten.
func CurrentInput(items []json.RawMessage) json.RawMessage {
	for i := 1; i < len(items); i++ {
		var item struct{ Role, Content string }
		if json.Unmarshal(items[i], &item) != nil || item.Role != "developer" {
			continue
		}
		_, references, ok := strings.Cut(item.Content, "\nReferences: ")
		var refs []struct{ ID string }
		if ok && json.Unmarshal([]byte(references), &refs) == nil && len(refs) > 0 && strings.HasPrefix(refs[0].ID, "clock:") {
			return items[i-1]
		}
	}
	if len(items) == 0 {
		return nil
	}
	return items[len(items)-1]
}

// LastInput keeps actual tool follow-ups at the tail, while an initial request
// is driven by its turn input rather than by the trailing ContextPacket.
func LastInput(items []json.RawMessage) json.RawMessage {
	if len(items) > 0 {
		last := items[len(items)-1]
		var item struct{ Type string }
		if json.Unmarshal(last, &item) == nil && (item.Type == "function_call_output" || item.Type == "custom_tool_call_output") {
			return last
		}
	}
	return CurrentInput(items)
}
