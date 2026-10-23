// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"encoding/json"
	"strings"

	"github.com/thisisjun786/lina/kit/provider"
)

// UTF-8 bytes, not tokens. The complete adapter request is still measured by
// Pass. This bound limits growth; it does not claim the request will fit.
const maxCarriedValueBytes = 24 * 1024

func summaryText(item provider.Item) (Text, error) {
	var envelope struct{ Role, Content string }
	if json.Unmarshal(item, &envelope) != nil || envelope.Role != "user" {
		return Text{}, ErrSummary
	}
	body, ok := strings.CutPrefix(envelope.Content, handoff)
	if !ok {
		body, ok = strings.CutPrefix(envelope.Content, managerHandoff)
	}
	var text Text
	if !ok || json.Unmarshal([]byte(body), &text) != nil || !text.valid() {
		return Text{}, ErrSummary
	}
	return text, nil
}

func carryValues(previous, added string) string {
	if strings.TrimSpace(previous) == "none" {
		return added
	}
	if strings.TrimSpace(added) == "none" || previous == added {
		return previous
	}
	// Preserve the old bytes and chronological order. These are historical
	// values; later corrections take precedence, and retractions rebuild from
	// visible sources instead of copying an invalidated root.
	return previous + "\n" + added
}
