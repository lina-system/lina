// SPDX-License-Identifier: Apache-2.0
// Package a2a implements Core's outbound A2A 1.0 profile.
package a2a

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/kit/tools"
)

var (
	ErrInvalid  = errors.New("a2a_invalid")
	ErrScope    = errors.New("a2a_out_of_scope")
	ErrChanged  = errors.New("a2a_card_changed")
	ErrSignIn   = errors.New("a2a_needs_sign_in")
	ErrUnknown  = errors.New("a2a_unknown")
	ErrEvidence = errors.New("a2a_unverified")
)

type Interface struct {
	URL     string `json:"url"`
	Binding string `json:"protocolBinding"`
	Version string `json:"protocolVersion"`
	Tenant  string `json:"tenant,omitempty"`
}
type Card struct {
	Name                 string                     `json:"name"`
	Interfaces           []Interface                `json:"supportedInterfaces"`
	InputModes           []string                   `json:"defaultInputModes"`
	OutputModes          []string                   `json:"defaultOutputModes"`
	SecuritySchemes      map[string]json.RawMessage `json:"securitySchemes"`
	SecurityRequirements []struct {
		Schemes map[string]Scopes `json:"schemes"`
	} `json:"securityRequirements"`
	Capabilities struct {
		Streaming  bool `json:"streaming"`
		Extensions []struct {
			Required bool `json:"required"`
		} `json:"extensions"`
	} `json:"capabilities"`
}
type Scopes struct {
	List []string `json:"list"`
}
type Part struct {
	Text      *string         `json:"text,omitempty"`
	Raw       string          `json:"raw,omitempty"`
	URL       string          `json:"url,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Filename  string          `json:"filename,omitempty"`
}
type Message struct {
	ID      string `json:"messageId"`
	Context string `json:"contextId,omitempty"`
	Task    string `json:"taskId,omitempty"`
	Role    string `json:"role"`
	Parts   []Part `json:"parts"`
}
type Artifact struct {
	ID    string `json:"artifactId"`
	Name  string `json:"name,omitempty"`
	Parts []Part `json:"parts"`
}
type Status struct {
	State   string   `json:"state"`
	Message *Message `json:"message,omitempty"`
}
type Task struct {
	ID        string     `json:"id"`
	Context   string     `json:"contextId"`
	Status    Status     `json:"status"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
}
type streamResponse struct {
	Task    *Task    `json:"task,omitempty"`
	Message *Message `json:"message,omitempty"`
	Status  *struct {
		TaskID  string `json:"taskId"`
		Context string `json:"contextId"`
		Status  Status `json:"status"`
	} `json:"statusUpdate,omitempty"`
	Artifact *struct {
		TaskID   string   `json:"taskId"`
		Context  string   `json:"contextId"`
		Artifact Artifact `json:"artifact"`
		Append   bool     `json:"append"`
		Last     bool     `json:"lastChunk"`
	} `json:"artifactUpdate,omitempty"`
}

// Profile is an owner-approved task definition, never supplied by the model or
// inferred from Agent Card annotations. SHA256 is the local done condition.
type Profile struct {
	Name        string            `json:"name"`
	Class       tools.EffectClass `json:"effect"`
	InputPrefix string            `json:"input_prefix"`
	MaxInput    int               `json:"max_input_bytes"`
	MaxOutput   int               `json:"max_output_bytes"`
	SHA256      string            `json:"sha256"`
	MediaType   string            `json:"media_type"`
}
type OAuth struct {
	Issuer           string   `json:"issuer"`
	AuthorizationURL string   `json:"authorization_url"`
	TokenURL         string   `json:"token_url"`
	ClientID         string   `json:"client_id"`
	Scopes           []string `json:"scopes"`
}
type Connection struct {
	ID                string    `json:"id"`
	Person            string    `json:"person"`
	CardURL           string    `json:"card_url"`
	Origin            string    `json:"origin"`
	PrivateHost       string    `json:"private_host,omitempty"`
	Scheme            string    `json:"scheme"`
	OAuth             *OAuth    `json:"oauth,omitempty"`
	Profiles          []Profile `json:"profiles"`
	Interface         Interface `json:"interface"`
	CardHash          string    `json:"card_hash"`
	Card              Card      `json:"card"`
	CredentialExpires time.Time `json:"credential_expires,omitempty"`
	Revision          string    `json:"revision"`
	Enabled           bool      `json:"enabled"`
}
type Origin struct {
	GrantID      string `json:"grant_id"`
	Person       string `json:"person"`
	Agent        string `json:"agent"`
	Conversation string `json:"conversation"`
	Turn         string `json:"turn"`
	Main         string `json:"main"`
	Epoch        uint64 `json:"epoch"`
}
type Input struct {
	Connection string `json:"connection"`
	Task       string `json:"task"`
	Text       string `json:"text"`
	Call       string `json:"call,omitempty"`
	Stream     bool   `json:"stream,omitempty"`
}
type Evidence struct {
	Artifact  string    `json:"artifact"`
	MediaType string    `json:"media_type"`
	Body      []byte    `json:"body"`
	SHA256    string    `json:"sha256"`
	Source    string    `json:"source"`
	Observed  time.Time `json:"observed"`
}
type Call struct {
	Admitted    Connection      `json:"admitted_connection"`
	Intent      string          `json:"intent"`
	ID          string          `json:"id"`
	Origin      Origin          `json:"origin"`
	Connection  string          `json:"connection"`
	Revision    string          `json:"connection_revision"`
	Profile     Profile         `json:"profile"`
	Payload     json.RawMessage `json:"payload"`
	PayloadHash string          `json:"payload_hash"`
	MessageID   string          `json:"message_id"`
	Deadline    time.Time       `json:"deadline"`
	TaskID      string          `json:"task_id,omitempty"`
	ContextID   string          `json:"context_id,omitempty"`
	State       string          `json:"state"`
	RemoteState string          `json:"remote_state,omitempty"`
	Evidence    []Evidence      `json:"evidence,omitempty"`
	Verified    bool            `json:"verified"`
	Delivered   bool            `json:"delivered"`
	Question    string          `json:"question,omitempty"`
	Parent      string          `json:"parent,omitempty"`
	Answer      string          `json:"answer,omitempty"`
}
