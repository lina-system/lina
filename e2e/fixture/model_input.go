// SPDX-License-Identifier: Apache-2.0
// Package fixture provides observations for synthetic models in acceptance tests.
package fixture

import (
	"encoding/json"
	"encoding/xml"
	"strings"
)

// CurrentInput locates the active input, including later steering, before the
// mandatory context packet or after its frozen boundary. The initial input is
// immediately before the mandatory clock
// item of Core's ContextPacket. Packet evidence can also have the user role;
// selecting the last user message would mistake that evidence for new input.
// Requests without an assembled packet (such as summaries/exchanges) retain
// their existing final item. The provider request itself is never rewritten.
func CurrentInput(items []json.RawMessage) json.RawMessage {
	for i := 1; i < len(items); i++ {
		var item struct{ Role string }
		refs := contextReferences(items[i])
		if json.Unmarshal(items[i], &item) != nil || item.Role != "developer" || len(refs) == 0 || !strings.HasPrefix(refs[0].ID, "clock:") {
			continue
		}
		current := items[i-1]
		end := i
		for end < len(items) && len(contextReferences(items[end])) > 0 {
			end++
		}
		// Core appends steering after the frozen packet, alongside protocol
		// continuations. It becomes the active input even in a worker reaction.
		for _, raw := range items[end:] {
			var next struct{ Role string }
			if json.Unmarshal(raw, &next) == nil && next.Role == "user" {
				current = raw
			}
		}
		return current
	}
	if len(items) == 0 {
		return nil
	}
	return items[len(items)-1]
}

type contextReference struct{ ID string }

// Every rendered packet item retains its structured references. External
// evidence escapes these inside its XML envelope; decode only that envelope
// to identify packet boundaries, without changing any request bytes.
func contextReferences(raw json.RawMessage) []contextReference {
	var item struct{ Role, Content string }
	if json.Unmarshal(raw, &item) != nil || (item.Role != "user" && item.Role != "developer") {
		return nil
	}
	content := item.Content
	if at := strings.Index(content, "<external_evidence "); at >= 0 {
		var envelope struct {
			Content string `xml:",chardata"`
		}
		if xml.Unmarshal([]byte(content[at:]), &envelope) != nil {
			return nil
		}
		content = envelope.Content
	}
	const marker = "\nReferences: "
	at := strings.LastIndex(content, marker)
	var refs []contextReference
	if at < 0 || json.Unmarshal([]byte(content[at+len(marker):]), &refs) != nil {
		return nil
	}
	return refs
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
