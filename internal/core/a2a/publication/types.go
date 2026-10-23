// SPDX-License-Identifier: Apache-2.0
package publication

import "encoding/json"

// Wire types follow A2A v1.0.1 a2a.proto (ProtoJSON), not the 0.3 examples.
const (
	Submitted     = "TASK_STATE_SUBMITTED"
	Working       = "TASK_STATE_WORKING"
	Completed     = "TASK_STATE_COMPLETED"
	Failed        = "TASK_STATE_FAILED"
	Canceled      = "TASK_STATE_CANCELED"
	Rejected      = "TASK_STATE_REJECTED"
	InputRequired = "TASK_STATE_INPUT_REQUIRED"
	AuthRequired  = "TASK_STATE_AUTH_REQUIRED"
	Unspecified   = "TASK_STATE_UNSPECIFIED"
)

func Interrupted(state string) bool {
	return Terminal(state) || state == InputRequired || state == AuthRequired
}
func Terminal(state string) bool {
	return state == Completed || state == Failed || state == Canceled || state == Rejected
}

type Part struct {
	Text      *string         `json:"text,omitempty"`
	Raw       []byte          `json:"raw,omitempty"`
	URL       string          `json:"url,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
}
type Message struct {
	MessageID        string          `json:"messageId"`
	ContextID        string          `json:"contextId,omitempty"`
	TaskID           string          `json:"taskId,omitempty"`
	Role             string          `json:"role"`
	Parts            []Part          `json:"parts"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	Extensions       []string        `json:"extensions,omitempty"`
	ReferenceTaskIDs []string        `json:"referenceTaskIds,omitempty"`
}
type Artifact struct {
	ArtifactID string          `json:"artifactId"`
	Name       string          `json:"name,omitempty"`
	Parts      []Part          `json:"parts"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}
type Status struct {
	State     string   `json:"state"`
	Message   *Message `json:"message,omitempty"`
	Timestamp string   `json:"timestamp"`
}
type Task struct {
	ID        string          `json:"id"`
	ContextID string          `json:"contextId"`
	Status    Status          `json:"status"`
	Artifacts []Artifact      `json:"artifacts,omitempty"`
	History   []Message       `json:"history,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}
type SendRequest struct {
	Tenant        string  `json:"tenant,omitempty"`
	Message       Message `json:"message"`
	Configuration struct {
		AcceptedOutputModes        []string        `json:"acceptedOutputModes,omitempty"`
		TaskPushNotificationConfig json.RawMessage `json:"taskPushNotificationConfig,omitempty"`
		HistoryLength              *int            `json:"historyLength,omitempty"`
		ReturnImmediately          bool            `json:"returnImmediately,omitempty"`
	} `json:"configuration,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}
type TaskRequest struct {
	Tenant        string          `json:"tenant,omitempty"`
	ID            string          `json:"id"`
	HistoryLength *int            `json:"historyLength,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}
type ListRequest struct {
	Tenant               string `json:"tenant,omitempty"`
	ContextID            string `json:"contextId,omitempty"`
	Status               string `json:"status,omitempty"`
	PageSize             *int   `json:"pageSize,omitempty"`
	PageToken            string `json:"pageToken,omitempty"`
	HistoryLength        *int   `json:"historyLength,omitempty"`
	StatusTimestampAfter string `json:"statusTimestampAfter,omitempty"`
	IncludeArtifacts     bool   `json:"includeArtifacts,omitempty"`
}
type ListResponse struct {
	Tasks         []Task `json:"tasks"`
	NextPageToken string `json:"nextPageToken"`
	PageSize      int    `json:"pageSize"`
	TotalSize     int    `json:"totalSize"`
}

func Projection(t Task, history *int, artifacts bool) Task {
	if history != nil && len(t.History) > *history {
		t.History = t.History[len(t.History)-*history:]
	}
	if !artifacts {
		t.Artifacts = nil
	}
	return t
}
