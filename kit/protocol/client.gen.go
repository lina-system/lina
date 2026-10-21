// SPDX-License-Identifier: Apache-2.0

// Code generated from protocol/schema/client/1.0/envelope.json; DO NOT EDIT.

package protocol

import (
	"encoding/json"
	"fmt"
)

type Kind string

const (
	KindHello                Kind = "hello"
	KindWelcome              Kind = "welcome"
	KindError                Kind = "error"
	KindConversationStart    Kind = "conversation_start"
	KindInput                Kind = "input"
	KindInterrupt            Kind = "interrupt"
	KindHistoryGet           Kind = "history_get"
	KindHistory              Kind = "history"
	KindSubscribe            Kind = "subscribe"
	KindOutputDelta          Kind = "output_delta"
	KindItemDone             Kind = "item_done"
	KindTurnState            Kind = "turn_state"
	KindApprovalRequest      Kind = "approval_request"
	KindApprovalAnswer       Kind = "approval_answer"
	KindQuestion             Kind = "question"
	KindQuestionAnswer       Kind = "question_answer"
	KindResult               Kind = "result"
	KindResultGet            Kind = "result_get"
	KindResultEdit           Kind = "result_edit"
	KindResultSave           Kind = "result_save"
	KindRunningGet           Kind = "running_get"
	KindRunning              Kind = "running"
	KindRunningUpdate        Kind = "running_update"
	KindStop                 Kind = "stop"
	KindFollowUp             Kind = "follow_up"
	KindBackgroundNotice     Kind = "background_notice"
	KindProgressGet          Kind = "progress_get"
	KindProgress             Kind = "progress"
	KindProgressUpdate       Kind = "progress_update"
	KindProgressDigestGet    Kind = "progress_digest_get"
	KindProgressDigest       Kind = "progress_digest"
	KindWaitingGet           Kind = "waiting_get"
	KindWaiting              Kind = "waiting"
	KindWaitingUpdate        Kind = "waiting_update"
	KindWaitingAnswer        Kind = "waiting_answer"
	KindDetailsGet           Kind = "details_get"
	KindDetails              Kind = "details"
	KindReadinessFailure     Kind = "readiness_failure"
	KindAgentRequest         Kind = "agent_request"
	KindAgentResult          Kind = "agent_result"
	KindWorkRequest          Kind = "work_request"
	KindWorkResult           Kind = "work_result"
	KindAccountRequest       Kind = "account_request"
	KindAccountResult        Kind = "account_result"
	KindMemoryRequest        Kind = "memory_request"
	KindMemoryResult         Kind = "memory_result"
	KindMemoryImportProgress Kind = "memory_import_progress"
	KindMemoryPromotionBatch Kind = "memory_promotion_batch"
	KindChannelRequest       Kind = "channel_request"
	KindChannelResult        Kind = "channel_result"
	KindChannelState         Kind = "channel_state"
	KindAgentTalkGet         Kind = "agent_talk_get"
	KindAgentTalk            Kind = "agent_talk"
	KindAgentTalkEntry       Kind = "agent_talk_entry"
	KindAgentTalkExpand      Kind = "agent_talk_expand"
	KindAgentTalkSubscribe   Kind = "agent_talk_subscribe"
	KindConnected            Kind = "connected"
	KindDisconnected         Kind = "disconnected"
	KindTaskStatus           Kind = "task_status"
	KindRunStatus            Kind = "run_status"
	KindTaskCancel           Kind = "task_cancel"
	KindMultiTaskControl     Kind = "multi_task_control"
	KindSignInListen         Kind = "sign_in_listen"
	KindSignInListening      Kind = "sign_in_listening"
	KindSignInOpen           Kind = "sign_in_open"
	KindSignInRedirect       Kind = "sign_in_redirect"
	KindSnapshot             Kind = "snapshot"
	KindResync               Kind = "resync"
	KindResyncRequired       Kind = "resync_required"
	KindRestartRequest       Kind = "restart_request"
	KindRestartGet           Kind = "restart_get"
	KindRestartStatus        Kind = "restart_status"
)

type AccountRequest struct {
	AccountID *string `json:"account_id,omitempty"`
	Operation *string `json:"operation,omitempty"`
	Provider  *string `json:"provider,omitempty"`
	Purpose   *string `json:"purpose,omitempty"`
}
type AccountResult struct {
	AccountID *string         `json:"account_id,omitempty"`
	Operation *string         `json:"operation,omitempty"`
	Status    *string         `json:"status,omitempty"`
	Stream    *StreamMetadata `json:"stream,omitempty"`
	Text      *string         `json:"text,omitempty"`
}
type Agent struct {
	AgentID string  `json:"agent_id"`
	Name    string  `json:"name"`
	Preset  *string `json:"preset"`
}

// AgentRequest: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type AgentRequest struct {
}

// AgentResult: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type AgentResult struct {
}

// AgentTalk: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type AgentTalk struct {
}
type AgentTalkEntry struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}

// AgentTalkExpand: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type AgentTalkExpand struct {
}

// AgentTalkGet: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type AgentTalkGet struct {
}

// AgentTalkSubscribe: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type AgentTalkSubscribe struct {
}
type ApprovalAnswer struct {
	Choice ApprovalChoice `json:"choice"`
}
type ApprovalChoice string

const (
	ApprovalChoiceOnce        ApprovalChoice = "once"
	ApprovalChoiceDeny        ApprovalChoice = "deny"
	ApprovalChoiceAlwaysAllow ApprovalChoice = "always_allow"
)

type ApprovalRequest struct {
	Choices []ApprovalChoice `json:"choices"`
	Stream  *StreamMetadata  `json:"stream,omitempty"`
}
type BackgroundNotice struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type Capability struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ChannelRequest: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type ChannelRequest struct {
}

// ChannelResult: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type ChannelResult struct {
}
type ChannelState struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type Connected struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type ConversationSpeaker string

const (
	ConversationSpeakerPerson ConversationSpeaker = "person"
	ConversationSpeakerAgent  ConversationSpeaker = "agent"
	ConversationSpeakerSystem ConversationSpeaker = "system"
)

type ConversationStart struct {
	AgentID string `json:"agent_id"`
}

// Details: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type Details struct {
}

// DetailsGet: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type DetailsGet struct {
}
type Disconnected struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type EffectState string

const (
	EffectNone      EffectState = "none"
	EffectRefused   EffectState = "refused"
	EffectFailed    EffectState = "failed"
	EffectApplied   EffectState = "applied"
	EffectCancelled EffectState = "cancelled"
	EffectUnknown   EffectState = "unknown"
)

type Error struct {
	Code   ErrorCode `json:"code"`
	Detail string    `json:"detail"`
}
type ErrorCode string

const (
	ErrorUnsupportedCombination  ErrorCode = "unsupported_combination"
	ErrorCapabilityNotNegotiated ErrorCode = "capability_not_negotiated"
	ErrorMalformed               ErrorCode = "malformed"
	ErrorStaleEpoch              ErrorCode = "stale_epoch"
	ErrorStaleRevision           ErrorCode = "stale_revision"
	ErrorNotAuthorized           ErrorCode = "not_authorized"
	ErrorOutOfScope              ErrorCode = "out_of_scope"
	ErrorDuplicate               ErrorCode = "duplicate"
	ErrorUnavailable             ErrorCode = "unavailable"
)

type ErrorPayload struct {
}

// FollowUp: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type FollowUp struct {
}
type FormField struct {
	Choices *[]string     `json:"choices,omitempty"`
	Name    string        `json:"name"`
	Type    FormFieldType `json:"type"`
}
type FormFieldType string

