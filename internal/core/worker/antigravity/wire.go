// SPDX-License-Identifier: Apache-2.0
package antigravity

type DeniedAction struct {
	Action      string `json:"action"`
	DisplayName string `json:"display_name"`
}
type Usage struct {
	Input     *int64 `json:"input_tokens"`
	Output    *int64 `json:"output_tokens"`
	Cached    *int64 `json:"cache_read_tokens"`
	Reasoning *int64 `json:"thinking_tokens"`
}

// Result is a worker report, not evidence of PR delivery.
type Result struct {
	ConversationID string         `json:"conversation_id"`
	Status         string         `json:"status"`
	Response       string         `json:"response"`
	Error          string         `json:"error,omitempty"`
	DeniedActions  []DeniedAction `json:"denied_actions,omitempty"`
	Usage          *Usage         `json:"usage,omitempty"`
}
type frame struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	Init           *struct {
		CWD   string `json:"cwd"`
		Model string `json:"model"`
	} `json:"init"`
	Step *struct {
		ConversationID string `json:"conversation_id"`
		Index          int    `json:"step_index"`
		State          string `json:"state"`
		Text           string `json:"text_delta"`
		ToolName       string `json:"tool_name"`
	} `json:"step_update"`
	Result *Result `json:"result"`
}
