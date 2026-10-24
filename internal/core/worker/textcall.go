// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"encoding/json"
	"strings"

	"github.com/thisisjun786/lina/internal/core/textcall"
)

// TextCallFailure identifies a settled turn whose transcript must not be resumed.
const TextCallFailure = "text_tool_call"

// TurnText bounds private assistant text while waiting for a terminal decision.
// Complete items replace their deltas; an overflow never publishes a prefix.
type TurnText struct {
	parts    map[string]string
	order    []string
	bytes    int
	Overflow bool
}

func (b *TurnText) Add(id, text string, complete bool) {
	if b.Overflow {
		return
	}
	prior, exists := b.parts[id]
	size := b.bytes + len(text)
	if complete {
		size -= len(prior)
	}
	if size > textcall.MaxBytes || !exists && len(b.order) >= 256 {
		*b = TurnText{Overflow: true}
		return
	}
	if b.parts == nil {
		b.parts = map[string]string{}
	}
	if !exists {
		b.order = append(b.order, id)
	}
	if complete {
		b.parts[id] = text
	} else {
		b.parts[id] = prior + text
	}
	b.bytes = size
}

func (b *TurnText) Text() string {
	var parts []string
	for _, id := range b.order {
		parts = append(parts, b.parts[id])
	}
	return strings.Join(parts, "\n")
}

// DetectTextCall only recovers the session's declared LINA work tools. A native
// worker tool or a similarly named extension cannot gain authority from text.
func DetectTextCall(text, stop string, realTool bool, tools []Tool) textcall.Decision {
	turn := textcall.Turn{Text: []string{text}, StopReason: stop}
	if realTool {
		turn.ToolCalls = []json.RawMessage{json.RawMessage(`{}`)}
	}
	var declared []textcall.Tool
	for _, tool := range tools {
		if tool.Namespace != "lina" {
			continue
		}
		switch tool.Name {
		case "read_projection", "ask", "coordinate", "submit_receipt", "check_inbox":
			declared = append(declared, textcall.Tool{Name: tool.Name, Schema: tool.InputSchema})
		}
	}
	return textcall.Detect(turn, declared)
}
