// SPDX-License-Identifier: Apache-2.0

// Package textcall recognizes terminal text-form tool calls. It proposes a
// recovery action; it never authorizes tools, executes them or opens sessions.
package textcall

import "encoding/json"

const (
	None     = "none"
	Dispatch = "parse_and_dispatch"
	Restart  = "restart_fresh_session"
	MaxBytes = 1024 * 1024
)

type Tool struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}
type Turn struct {
	StopReason        string            `json:"stop_reason"`
	ToolCalls         []json.RawMessage `json:"tool_calls"`
	Text              []string          `json:"text"`
	Thinking          []string          `json:"thinking"`
	DeltaTimestampsMS []int64           `json:"delta_timestamps_ms"`
}
type Call struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Decision struct {
	Detected    bool   `json:"detected"`
	Action      string `json:"action"`
	Call        *Call  `json:"parsed_call,omitempty"`
	VisibleText string `json:"-"`
}
type Gold struct {
	Detected       bool   `json:"detected"`
	Action         string `json:"action"`
	ParsedCall     *Call  `json:"parsed_call,omitempty"`
	ProgressLeaked bool   `json:"progress_leaked"`
}
type Case struct {
	ID            string `json:"id"`
	Slice         string `json:"slice"`
	Route         string `json:"route"`
	DeclaredTools []Tool `json:"declared_tools"`
	Turn          Turn   `json:"turn"`
	Gold          Gold   `json:"gold"`
}
