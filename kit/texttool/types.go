// SPDX-License-Identifier: Apache-2.0

// Package texttool recognizes terminal text-form tool calls without executing
// tools, opening sessions, or changing a provider or worker configuration.
package texttool

import "encoding/json"

const (
	None                = "none"
	ParseAndDispatch    = "parse_and_dispatch"
	RestartFreshSession = "restart_fresh_session"
)

type Declaration struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}

type Call struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Turn struct {
	StopReason        string   `json:"stop_reason"`
	ToolCalls         []Call   `json:"tool_calls"`
	Text              string   `json:"text"`
	Thinking          []string `json:"thinking"`
	DeltaTimestampsMS []int64  `json:"delta_timestamps_ms"`
}

// Decision is a plan, not a receipt. Callers must authorize and validate any
// proposed call through their normal dispatcher before executing it.
type Decision struct {
	Detected           bool   `json:"detected"`
	Action             string `json:"action"`
	ParsedCall         *Call  `json:"parsed_call,omitempty"`
	VisibleText        string `json:"visible_text"`
	ExtraTurns         int    `json:"extra_turns"`
	FreshSessionStarts int    `json:"fresh_session_starts"`
	SameSessionNudges  int    `json:"same_session_nudges"`
}
