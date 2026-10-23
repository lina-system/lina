// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"encoding/json"
	"strings"
)

// publishedBody enriches the live projection only. The original content and
// tool_calls remain in import_event and legacy_ref for exact reverse mapping.
// content is the event's display text: its body, or the sidecar bytes of a
// material-backed event.
func publishedBody(r *record, content string) (string, error) {
	if r.event.role != "assistant" || len(r.sources) == 0 {
		return content, nil
	}
	// The first source owns this event's payload. Other sources are matched
	// copies or binding/history rows and must not add duplicate or changed calls.
	raw := r.sources[0].row.text("tool_calls")
	if trimmed := strings.TrimSpace(raw); trimmed == "" || trimmed == "null" {
		return content, nil
	}
	var source []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string  `json:"name"`
			Arguments *string `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal([]byte(raw), &source) != nil {
		return unparsedCallBody(content, raw)
	}
	if len(source) == 0 {
		return content, nil
	}
	type call struct {
		Type      string `json:"type"`
		ID        string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	calls := make([]call, 0, len(source))
	for _, c := range source {
		if c.Type != "function" || c.ID == "" || c.Function.Name == "" || c.Function.Arguments == nil {
			return unparsedCallBody(content, raw)
		}
		calls = append(calls, call{"function_call", c.ID, c.Function.Name, *c.Function.Arguments})
	}
	body, err := json.Marshal(struct {
		Content string `json:"content"`
		Calls   []call `json:"tool_calls"`
	}{content, calls})
	return string(body), err
}

// Unknown source payloads remain readable evidence. Keeping their exact text
// avoids making an otherwise lossless import depend on a callable tool schema.
func unparsedCallBody(content, raw string) (string, error) {
	body, err := json.Marshal(struct {
		Content string `json:"content"`
		Raw     string `json:"unparsed_tool_calls"`
	}{content, raw})
	return string(body), err
}