const (
	FormFieldTypeText    FormFieldType = "text"
	FormFieldTypeNumber  FormFieldType = "number"
	FormFieldTypeBoolean FormFieldType = "boolean"
	FormFieldTypeChoice  FormFieldType = "choice"
)

type FormValue struct {
	String  *string
	Number  *json.Number
	Boolean *bool
}

func (v FormValue) MarshalJSON() ([]byte, error) {
	var value any
	count := 0
	if v.String != nil {
		value = v.String
		count++
	}
	if v.Number != nil {
		value = v.Number
		count++
	}
	if v.Boolean != nil {
		value = v.Boolean
		count++
	}
	if count != 1 {
		return nil, fmt.Errorf("union requires exactly one value")
	}
	return json.Marshal(value)
}
func (v *FormValue) UnmarshalJSON(data []byte) error {
	if clientSchema.Defs["form_value"].OneOf[0].Validate(data, clientSchema) == nil {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*v = FormValue{String: &value}
		return nil
	}
	if clientSchema.Defs["form_value"].OneOf[1].Validate(data, clientSchema) == nil {
		var value json.Number
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*v = FormValue{Number: &value}
		return nil
	}
	if clientSchema.Defs["form_value"].OneOf[2].Validate(data, clientSchema) == nil {
		var value bool
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*v = FormValue{Boolean: &value}
		return nil
	}
	return fmt.Errorf("invalid union value")
}

type FormValues map[string]FormValue
type Hello struct {
	Capabilities     []Capability `json:"capabilities"`
	ID               string       `json:"id"`
	Party            Party        `json:"party"`
	Peer             *string      `json:"peer,omitempty"`
	Platform         *string      `json:"platform,omitempty"`
	ProductVersion   string       `json:"product_version"`
	ProtocolVersions []string     `json:"protocol_versions"`
}
type History struct {
	AgentID        *string   `json:"agent_id,omitempty"`
	ConversationID *string   `json:"conversation_id,omitempty"`
	Snapshot       *Snapshot `json:"snapshot,omitempty"`
}
type HistoryGet struct {
	AgentID        *string `json:"agent_id,omitempty"`
	ConversationID *string `json:"conversation_id,omitempty"`
}
type Input struct {
	ConversationID *string    `json:"conversation_id,omitempty"`
	InputID        *string    `json:"input_id,omitempty"`
	Mode           *InputMode `json:"mode,omitempty"`
	Text           *string    `json:"text,omitempty"`
}
type InputMode string

const (
	InputModeSteer InputMode = "steer"
	InputModeQueue InputMode = "queue"
)

type Interrupt struct {
	ConversationID *string `json:"conversation_id,omitempty"`
}
type ItemDone struct {
	AgentID  *string              `json:"agent_id,omitempty"`
	EchoKey  *string              `json:"echo_key,omitempty"`
	PersonID *string              `json:"person_id,omitempty"`
	Speaker  *ConversationSpeaker `json:"speaker,omitempty"`
	Stream   *StreamMetadata      `json:"stream,omitempty"`
	Text     *string              `json:"text,omitempty"`
}

// MemoryImportProgress: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type MemoryImportProgress struct {
}

// MemoryPromotionBatch: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type MemoryPromotionBatch struct {
}

// MemoryRequest: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type MemoryRequest struct {
}

// MemoryResult: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type MemoryResult struct {
}

// MultiTaskControl: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type MultiTaskControl struct {
}
type OutputDelta struct {
	AgentID *string         `json:"agent_id,omitempty"`
	Stream  *StreamMetadata `json:"stream,omitempty"`
	Text    *string         `json:"text,omitempty"`
}
type Party string

const (
	PartyCore   Party = "core"
	PartyClient Party = "client"
	PartyNode   Party = "node"
)

// Progress: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type Progress struct {
}

// ProgressDigest: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type ProgressDigest struct {
}

// ProgressDigestGet: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type ProgressDigestGet struct {
}

// ProgressGet: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type ProgressGet struct {
}
type ProgressUpdate struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type Protocol struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type Question struct {
	ConversationID string          `json:"conversation_id"`
	Form           *QuestionForm   `json:"form,omitempty"`
	Stream         *StreamMetadata `json:"stream,omitempty"`
}
type QuestionAnswer struct {
	Answer QuestionAnswerAction `json:"answer"`
	Values *FormValues          `json:"values,omitempty"`
}
type QuestionAnswerAction string

const (
	QuestionAnswerActionAccept  QuestionAnswerAction = "accept"
	QuestionAnswerActionDecline QuestionAnswerAction = "decline"
	QuestionAnswerActionCancel  QuestionAnswerAction = "cancel"
)

type QuestionForm struct {
	Fields     []FormField `json:"fields"`
	PluginName string      `json:"plugin_name"`
}
type ReadinessFailure struct {
	Step string `json:"step"`
}
type ReceiptRef struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Revision string `json:"revision"`
}
type RestartGet struct {
}
type RestartMode string

const (
	RestartModeWhenIdle RestartMode = "when_idle"
	RestartModeDrain    RestartMode = "drain"
)

type RestartRequest struct {
	Mode *RestartMode `json:"mode,omitempty"`
}
type RestartStatus struct {
	CommittedEpoch  *string      `json:"committed_epoch,omitempty"`
	DischargedEpoch *string      `json:"discharged_epoch,omitempty"`
	Mode            *RestartMode `json:"mode,omitempty"`
	Owed            bool         `json:"owed"`
	RequestID       *string      `json:"request_id,omitempty"`
	RequestedEpoch  *string      `json:"requested_epoch,omitempty"`
}
type Result struct {
	Body       *ResultBody      `json:"body,omitempty"`
	Provenance ResultProvenance `json:"provenance"`
	ResultID   string           `json:"result_id"`
	RevisedBy  string           `json:"revised_by"`
	Revision   json.Number      `json:"revision"`
	SavedAs    *SavedReference  `json:"saved_as"`
	Status     ResultStatus     `json:"status"`
	Stream     *StreamMetadata  `json:"stream,omitempty"`
	Summary    string           `json:"summary"`
	Text       *string          `json:"text,omitempty"`
	Title      string           `json:"title"`
	Type       ResultType       `json:"type"`
}

// ResultBody: Reserved: docs/design/host-protocol.md#deferred — The JSON Schema of the result bodies, their size limits and the payload fields of the result kinds are set by the implementation issue that ships results.
type ResultBody struct {
}
type ResultEdit struct {
	BaseRevision json.Number `json:"base_revision"`
	Body         ResultBody  `json:"body"`
	ResultID     string      `json:"result_id"`
}
type ResultGet struct {
	ResultID string       `json:"result_id"`
	Revision *json.Number `json:"revision,omitempty"`
}
type ResultProvenance struct {
	AgentID        string  `json:"agent_id"`
	ConversationID string  `json:"conversation_id"`
	TaskID         *string `json:"task_id"`
	TurnID         string  `json:"turn_id"`
}
type ResultSave struct {
	Destination SaveDestination `json:"destination"`
	ResultID    string          `json:"result_id"`
	Revision    json.Number     `json:"revision"`
}
type ResultStatus string

const (
	ResultStatusPreview ResultStatus = "preview"
	ResultStatusSaved   ResultStatus = "saved"
)

type ResultType string

const (
	ResultTypeSummary       ResultType = "summary"
	ResultTypeTable         ResultType = "table"
	ResultTypeDiagram       ResultType = "diagram"
	ResultTypeChart         ResultType = "chart"
	ResultTypeDocumentDraft ResultType = "document_draft"
	ResultTypeComparison    ResultType = "comparison"
	ResultTypePlanView      ResultType = "plan_view"
	ResultTypeFile          ResultType = "file"
)

type Resync struct {
	Cursor StreamCursor `json:"cursor"`
	Topic  string       `json:"topic"`
}
type ResyncRequired struct {
	Cursor StreamCursor `json:"cursor"`
	Topic  string       `json:"topic"`
}
type RunStatus struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}

