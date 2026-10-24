// SPDX-License-Identifier: Apache-2.0
package textcall

import (
	"encoding/json"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// Guard applies the shared parser before stream consumers can publish or save
// text. Parsed calls are provider events, not authorized tool executions.
func Guard(open loop.StreamFunc) loop.StreamFunc {
	return loop.GuardTextCalls(open, func(text string, declarations []provider.ToolDecl) (*provider.ToolCall, bool) {
		tools := make([]Tool, 0, len(declarations))
		for _, raw := range declarations {
			var d struct {
				Type, Name string
				Parameters json.RawMessage
			}
			if json.Unmarshal(raw, &d) == nil && d.Type == "function" {
				tools = append(tools, Tool{Name: d.Name, Schema: d.Parameters})
			}
		}
		decision := Detect(Turn{StopReason: "stop", Text: []string{text}}, tools)
		if decision.Call == nil {
			return nil, decision.Detected
		}
		return &provider.ToolCall{Name: decision.Call.Name, Arguments: string(decision.Call.Arguments)}, true
	})
}
