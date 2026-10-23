// SPDX-License-Identifier: Apache-2.0
package result

import "encoding/json"

// Scope is supplied by trusted Core admission, never decoded from model input.
type Scope struct {
	PersonID       string  `json:"person_id"`
	AgentID        string  `json:"agent_id"`
	ConversationID string  `json:"conversation_id"`
	TurnID         string  `json:"turn_id"`
	TaskID         *string `json:"task_id"`
}
type Provenance struct {
	AgentID        string  `json:"agent_id"`
	ConversationID string  `json:"conversation_id"`
	TurnID         string  `json:"turn_id"`
	TaskID         *string `json:"task_id"`
}
type Result struct {
	ReplyType  string          `json:"reply_type,omitempty"`
	ID         string          `json:"result_id"`
	Revision   int             `json:"revision"`
	Type       Type            `json:"type"`
	Title      string          `json:"title"`
	Summary    string          `json:"summary"`
	Text       string          `json:"text"`
	Body       json.RawMessage `json:"body"`
	Status     string          `json:"status"`
	SavedAs    *SavedRef       `json:"saved_as"`
	RevisedBy  string          `json:"revised_by"`
	Provenance Provenance      `json:"provenance"`
}

// Card is the model-visible acknowledgement. The complete body is returned only
// by Read. Earlier acknowledgements are immutable history items.
type Card struct {
	ID       string    `json:"result_id"`
	Revision int       `json:"revision"`
	Type     Type      `json:"type"`
	Title    string    `json:"title"`
	Summary  string    `json:"summary"`
	Text     string    `json:"text"`
	SavedAs  *SavedRef `json:"saved_as"`
}
type PresentRequest struct {
	ReplyType    string          `json:"reply_type,omitempty"`
	ID           string          `json:"result_id,omitempty"`
	BaseRevision int             `json:"base_revision,omitempty"`
	Type         Type            `json:"type"`
	Title        string          `json:"title"`
	Summary      string          `json:"summary"`
	Body         json.RawMessage `json:"body"`
}
type ReadRequest struct {
	ID string `json:"result_id"`
}

// Destination carries a resolved record location. Container is an opaque id;
// the records owner resolves it to a path, never the model.
type Destination struct {
	Kind      string `json:"kind"`
	Container string `json:"container,omitempty"`
}
type SaveRequest struct {
	ID          string      `json:"result_id"`
	Revision    int         `json:"revision"`
	Destination Destination `json:"destination"`
}
type SavedRef struct {
	ID          string      `json:"record_id"`
	Version     int         `json:"version"`
	Destination Destination `json:"destination"`
}
type SaveReceipt struct {
	State   string    `json:"effect_state"`
	SavedAs *SavedRef `json:"saved_as"`
}

// ValidReplyType checks a structural annotation, never natural-language intent.
func ValidReplyType(kind string) bool {
	switch kind {
	case "", "chat", "status", "comparison", "candidates", "third_party":
		return true
	}
	return false
}