// Running: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type Running struct {
}

// RunningGet: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type RunningGet struct {
}
type RunningUpdate struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}

// SaveDestination: Reserved: docs/design/host-protocol.md#deferred — The JSON Schema of the result bodies, their size limits and the payload fields of the result kinds are set by the implementation issue that ships results.
type SaveDestination struct {
}

// SavedReference: Reserved: docs/design/host-protocol.md#deferred — The JSON Schema of the result bodies, their size limits and the payload fields of the result kinds are set by the implementation issue that ships results.
type SavedReference struct {
}
type Sender struct {
	ID    string `json:"id"`
	Party Party  `json:"party"`
}

// SignInListen: Request a listener for the fixed /auth/callback path before opening the browser.
type SignInListen struct {
}
type SignInListening struct {
	CannotListen *string      `json:"cannot_listen,omitempty"`
	Port         *json.Number `json:"port,omitempty"`
}
type SignInOpen struct {
	URL *string `json:"url,omitempty"`
}
type SignInRedirect struct {
	Code             *string `json:"code,omitempty"`
	Error            *string `json:"error,omitempty"`
	ErrorDescription *string `json:"error_description,omitempty"`
	ErrorURI         *string `json:"error_uri,omitempty"`
	Iss              *string `json:"iss,omitempty"`
	State            *string `json:"state,omitempty"`
	URL              *string `json:"url,omitempty"`
}
type Snapshot struct {
	Cursor          StreamCursor `json:"cursor"`
	Items           []StreamItem `json:"items"`
	RevocationEpoch string       `json:"revocation_epoch"`
	Topic           string       `json:"topic"`
}
type SourceRef struct {
	ID         string  `json:"id"`
	Locator    string  `json:"locator"`
	Namespace  string  `json:"namespace"`
	ObservedAt string  `json:"observed_at"`
	ObservedBy *string `json:"observed_by"`
	Origin     string  `json:"origin"`
	Revision   string  `json:"revision"`
}

// Stop: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type Stop struct {
}
type StreamCursor struct {
	Epoch string      `json:"epoch"`
	Seq   json.Number `json:"seq"`
}
type StreamItem struct {
	Key      string          `json:"key"`
	Kind     Kind            `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
	Revision json.Number     `json:"revision"`
}
type StreamMetadata struct {
	Cursor   StreamCursor `json:"cursor"`
	Key      string       `json:"key"`
	Revision json.Number  `json:"revision"`
	Topic    string       `json:"topic"`
}
type Subscribe struct {
	Active *bool         `json:"active,omitempty"`
	After  *StreamCursor `json:"after,omitempty"`
	Topic  *string       `json:"topic,omitempty"`
}

// TaskCancel: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type TaskCancel struct {
}
type TaskStatus struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type TurnState struct {
	Active  *bool           `json:"active,omitempty"`
	AgentID *string         `json:"agent_id,omitempty"`
	Stream  *StreamMetadata `json:"stream,omitempty"`
	Text    *string         `json:"text,omitempty"`
}

// Waiting: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type Waiting struct {
}

// WaitingAnswer: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type WaitingAnswer struct {
}

// WaitingGet: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type WaitingGet struct {
}
type WaitingUpdate struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type Welcome struct {
	Agents          []Agent      `json:"agents"`
	Capabilities    []Capability `json:"capabilities"`
	Epoch           string       `json:"epoch"`
	MainID          string       `json:"main_id"`
	PersonID        string       `json:"person_id"`
	ProtocolVersion string       `json:"protocol_version"`
}

// WorkRequest: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type WorkRequest struct {
}

// WorkResult: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type WorkResult struct {
}

// Payload is the closed set of client 1.0 payload types.
type Payload interface{ isPayload() }

func (Hello) isPayload()                {}
func (Welcome) isPayload()              {}
func (ErrorPayload) isPayload()         {}
func (ConversationStart) isPayload()    {}
func (Input) isPayload()                {}
func (Interrupt) isPayload()            {}
func (HistoryGet) isPayload()           {}
func (History) isPayload()              {}
func (Subscribe) isPayload()            {}
func (OutputDelta) isPayload()          {}
func (ItemDone) isPayload()             {}
func (TurnState) isPayload()            {}
func (ApprovalRequest) isPayload()      {}
func (ApprovalAnswer) isPayload()       {}
func (Question) isPayload()             {}
func (QuestionAnswer) isPayload()       {}
func (Result) isPayload()               {}
func (ResultGet) isPayload()            {}
func (ResultEdit) isPayload()           {}
func (ResultSave) isPayload()           {}
func (RunningGet) isPayload()           {}
func (Running) isPayload()              {}
func (RunningUpdate) isPayload()        {}
func (Stop) isPayload()                 {}
func (FollowUp) isPayload()             {}
func (BackgroundNotice) isPayload()     {}
func (ProgressGet) isPayload()          {}
func (Progress) isPayload()             {}
func (ProgressUpdate) isPayload()       {}
func (ProgressDigestGet) isPayload()    {}
func (ProgressDigest) isPayload()       {}
func (WaitingGet) isPayload()           {}
func (Waiting) isPayload()              {}
func (WaitingUpdate) isPayload()        {}
func (WaitingAnswer) isPayload()        {}
func (DetailsGet) isPayload()           {}
func (Details) isPayload()              {}
func (ReadinessFailure) isPayload()     {}
func (AgentRequest) isPayload()         {}
func (AgentResult) isPayload()          {}
func (WorkRequest) isPayload()          {}
func (WorkResult) isPayload()           {}
func (AccountRequest) isPayload()       {}
func (AccountResult) isPayload()        {}
func (MemoryRequest) isPayload()        {}
func (MemoryResult) isPayload()         {}
func (MemoryImportProgress) isPayload() {}
func (MemoryPromotionBatch) isPayload() {}
func (ChannelRequest) isPayload()       {}
func (ChannelResult) isPayload()        {}
func (ChannelState) isPayload()         {}
func (AgentTalkGet) isPayload()         {}
func (AgentTalk) isPayload()            {}
func (AgentTalkEntry) isPayload()       {}
func (AgentTalkExpand) isPayload()      {}
func (AgentTalkSubscribe) isPayload()   {}
func (Connected) isPayload()            {}
func (Disconnected) isPayload()         {}
func (TaskStatus) isPayload()           {}
func (RunStatus) isPayload()            {}
func (TaskCancel) isPayload()           {}
func (MultiTaskControl) isPayload()     {}
func (SignInListen) isPayload()         {}
func (SignInListening) isPayload()      {}
func (SignInOpen) isPayload()           {}
func (SignInRedirect) isPayload()       {}
func (Snapshot) isPayload()             {}
func (Resync) isPayload()               {}
func (ResyncRequired) isPayload()       {}
func (RestartRequest) isPayload()       {}
func (RestartGet) isPayload()           {}
func (RestartStatus) isPayload()        {}

type message[P any] struct {
	AttemptID      string      `json:"attempt_id"`
	EffectState    EffectState `json:"effect_state"`
	Epoch          string      `json:"epoch"`
	Error          *Error      `json:"error"`
	IdempotencyKey *string     `json:"idempotency_key"`
	Kind           Kind        `json:"kind"`
	MessageID      string      `json:"message_id"`
	Payload        P           `json:"payload"`
	Protocol       Protocol    `json:"protocol"`
	ReceiptRef     *ReceiptRef `json:"receipt_ref"`
	ReplyTo        *string     `json:"reply_to"`
	Sender         Sender      `json:"sender"`
	SentAt         string      `json:"sent_at"`
	SourceRefs     []SourceRef `json:"source_refs"`
}

// Envelope preserves required nulls and arrays; its codec validates the schema.
type Envelope message[Payload]

func decodePayload(kind Kind, data json.RawMessage) (Payload, error) {
	switch kind {
	case KindHello:
		var p Hello
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWelcome:
		var p Welcome
		err := json.Unmarshal(data, &p)
		return p, err
	case KindError:
		var p ErrorPayload
		err := json.Unmarshal(data, &p)
		return p, err
	case KindConversationStart:
		var p ConversationStart
		err := json.Unmarshal(data, &p)
		return p, err
	case KindInput:
		var p Input
		err := json.Unmarshal(data, &p)
		return p, err
	case KindInterrupt:
		var p Interrupt
		err := json.Unmarshal(data, &p)
		return p, err
	case KindHistoryGet:
		var p HistoryGet
		err := json.Unmarshal(data, &p)
		return p, err
	case KindHistory:
		var p History
		err := json.Unmarshal(data, &p)
		return p, err
	case KindSubscribe:
		var p Subscribe
		err := json.Unmarshal(data, &p)
		return p, err
	case KindOutputDelta:
		var p OutputDelta
		err := json.Unmarshal(data, &p)
		return p, err
	case KindItemDone:
		var p ItemDone
		err := json.Unmarshal(data, &p)
		return p, err
	case KindTurnState:
		var p TurnState
		err := json.Unmarshal(data, &p)
		return p, err
	case KindApprovalRequest:
		var p ApprovalRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindApprovalAnswer:
		var p ApprovalAnswer
		err := json.Unmarshal(data, &p)
		return p, err
	case KindQuestion:
		var p Question
		err := json.Unmarshal(data, &p)
		return p, err
	case KindQuestionAnswer:
		var p QuestionAnswer
		err := json.Unmarshal(data, &p)
		return p, err
	case KindResult:
		var p Result
		err := json.Unmarshal(data, &p)
		return p, err
	case KindResultGet:
		var p ResultGet
		err := json.Unmarshal(data, &p)
		return p, err
	case KindResultEdit:
		var p ResultEdit
		err := json.Unmarshal(data, &p)
		return p, err
	case KindResultSave:
		var p ResultSave
		err := json.Unmarshal(data, &p)
		return p, err
	case KindRunningGet:
		var p RunningGet
		err := json.Unmarshal(data, &p)
		return p, err
	case KindRunning:
		var p Running
		err := json.Unmarshal(data, &p)
		return p, err
	case KindRunningUpdate:
		var p RunningUpdate
		err := json.Unmarshal(data, &p)
		return p, err
	case KindStop:
		var p Stop
		err := json.Unmarshal(data, &p)
		return p, err
	case KindFollowUp:
		var p FollowUp
		err := json.Unmarshal(data, &p)
		return p, err
	case KindBackgroundNotice:
		var p BackgroundNotice
		err := json.Unmarshal(data, &p)
		return p, err
	case KindProgressGet:
		var p ProgressGet
		err := json.Unmarshal(data, &p)
		return p, err
	case KindProgress:
		var p Progress
		err := json.Unmarshal(data, &p)
		return p, err
	case KindProgressUpdate:
		var p ProgressUpdate
		err := json.Unmarshal(data, &p)
		return p, err
	case KindProgressDigestGet:
		var p ProgressDigestGet
		err := json.Unmarshal(data, &p)
		return p, err
	case KindProgressDigest:
		var p ProgressDigest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWaitingGet:
		var p WaitingGet
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWaiting:
		var p Waiting
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWaitingUpdate:
		var p WaitingUpdate
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWaitingAnswer:
		var p WaitingAnswer
		err := json.Unmarshal(data, &p)
		return p, err
	case KindDetailsGet:
		var p DetailsGet
		err := json.Unmarshal(data, &p)
		return p, err
	case KindDetails:
		var p Details
		err := json.Unmarshal(data, &p)
		return p, err
	case KindReadinessFailure:
		var p ReadinessFailure
		err := json.Unmarshal(data, &p)
		return p, err
	case KindAgentRequest:
		var p AgentRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindAgentResult:
		var p AgentResult
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWorkRequest:
		var p WorkRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWorkResult:
		var p WorkResult
		err := json.Unmarshal(data, &p)
		return p, err
	case KindAccountRequest:
		var p AccountRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindAccountResult:
		var p AccountResult
		err := json.Unmarshal(data, &p)
		return p, err
	case KindMemoryRequest:
		var p MemoryRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindMemoryResult:
		var p MemoryResult
		err := json.Unmarshal(data, &p)
		return p, err
	case KindMemoryImportProgress:
		var p MemoryImportProgress
		err := json.Unmarshal(data, &p)
		return p, err
	case KindMemoryPromotionBatch:
		var p MemoryPromotionBatch
		err := json.Unmarshal(data, &p)
		return p, err
	case KindChannelRequest:
		var p ChannelRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindChannelResult:
		var p ChannelResult
		err := json.Unmarshal(data, &p)
		return p, err
	case KindChannelState:
		var p ChannelState
		err := json.Unmarshal(data, &p)
		return p, err
	case KindAgentTalkGet:
		var p AgentTalkGet
		err := json.Unmarshal(data, &p)
		return p, err
	case KindAgentTalk:
		var p AgentTalk
		err := json.Unmarshal(data, &p)
		return p, err
	case KindAgentTalkEntry:
		var p AgentTalkEntry
		err := json.Unmarshal(data, &p)
		return p, err
	case KindAgentTalkExpand:
		var p AgentTalkExpand
		err := json.Unmarshal(data, &p)
		return p, err
	case KindAgentTalkSubscribe:
		var p AgentTalkSubscribe
		err := json.Unmarshal(data, &p)
		return p, err
	case KindConnected:
		var p Connected
		err := json.Unmarshal(data, &p)
		return p, err
	case KindDisconnected:
		var p Disconnected
		err := json.Unmarshal(data, &p)
		return p, err
	case KindTaskStatus:
		var p TaskStatus
		err := json.Unmarshal(data, &p)
		return p, err
	case KindRunStatus:
		var p RunStatus
		err := json.Unmarshal(data, &p)
		return p, err
	case KindTaskCancel:
		var p TaskCancel
		err := json.Unmarshal(data, &p)
		return p, err
	case KindMultiTaskControl:
		var p MultiTaskControl
		err := json.Unmarshal(data, &p)
		return p, err
	case KindSignInListen:
		var p SignInListen
		err := json.Unmarshal(data, &p)
		return p, err
	case KindSignInListening:
		var p SignInListening
		err := json.Unmarshal(data, &p)
		return p, err
	case KindSignInOpen:
		var p SignInOpen
		err := json.Unmarshal(data, &p)
		return p, err
	case KindSignInRedirect:
		var p SignInRedirect
		err := json.Unmarshal(data, &p)
		return p, err
	case KindSnapshot:
		var p Snapshot
		err := json.Unmarshal(data, &p)
		return p, err
	case KindResync:
		var p Resync
		err := json.Unmarshal(data, &p)
		return p, err
	case KindResyncRequired:
		var p ResyncRequired
		err := json.Unmarshal(data, &p)
		return p, err
	case KindRestartRequest:
		var p RestartRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindRestartGet:
		var p RestartGet
		err := json.Unmarshal(data, &p)
		return p, err
	case KindRestartStatus:
		var p RestartStatus
		err := json.Unmarshal(data, &p)
		return p, err
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
}
func decodeMessage(data []byte) (Envelope, error) {
	var wire message[json.RawMessage]
	if err := json.Unmarshal(data, &wire); err != nil {
		return Envelope{}, err
	}
	payload, err := decodePayload(wire.Kind, wire.Payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		AttemptID:      wire.AttemptID,
		EffectState:    wire.EffectState,
		Epoch:          wire.Epoch,
		Error:          wire.Error,
		IdempotencyKey: wire.IdempotencyKey,
		Kind:           wire.Kind,
		MessageID:      wire.MessageID,
		Payload:        payload,
		Protocol:       wire.Protocol,
		ReceiptRef:     wire.ReceiptRef,
		ReplyTo:        wire.ReplyTo,
		Sender:         wire.Sender,
		SentAt:         wire.SentAt,
		SourceRefs:     wire.SourceRefs,
	}, nil
}

const clientSchemaJSON = "{\"$comment\":\"SPDX-License-Identifier: Apache-2.0\",\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"title\":\"Client 1.0 envelope\",\"description\":\"Client 1.0 kinds and specified payload fields. Reserved objects have no wire fields until the implementation issue named in their $comment defines them.\",\"type\":\"object\",\"properties\":{\"protocol\":{\"$ref\":\"#/$defs/protocol\"},\"kind\":{\"type\":\"string\",\"enum\":[\"hello\",\"welcome\",\"error\",\"conversation_start\",\"input\",\"interrupt\",\"history_get\",\"history\",\"subscribe\",\"output_delta\",\"item_done\",\"turn_state\",\"approval_request\",\"approval_answer\",\"question\",\"question_answer\",\"result\",\"result_get\",\"result_edit\",\"result_save\",\"running_get\",\"running\",\"running_update\",\"stop\",\"follow_up\",\"background_notice\",\"progress_get\",\"progress\",\"progress_update\",\"progress_digest_get\",\"progress_digest\",\"waiting_get\",\"waiting\",\"waiting_update\",\"waiting_answer\",\"details_get\",\"details\",\"readiness_failure\",\"agent_request\",\"agent_result\",\"work_request\",\"work_result\",\"account_request\",\"account_result\",\"memory_request\",\"memory_result\",\"memory_import_progress\",\"memory_promotion_batch\",\"channel_request\",\"channel_result\",\"channel_state\",\"agent_talk_get\",\"agent_talk\",\"agent_talk_entry\",\"agent_talk_expand\",\"agent_talk_subscribe\",\"connected\",\"disconnected\",\"task_status\",\"run_status\",\"task_cancel\",\"multi_task_control\",\"sign_in_listen\",\"sign_in_listening\",\"sign_in_open\",\"sign_in_redirect\",\"snapshot\",\"resync\",\"resync_required\",\"restart_request\",\"restart_get\",\"restart_status\"]},\"message_id\":{\"type\":\"string\"},\"attempt_id\":{\"type\":\"string\"},\"reply_to\":{\"anyOf\":[{\"type\":\"string\"},{\"type\":\"null\"}]},\"sender\":{\"$ref\":\"#/$defs/sender\"},\"epoch\":{\"$ref\":\"#/$defs/epoch\"},\"sent_at\":{\"$ref\":\"#/$defs/utc\"},\"idempotency_key\":{\"anyOf\":[{\"type\":\"string\"},{\"type\":\"null\"}]},\"effect_state\":{\"$ref\":\"#/$defs/effect_state\"},\"receipt_ref\":{\"anyOf\":[{\"$ref\":\"#/$defs/receipt_ref\"},{\"type\":\"null\"}]},\"source_refs\":{\"type\":\"array\",\"items\":{\"$ref\":\"#/$defs/source_ref\"}},\"error\":{\"anyOf\":[{\"$ref\":\"#/$defs/error\"},{\"type\":\"null\"}]},\"payload\":{\"type\":\"object\"}},\"required\":[\"protocol\",\"kind\",\"message_id\",\"attempt_id\",\"reply_to\",\"sender\",\"epoch\",\"sent_at\",\"idempotency_key\",\"effect_state\",\"receipt_ref\",\"source_refs\",\"error\",\"payload\"],\"additionalProperties\":false,\"allOf\":[{\"if\":{\"properties\":{\"kind\":{\"const\":\"hello\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/hello\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"welcome\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/welcome\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"error\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/error_payload\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"conversation_start\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/conversation_start\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"input\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/input\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"interrupt\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/interrupt\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"history_get\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/history_get\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"history\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/history\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"subscribe\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/subscribe\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"output_delta\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/output_delta\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"item_done\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/item_done\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"turn_state\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/turn_state\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"approval_request\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/approval_request\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"approval_answer\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/approval_answer\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"question\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/question\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"question_answer\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/question_answer\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"result\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/result\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"result_get\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/result_get\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"result_edit\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/result_edit\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"result_save\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/result_save\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"running_get\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/running_get\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"running\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/running\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"running_update\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/running_update\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"stop\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/stop\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"follow_up\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/follow_up\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"background_notice\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/background_notice\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"progress_get\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/progress_get\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"progress\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/progress\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"progress_update\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/progress_update\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"progress_digest_get\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/progress_digest_get\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"progress_digest\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/progress_digest\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"waiting_get\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/waiting_get\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"waiting\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/waiting\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"waiting_update\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/waiting_update\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"waiting_answer\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/waiting_answer\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"details_get\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/details_get\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"details\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/details\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"readiness_failure\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/readiness_failure\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"agent_request\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/agent_request\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"agent_result\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/agent_result\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"work_request\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/work_request\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"work_result\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/work_result\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"account_request\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/account_request\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"account_result\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/account_result\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"memory_request\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/memory_request\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"memory_result\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/memory_result\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"memory_import_progress\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/memory_import_progress\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"memory_promotion_batch\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/memory_promotion_batch\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"channel_request\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/channel_request\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"channel_result\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/channel_result\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"channel_state\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/channel_state\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"agent_talk_get\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/agent_talk_get\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"agent_talk\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/agent_talk\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"agent_talk_entry\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/agent_talk_entry\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"agent_talk_expand\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/agent_talk_expand\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"agent_talk_subscribe\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/agent_talk_subscribe\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"connected\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/connected\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"disconnected\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/disconnected\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"task_status\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/task_status\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"run_status\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/run_status\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"task_cancel\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/task_cancel\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"multi_task_control\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/multi_task_control\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"sign_in_listen\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/sign_in_listen\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"sign_in_listening\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/sign_in_listening\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"sign_in_open\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/sign_in_open\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"sign_in_redirect\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/sign_in_redirect\"}}}},{\"if\":{\"anyOf\":[{\"properties\":{\"kind\":{\"const\":\"error\"}}},{\"properties\":{\"effect_state\":{\"enum\":[\"refused\",\"failed\"]}}}]},\"then\":{\"properties\":{\"error\":{\"$ref\":\"#/$defs/error\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"result_save\"}}},\"then\":{\"properties\":{\"idempotency_key\":{\"type\":\"string\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"snapshot\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/snapshot\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"resync\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/resync\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"resync_required\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/resync_required\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"restart_request\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/restart_request\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"restart_get\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/restart_get\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"restart_status\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/restart_status\"}}}}],\"$defs\":{\"version\":{\"type\":\"string\",\"pattern\":\"^(0|[1-9][0-9]*)\\\\.(0|[1-9][0-9]*)$\",\"not\":{\"pattern\":\"\\\\s\"}},\"epoch\":{\"type\":\"string\",\"pattern\":\"^(0|[1-9][0-9]{0,18}|1[0-7][0-9]{18}|18[0-3][0-9]{17}|184[0-3][0-9]{16}|1844[0-5][0-9]{15}|18446[0-6][0-9]{14}|184467[0-3][0-9]{13}|1844674[0-3][0-9]{12}|184467440[0-6][0-9]{10}|1844674407[0-2][0-9]{9}|18446744073[0-6][0-9]{8}|1844674407370[0-8][0-9]{6}|18446744073709[0-4][0-9]{5}|184467440737095[0-4][0-9]{4}|18446744073709550[0-9]{3}|18446744073709551[0-5][0-9]{2}|1844674407370955160[0-9]{1}|1844674407370955161[0-4]|18446744073709551615)$\",\"not\":{\"pattern\":\"\\\\s\"}},\"utc\":{\"type\":\"string\",\"format\":\"date-time\",\"pattern\":\"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\\\.[0-9]+)?(Z|\\\\+00:00)$\",\"not\":{\"pattern\":\"\\\\s\"}},\"party\":{\"type\":\"string\",\"enum\":[\"core\",\"client\",\"node\"]},\"effect_state\":{\"type\":\"string\",\"enum\":[\"none\",\"refused\",\"failed\",\"applied\",\"cancelled\",\"unknown\"],\"x-go-prefix\":\"Effect\"},\"error_code\":{\"type\":\"string\",\"enum\":[\"unsupported_combination\",\"capability_not_negotiated\",\"malformed\",\"stale_epoch\",\"stale_revision\",\"not_authorized\",\"out_of_scope\",\"duplicate\",\"unavailable\"],\"x-go-prefix\":\"Error\"},\"protocol\":{\"type\":\"object\",\"properties\":{\"name\":{\"const\":\"client\"},\"version\":{\"const\":\"1.0\"}},\"required\":[\"name\",\"version\"],\"additionalProperties\":false},\"sender\":{\"type\":\"object\",\"properties\":{\"party\":{\"$ref\":\"#/$defs/party\"},\"id\":{\"type\":\"string\"}},\"required\":[\"party\",\"id\"],\"additionalProperties\":false},\"receipt_ref\":{\"type\":\"object\",\"properties\":{\"kind\":{\"const\":\"receipt\"},\"id\":{\"type\":\"string\"},\"revision\":{\"type\":\"string\"}},\"required\":[\"kind\",\"id\",\"revision\"],\"additionalProperties\":false},\"source_ref\":{\"type\":\"object\",\"properties\":{\"origin\":{\"type\":\"string\"},\"namespace\":{\"type\":\"string\"},\"id\":{\"type\":\"string\"},\"revision\":{\"type\":\"string\"},\"locator\":{\"type\":\"string\"},\"observed_at\":{\"$ref\":\"#/$defs/utc\"},\"observed_by\":{\"anyOf\":[{\"type\":\"string\"},{\"type\":\"null\"}]}},\"required\":[\"origin\",\"namespace\",\"id\",\"revision\",\"locator\",\"observed_at\",\"observed_by\"],\"additionalProperties\":false},\"error\":{\"type\":\"object\",\"properties\":{\"code\":{\"$ref\":\"#/$defs/error_code\"},\"detail\":{\"type\":\"string\"}},\"required\":[\"code\",\"detail\"],\"additionalProperties\":false},\"capability\":{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"},\"version\":{\"$ref\":\"#/$defs/version\"}},\"required\":[\"name\",\"version\"],\"additionalProperties\":false},\"agent\":{\"type\":\"object\",\"properties\":{\"agent_id\":{\"type\":\"string\"},\"name\":{\"type\":\"string\"},\"preset\":{\"anyOf\":[{\"type\":\"string\"},{\"type\":\"null\"}]}},\"required\":[\"agent_id\",\"name\",\"preset\"],\"additionalProperties\":false},\"hello\":{\"type\":\"object\",\"properties\":{\"party\":{\"const\":\"client\",\"x-go-type\":\"Party\"},\"id\":{\"type\":\"string\"},\"product_version\":{\"type\":\"string\"},\"protocol_versions\":{\"type\":\"array\",\"items\":{\"$ref\":\"#/$defs/version\"},\"minItems\":1},\"capabilities\":{\"type\":\"array\",\"items\":{\"$ref\":\"#/$defs/capability\"}},\"peer\":{\"type\":\"string\"},\"platform\":{\"type\":\"string\"}},\"required\":[\"party\",\"id\",\"product_version\",\"protocol_versions\",\"capabilities\"],\"additionalProperties\":false},\"welcome\":{\"type\":\"object\",\"properties\":{\"protocol_version\":{\"const\":\"1.0\"},\"capabilities\":{\"type\":\"array\",\"items\":{\"$ref\":\"#/$defs/capability\"}},\"main_id\":{\"type\":\"string\"},\"epoch\":{\"$ref\":\"#/$defs/epoch\"},\"person_id\":{\"type\":\"string\"},\"agents\":{\"type\":\"array\",\"items\":{\"$ref\":\"#/$defs/agent\"}}},\"required\":[\"protocol_version\",\"capabilities\",\"main_id\",\"epoch\",\"person_id\",\"agents\"],\"additionalProperties\":false},\"error_payload\":{\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"conversation_start\":{\"type\":\"object\",\"properties\":{\"agent_id\":{\"type\":\"string\"}},\"required\":[\"agent_id\"],\"additionalProperties\":false},\"input\":{\"type\":\"object\",\"properties\":{\"conversation_id\":{\"type\":\"string\",\"minLength\":1},\"input_id\":{\"type\":\"string\",\"minLength\":1},\"text\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":16384},\"mode\":{\"$ref\":\"#/$defs/input_mode\"}},\"required\":[],\"additionalProperties\":false},\"interrupt\":{\"type\":\"object\",\"properties\":{\"conversation_id\":{\"type\":\"string\",\"minLength\":1}},\"required\":[],\"additionalProperties\":false},\"history_get\":{\"type\":\"object\",\"properties\":{\"agent_id\":{\"type\":\"string\",\"minLength\":1},\"conversation_id\":{\"type\":\"string\",\"minLength\":1}},\"required\":[],\"additionalProperties\":false},\"history\":{\"type\":\"object\",\"properties\":{\"conversation_id\":{\"type\":\"string\",\"minLength\":1},\"snapshot\":{\"$ref\":\"#/$defs/snapshot\"},\"agent_id\":{\"type\":\"string\",\"minLength\":1}},\"required\":[],\"additionalProperties\":false},\"subscribe\":{\"type\":\"object\",\"properties\":{\"topic\":{\"$ref\":\"#/$defs/stream_topic\"},\"after\":{\"$ref\":\"#/$defs/stream_cursor\"},\"active\":{\"type\":\"boolean\"}},\"required\":[],\"additionalProperties\":false},\"output_delta\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"},\"agent_id\":{\"type\":\"string\",\"minLength\":1}},\"required\":[],\"additionalProperties\":false},\"item_done\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"},\"echo_key\":{\"type\":\"string\",\"minLength\":1},\"speaker\":{\"$ref\":\"#/$defs/conversation_speaker\"},\"agent_id\":{\"type\":\"string\",\"minLength\":1},\"person_id\":{\"type\":\"string\",\"minLength\":1}},\"required\":[],\"additionalProperties\":false},\"turn_state\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"},\"active\":{\"type\":\"boolean\"},\"agent_id\":{\"type\":\"string\",\"minLength\":1}},\"required\":[],\"additionalProperties\":false},\"approval_request\":{\"type\":\"object\",\"properties\":{\"choices\":{\"type\":\"array\",\"items\":{\"$ref\":\"#/$defs/approval_choice\"},\"minItems\":1},\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"}},\"required\":[\"choices\"],\"additionalProperties\":false},\"approval_answer\":{\"type\":\"object\",\"properties\":{\"choice\":{\"$ref\":\"#/$defs/approval_choice\"}},\"required\":[\"choice\"],\"additionalProperties\":false},\"question\":{\"type\":\"object\",\"properties\":{\"conversation_id\":{\"type\":\"string\"},\"form\":{\"$ref\":\"#/$defs/question_form\"},\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"}},\"required\":[\"conversation_id\"],\"additionalProperties\":false},\"question_answer\":{\"type\":\"object\",\"properties\":{\"answer\":{\"$ref\":\"#/$defs/question_answer_action\"},\"values\":{\"$ref\":\"#/$defs/form_values\"}},\"required\":[\"answer\"],\"additionalProperties\":false,\"allOf\":[{\"if\":{\"properties\":{\"answer\":{\"const\":\"accept\"}}},\"then\":{\"required\":[\"values\"]},\"else\":{\"not\":{\"required\":[\"values\"]}}}]},\"result\":{\"type\":\"object\",\"properties\":{\"result_id\":{\"type\":\"string\"},\"revision\":{\"$ref\":\"#/$defs/result_revision\"},\"type\":{\"$ref\":\"#/$defs/result_type\"},\"title\":{\"type\":\"string\"},\"summary\":{\"type\":\"string\"},\"text\":{\"type\":\"string\"},\"body\":{\"$ref\":\"#/$defs/result_body\"},\"status\":{\"$ref\":\"#/$defs/result_status\"},\"saved_as\":{\"anyOf\":[{\"$ref\":\"#/$defs/saved_reference\"},{\"type\":\"null\"}]},\"revised_by\":{\"type\":\"string\"},\"provenance\":{\"$ref\":\"#/$defs/result_provenance\"},\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"}},\"required\":[\"result_id\",\"revision\",\"type\",\"title\",\"summary\",\"status\",\"saved_as\",\"revised_by\",\"provenance\"],\"additionalProperties\":false,\"allOf\":[{\"if\":{\"properties\":{\"status\":{\"const\":\"preview\"}}},\"then\":{\"properties\":{\"saved_as\":{\"type\":\"null\"}}},\"else\":{\"properties\":{\"saved_as\":{\"$ref\":\"#/$defs/saved_reference\"}}}},{\"if\":{\"required\":[\"body\"]},\"then\":{\"required\":[\"text\"]}}]},\"result_get\":{\"type\":\"object\",\"properties\":{\"result_id\":{\"type\":\"string\"},\"revision\":{\"$ref\":\"#/$defs/result_revision\"}},\"required\":[\"result_id\"],\"additionalProperties\":false},\"result_edit\":{\"type\":\"object\",\"properties\":{\"result_id\":{\"type\":\"string\"},\"base_revision\":{\"$ref\":\"#/$defs/result_revision\"},\"body\":{\"$ref\":\"#/$defs/result_body\"}},\"required\":[\"result_id\",\"base_revision\",\"body\"],\"additionalProperties\":false},\"result_save\":{\"type\":\"object\",\"properties\":{\"result_id\":{\"type\":\"string\"},\"revision\":{\"$ref\":\"#/$defs/result_revision\"},\"destination\":{\"$ref\":\"#/$defs/save_destination\"}},\"required\":[\"result_id\",\"revision\",\"destination\"],\"additionalProperties\":false},\"running_get\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"running\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"running_update\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"stop\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"follow_up\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"background_notice\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"progress_get\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"progress\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"progress_update\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"progress_digest_get\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"progress_digest\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"waiting_get\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"waiting\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"waiting_update\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"waiting_answer\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"details_get\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"details\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"readiness_failure\":{\"type\":\"object\",\"properties\":{\"step\":{\"type\":\"string\"}},\"required\":[\"step\"],\"additionalProperties\":false},\"agent_request\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"agent_result\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"work_request\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"work_result\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"account_request\":{\"type\":\"object\",\"properties\":{\"operation\":{\"type\":\"string\",\"const\":\"sign_in\"},\"provider\":{\"type\":\"string\",\"const\":\"chatgpt\"},\"purpose\":{\"type\":\"string\",\"enum\":[\"login\",\"provider\"]},\"account_id\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":512}},\"required\":[],\"additionalProperties\":false,\"allOf\":[{\"if\":{\"anyOf\":[{\"required\":[\"operation\"]},{\"required\":[\"provider\"]},{\"required\":[\"purpose\"]},{\"required\":[\"account_id\"]}]},\"then\":{\"required\":[\"operation\",\"provider\",\"purpose\"]}}]},\"account_result\":{\"type\":\"object\",\"properties\":{\"operation\":{\"type\":\"string\",\"const\":\"sign_in\"},\"account_id\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":512},\"status\":{\"type\":\"string\",\"enum\":[\"ok\",\"needs_sign_in\",\"not_eligible\",\"usage_capped\"]},\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false,\"allOf\":[{\"if\":{\"anyOf\":[{\"required\":[\"operation\"]},{\"required\":[\"account_id\"]},{\"required\":[\"status\"]}]},\"then\":{\"required\":[\"operation\",\"account_id\",\"status\"]}}]},\"memory_request\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"memory_result\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"memory_import_progress\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"memory_promotion_batch\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"channel_request\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"channel_result\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"channel_state\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"agent_talk_get\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"agent_talk\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"agent_talk_entry\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"agent_talk_expand\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"agent_talk_subscribe\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"connected\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"disconnected\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"task_status\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"run_status\":{\"type\":\"object\",\"properties\":{\"stream\":{\"$ref\":\"#/$defs/stream_metadata\"},\"text\":{\"type\":\"string\"}},\"required\":[],\"additionalProperties\":false},\"task_cancel\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"multi_task_control\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"sign_in_listen\":{\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false,\"$comment\":\"Request a listener for the fixed /auth/callback path before opening the browser.\"},\"sign_in_listening\":{\"type\":\"object\",\"properties\":{\"port\":{\"type\":\"integer\",\"minimum\":1,\"maximum\":65535},\"cannot_listen\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":256}},\"required\":[],\"additionalProperties\":false,\"allOf\":[{\"oneOf\":[{\"not\":{\"anyOf\":[{\"required\":[\"port\"]},{\"required\":[\"cannot_listen\"]}]}},{\"required\":[\"port\"],\"not\":{\"anyOf\":[{\"required\":[\"cannot_listen\"]}]}},{\"required\":[\"cannot_listen\"],\"not\":{\"anyOf\":[{\"required\":[\"port\"]}]}}]}]},\"sign_in_open\":{\"type\":\"object\",\"properties\":{\"url\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":16384,\"pattern\":\"^https?://[^\\\\s]+$\"}},\"required\":[],\"additionalProperties\":false},\"sign_in_redirect\":{\"type\":\"object\",\"properties\":{\"code\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":4096},\"state\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":512},\"iss\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":2048},\"error\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":256},\"error_description\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":4096},\"error_uri\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":2048},\"url\":{\"type\":\"string\",\"minLength\":1,\"maxLength\":16384}},\"required\":[],\"additionalProperties\":false,\"allOf\":[{\"oneOf\":[{\"not\":{\"anyOf\":[{\"required\":[\"code\"]},{\"required\":[\"state\"]},{\"required\":[\"iss\"]},{\"required\":[\"error\"]},{\"required\":[\"error_description\"]},{\"required\":[\"error_uri\"]},{\"required\":[\"url\"]}]}},{\"required\":[\"url\"],\"not\":{\"anyOf\":[{\"required\":[\"code\"]},{\"required\":[\"state\"]},{\"required\":[\"iss\"]},{\"required\":[\"error\"]},{\"required\":[\"error_description\"]},{\"required\":[\"error_uri\"]}]}},{\"required\":[\"code\",\"state\"],\"not\":{\"anyOf\":[{\"required\":[\"url\"]},{\"required\":[\"error\"]},{\"required\":[\"error_description\"]},{\"required\":[\"error_uri\"]}]}},{\"required\":[\"error\",\"state\"],\"not\":{\"anyOf\":[{\"required\":[\"url\"]},{\"required\":[\"code\"]}]}}]}]},\"approval_choice\":{\"type\":\"string\",\"enum\":[\"once\",\"deny\",\"always_allow\"]},\"form_field_type\":{\"type\":\"string\",\"enum\":[\"text\",\"number\",\"boolean\",\"choice\"]},\"form_field\":{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"},\"type\":{\"$ref\":\"#/$defs/form_field_type\"},\"choices\":{\"type\":\"array\",\"items\":{\"type\":\"string\"},\"minItems\":1}},\"required\":[\"name\",\"type\"],\"additionalProperties\":false,\"allOf\":[{\"if\":{\"properties\":{\"type\":{\"const\":\"choice\"}}},\"then\":{\"required\":[\"choices\"]},\"else\":{\"not\":{\"required\":[\"choices\"]}}}]},\"question_form\":{\"type\":\"object\",\"properties\":{\"plugin_name\":{\"type\":\"string\"},\"fields\":{\"type\":\"array\",\"items\":{\"$ref\":\"#/$defs/form_field\"}}},\"required\":[\"plugin_name\",\"fields\"],\"additionalProperties\":false},\"form_value\":{\"oneOf\":[{\"type\":\"string\"},{\"type\":\"number\"},{\"type\":\"boolean\"}]},\"form_values\":{\"type\":\"object\",\"additionalProperties\":{\"$ref\":\"#/$defs/form_value\"}},\"question_answer_action\":{\"type\":\"string\",\"enum\":[\"accept\",\"decline\",\"cancel\"]},\"result_type\":{\"type\":\"string\",\"enum\":[\"summary\",\"table\",\"diagram\",\"chart\",\"document_draft\",\"comparison\",\"plan_view\",\"file\"]},\"result_status\":{\"type\":\"string\",\"enum\":[\"preview\",\"saved\"]},\"result_revision\":{\"type\":\"integer\",\"minimum\":1},\"result_body\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The JSON Schema of the result bodies, their size limits and the payload fields of the result kinds are set by the implementation issue that ships results.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"saved_reference\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The JSON Schema of the result bodies, their size limits and the payload fields of the result kinds are set by the implementation issue that ships results.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"save_destination\":{\"$comment\":\"Reserved: docs/design/host-protocol.md#deferred — The JSON Schema of the result bodies, their size limits and the payload fields of the result kinds are set by the implementation issue that ships results.\",\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"result_provenance\":{\"type\":\"object\",\"properties\":{\"agent_id\":{\"type\":\"string\"},\"conversation_id\":{\"type\":\"string\"},\"turn_id\":{\"type\":\"string\"},\"task_id\":{\"anyOf\":[{\"type\":\"string\"},{\"type\":\"null\"}]}},\"required\":[\"agent_id\",\"conversation_id\",\"turn_id\",\"task_id\"],\"additionalProperties\":false},\"stream_topic\":{\"type\":\"string\",\"pattern\":\"^(conversation:[\\\\s\\\\S]+|results:[\\\\s\\\\S]+|running|progress|waiting|exchanges|accounts|channels|core)$\"},\"stream_cursor\":{\"type\":\"object\",\"properties\":{\"epoch\":{\"$ref\":\"#/$defs/epoch\"},\"seq\":{\"type\":\"integer\",\"minimum\":0,\"maximum\":18446744073709551615}},\"required\":[\"epoch\",\"seq\"],\"additionalProperties\":false},\"stream_metadata\":{\"type\":\"object\",\"properties\":{\"topic\":{\"$ref\":\"#/$defs/stream_topic\"},\"cursor\":{\"$ref\":\"#/$defs/stream_cursor\"},\"key\":{\"type\":\"string\",\"minLength\":1},\"revision\":{\"type\":\"integer\",\"minimum\":1,\"maximum\":18446744073709551615}},\"required\":[\"topic\",\"cursor\",\"key\",\"revision\"],\"additionalProperties\":false},\"stream_item\":{\"type\":\"object\",\"properties\":{\"kind\":{\"type\":\"string\",\"enum\":[\"output_delta\",\"item_done\",\"turn_state\",\"approval_request\",\"question\",\"result\",\"running_update\",\"progress_update\",\"waiting_update\",\"agent_talk_entry\",\"account_result\",\"channel_state\",\"task_status\",\"run_status\",\"background_notice\",\"connected\",\"disconnected\"],\"x-go-type\":\"Kind\"},\"key\":{\"type\":\"string\",\"minLength\":1},\"revision\":{\"type\":\"integer\",\"minimum\":1,\"maximum\":18446744073709551615},\"payload\":{\"x-go-type\":\"json.RawMessage\"}},\"required\":[\"kind\",\"key\",\"revision\",\"payload\"],\"additionalProperties\":false,\"allOf\":[{\"if\":{\"properties\":{\"kind\":{\"const\":\"output_delta\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/output_delta\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"item_done\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/item_done\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"turn_state\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/turn_state\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"approval_request\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/approval_request\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"question\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/question\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"result\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/result\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"running_update\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/running_update\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"progress_update\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/progress_update\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"waiting_update\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/waiting_update\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"agent_talk_entry\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/agent_talk_entry\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"account_result\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/account_result\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"channel_state\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/channel_state\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"task_status\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/task_status\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"run_status\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/run_status\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"background_notice\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/background_notice\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"connected\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/connected\"}}}},{\"if\":{\"properties\":{\"kind\":{\"const\":\"disconnected\"}}},\"then\":{\"properties\":{\"payload\":{\"$ref\":\"#/$defs/disconnected\"}}}}]},\"snapshot\":{\"type\":\"object\",\"properties\":{\"topic\":{\"$ref\":\"#/$defs/stream_topic\"},\"cursor\":{\"$ref\":\"#/$defs/stream_cursor\"},\"revocation_epoch\":{\"$ref\":\"#/$defs/epoch\"},\"items\":{\"type\":\"array\",\"items\":{\"$ref\":\"#/$defs/stream_item\"}}},\"required\":[\"topic\",\"cursor\",\"revocation_epoch\",\"items\"],\"additionalProperties\":false},\"resync\":{\"type\":\"object\",\"properties\":{\"topic\":{\"$ref\":\"#/$defs/stream_topic\"},\"cursor\":{\"$ref\":\"#/$defs/stream_cursor\"}},\"required\":[\"topic\",\"cursor\"],\"additionalProperties\":false},\"resync_required\":{\"type\":\"object\",\"properties\":{\"topic\":{\"$ref\":\"#/$defs/stream_topic\"},\"cursor\":{\"$ref\":\"#/$defs/stream_cursor\"}},\"required\":[\"topic\",\"cursor\"],\"additionalProperties\":false},\"restart_mode\":{\"type\":\"string\",\"enum\":[\"when_idle\",\"drain\"]},\"restart_request\":{\"type\":\"object\",\"properties\":{\"mode\":{\"$ref\":\"#/$defs/restart_mode\"}},\"required\":[],\"additionalProperties\":false},\"restart_get\":{\"type\":\"object\",\"properties\":{},\"required\":[],\"additionalProperties\":false},\"restart_status\":{\"type\":\"object\",\"properties\":{\"owed\":{\"type\":\"boolean\"},\"request_id\":{\"type\":\"string\",\"pattern\":\"^[0-9a-f]{32}$\"},\"mode\":{\"$ref\":\"#/$defs/restart_mode\"},\"requested_epoch\":{\"$ref\":\"#/$defs/epoch\"},\"committed_epoch\":{\"$ref\":\"#/$defs/epoch\"},\"discharged_epoch\":{\"$ref\":\"#/$defs/epoch\"}},\"required\":[\"owed\"],\"additionalProperties\":false},\"input_mode\":{\"type\":\"string\",\"enum\":[\"steer\",\"queue\"]},\"conversation_speaker\":{\"type\":\"string\",\"enum\":[\"person\",\"agent\",\"system\"]}}}"
