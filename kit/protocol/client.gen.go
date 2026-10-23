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
	KindToolActivity         Kind = "tool_activity"
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
	KindStopResult           Kind = "stop_result"
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
	KindWorkSwitchRequest    Kind = "work_switch_request"
	KindWorkSwitchResult     Kind = "work_switch_result"
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
	KindExportRequest        Kind = "export_request"
	KindExportResult         Kind = "export_result"
	KindReplayRequest        Kind = "replay_request"
	KindReplayResult         Kind = "replay_result"
	KindPlanRequest          Kind = "plan_request"
	KindPlanResponse         Kind = "plan_response"
	KindWebRequest           Kind = "web_request"
	KindWebResult            Kind = "web_result"
	KindRecordRequest        Kind = "record_request"
	KindRecordResult         Kind = "record_result"
)

type AccountRequest struct {
	AccountID *string `json:"account_id,omitempty"`
	Operation *string `json:"operation,omitempty"`
	Provider  *string `json:"provider,omitempty"`
	Purpose   *string `json:"purpose,omitempty"`
}
type AccountResult struct {
	AccountID *string           `json:"account_id,omitempty"`
	Accounts  *[]AccountSummary `json:"accounts,omitempty"`
	Operation *string           `json:"operation,omitempty"`
	Status    *string           `json:"status,omitempty"`
	Stream    *StreamMetadata   `json:"stream,omitempty"`
	Text      *string           `json:"text,omitempty"`
}
type AccountSummary struct {
	AccountID string  `json:"account_id"`
	Label     string  `json:"label"`
	Plan      *string `json:"plan,omitempty"`
	Provider  string  `json:"provider"`
	Status    string  `json:"status"`
}
type Agent struct {
	AgentID string  `json:"agent_id"`
	Name    string  `json:"name"`
	Preset  *string `json:"preset"`
}
type AgentRequest struct {
	AgentID   *string          `json:"agent_id,omitempty"`
	Candidate *GrowthCandidate `json:"candidate,omitempty"`
	Name      *string          `json:"name,omitempty"`
	Operation *string          `json:"operation,omitempty"`
	Revision  *string          `json:"revision,omitempty"`
}
type AgentResult struct {
	AgentID   *string         `json:"agent_id,omitempty"`
	Agents    *[]Agent        `json:"agents,omitempty"`
	Notices   *[]GrowthNotice `json:"notices,omitempty"`
	Operation *string         `json:"operation,omitempty"`
	Revision  *string         `json:"revision,omitempty"`
}
type AgentTalk struct {
	Entries    *[]AgentTalkLine  `json:"entries,omitempty"`
	Events     *[]AgentTalkEvent `json:"events,omitempty"`
	ExchangeID *string           `json:"exchange_id,omitempty"`
	NextCursor *string           `json:"next_cursor,omitempty"`
}
type AgentTalkEntry struct {
	Entry  *AgentTalkLine  `json:"entry,omitempty"`
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type AgentTalkEvent struct {
	AgentID string `json:"agent_id"`
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
}
type AgentTalkExpand struct {
	Cursor     *string `json:"cursor,omitempty"`
	ExchangeID *string `json:"exchange_id,omitempty"`
}
type AgentTalkGet struct {
	AgentID *string `json:"agent_id,omitempty"`
}
type AgentTalkLine struct {
	ExchangeID  string `json:"exchange_id"`
	FromAgent   string `json:"from_agent"`
	NeedsPerson bool   `json:"needs_person"`
	Outcome     string `json:"outcome"`
	State       string `json:"state"`
	Subject     string `json:"subject"`
	ToAgent     string `json:"to_agent"`
	UpdatedAt   string `json:"updated_at"`
}

// AgentTalkSubscribe: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type AgentTalkSubscribe struct {
}
type ApprovalAnswer struct {
	CardID   *string          `json:"card_id,omitempty"`
	Choice   ApprovalChoice   `json:"choice"`
	Outcome  *DecisionOutcome `json:"outcome,omitempty"`
	Revision *string          `json:"revision,omitempty"`
}
type ApprovalChoice string

const (
	ApprovalChoiceOnce        ApprovalChoice = "once"
	ApprovalChoiceDeny        ApprovalChoice = "deny"
	ApprovalChoiceAlwaysAllow ApprovalChoice = "always_allow"
)

type ApprovalRequest struct {
	Card           *DecisionCard    `json:"card,omitempty"`
	Choices        []ApprovalChoice `json:"choices"`
	ConversationID *string          `json:"conversation_id,omitempty"`
	Stream         *StreamMetadata  `json:"stream,omitempty"`
}
type BackgroundNotice struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type Capability struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type ChannelBinding struct {
	AgentID     string       `json:"agent_id"`
	Chat        string       `json:"chat"`
	Scope       ChannelScope `json:"scope"`
	Topic       string       `json:"topic"`
	Unreachable bool         `json:"unreachable"`
}
type ChannelConnection struct {
	Account     string           `json:"account"`
	Bindings    []ChannelBinding `json:"bindings"`
	Platform    string           `json:"platform"`
	PrivateChat string           `json:"private_chat"`
	Status      string           `json:"status"`
}
type ChannelRequest struct {
	Account   *string       `json:"account,omitempty"`
	AgentID   *string       `json:"agent_id,omitempty"`
	Chat      *string       `json:"chat,omitempty"`
	Operation *string       `json:"operation,omitempty"`
	Platform  *string       `json:"platform,omitempty"`
	Scope     *ChannelScope `json:"scope,omitempty"`
	Token     *string       `json:"token,omitempty"`
	Topic     *string       `json:"topic,omitempty"`
}
type ChannelResult struct {
	Connections *[]ChannelConnection `json:"connections,omitempty"`
	ExpiresAt   *string              `json:"expires_at,omitempty"`
	Operation   *string              `json:"operation,omitempty"`
	PairingCode *string              `json:"pairing_code,omitempty"`
}
type ChannelScope struct {
	Memory bool   `json:"memory"`
	Reads  string `json:"reads"`
	Tools  bool   `json:"tools"`
	Writes string `json:"writes"`
}
type ChannelState struct {
	Connection *ChannelConnection `json:"connection,omitempty"`
	Stream     *StreamMetadata    `json:"stream,omitempty"`
	Text       *string            `json:"text,omitempty"`
}
type CompletionState string

const (
	CompletionStateDone    CompletionState = "done"
	CompletionStateClaimed CompletionState = "claimed"
)

type CompletionStatus struct {
	Label string          `json:"label"`
	State CompletionState `json:"state"`
}
type Connected struct {
	ShellFileToolsAvailable *bool           `json:"shell_file_tools_available,omitempty"`
	Stream                  *StreamMetadata `json:"stream,omitempty"`
	Text                    *string         `json:"text,omitempty"`
}
type ConversationSpeaker string

const (
	ConversationSpeakerPerson    ConversationSpeaker = "person"
	ConversationSpeakerAgent     ConversationSpeaker = "agent"
	ConversationSpeakerSystem    ConversationSpeaker = "system"
	ConversationSpeakerForwarded ConversationSpeaker = "forwarded"
)

type ConversationStart struct {
	AgentID string `json:"agent_id"`
}
type DecisionCard struct {
	Agent          string           `json:"agent"`
	ID             string           `json:"id"`
	Outcome        *DecisionOutcome `json:"outcome,omitempty"`
	Recommendation *string          `json:"recommendation,omitempty"`
	Revision       string           `json:"revision"`
	Summary        string           `json:"summary"`
	Targets        []DecisionTarget `json:"targets"`
}
type DecisionOutcome string

const (
	DecisionOutcomeResolved        DecisionOutcome = "resolved"
	DecisionOutcomeAlreadyResolved DecisionOutcome = "already_resolved"
	DecisionOutcomeWithdrawn       DecisionOutcome = "withdrawn"
	DecisionOutcomeExpired         DecisionOutcome = "expired"
	DecisionOutcomeStaleEpoch      DecisionOutcome = "stale_epoch"
	DecisionOutcomeStaleGroup      DecisionOutcome = "stale_group"
	DecisionOutcomeRefused         DecisionOutcome = "refused"
	DecisionOutcomeStopped         DecisionOutcome = "stopped"
)

type DecisionTarget struct {
	Name  string `json:"name"`
	Value string `json:"value"`
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
type ExportRequest struct {
	AgentID       *string   `json:"agent_id,omitempty"`
	Conversations *[]string `json:"conversations,omitempty"`
	Since         *string   `json:"since,omitempty"`
	Until         *string   `json:"until,omitempty"`
}
type ExportResult struct {
	Directory string `json:"directory"`
	Manifest  string `json:"manifest"`
}
type FollowUp struct {
	ConversationID *string `json:"conversation_id,omitempty"`
	RunID          string  `json:"run_id"`
	Text           string  `json:"text"`
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
type GithubCheck struct {
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
	State      string `json:"state"`
}
type GithubComment struct {
	Body string `json:"body"`
	ID   string `json:"id"`
	Path string `json:"path"`
	URL  string `json:"url"`
}
type GithubReview struct {
	Comments []GithubComment `json:"comments"`
	ID       string          `json:"id"`
	Outdated bool            `json:"outdated"`
	Resolved bool            `json:"resolved"`
}
type GrowthCandidate struct {
	Expected string `json:"expected"`
	ID       string `json:"id"`
	Notice   string `json:"notice"`
	Snapshot string `json:"snapshot"`
}
type GrowthNotice struct {
	Notice       string `json:"notice"`
	Revision     string `json:"revision"`
	UndoRevision string `json:"undo_revision"`
}
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
type MemoryRequest struct {
	After     *string `json:"after,omitempty"`
	AgentID   *string `json:"agent_id,omitempty"`
	Holder    *string `json:"holder,omitempty"`
	ItemID    *string `json:"item_id,omitempty"`
	Operation *string `json:"operation,omitempty"`
	Scope     *string `json:"scope,omitempty"`
	Text      *string `json:"text,omitempty"`
}
type MemoryResult struct {
	ItemID    *string          `json:"item_id,omitempty"`
	Items     *[]MemorySummary `json:"items,omitempty"`
	Next      *string          `json:"next,omitempty"`
	Operation *string          `json:"operation,omitempty"`
}
type MemorySummary struct {
	Content  string      `json:"content"`
	Evidence []string    `json:"evidence"`
	Holder   string      `json:"holder"`
	ItemID   string      `json:"item_id"`
	Revision json.Number `json:"revision"`
	Scope    string      `json:"scope"`
}
type MultiTaskControl struct {
	Action         string       `json:"action"`
	ConversationID *string      `json:"conversation_id,omitempty"`
	Count          *json.Number `json:"count,omitempty"`
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

type PlanBody struct {
	Confirm *string     `json:"confirm"`
	Entries []PlanEntry `json:"entries"`
}
type PlanEntry struct {
	DependsOn *[]string `json:"depends_on,omitempty"`
	Done      *string   `json:"done,omitempty"`
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Parent    *string   `json:"parent"`
	Stage     *string   `json:"stage,omitempty"`
	State     string    `json:"state"`
	Title     string    `json:"title"`
}
type PlanRequest struct {
	AgentID   string       `json:"agent_id"`
	Done      *string      `json:"done,omitempty"`
	Operation string       `json:"operation"`
	ResultID  *string      `json:"result_id,omitempty"`
	Revision  *json.Number `json:"revision,omitempty"`
	Title     *string      `json:"title,omitempty"`
}
type PlanResponse struct {
	EffectState *EffectState `json:"effect_state,omitempty"`
	RecordID    *string      `json:"record_id,omitempty"`
	Records     *[]string    `json:"records,omitempty"`
	Result      *Result      `json:"result,omitempty"`
	Revision    *string      `json:"revision,omitempty"`
}
type Progress struct {
	CardOrder   *[]string      `json:"card_order,omitempty"`
	Lanes       *[]WorkerLane  `json:"lanes,omitempty"`
	NextCursor  *string        `json:"next_cursor,omitempty"`
	Rows        *[]ProgressRow `json:"rows,omitempty"`
	Waiting     *[]WaitingRow  `json:"waiting,omitempty"`
	WaitingMore *json.Number   `json:"waiting_more,omitempty"`
}
type ProgressDigest struct {
	Lines []string    `json:"lines"`
	Link  string      `json:"link"`
	More  json.Number `json:"more"`
	Total json.Number `json:"total"`
}
type ProgressDigestGet struct {
	Order *string `json:"order,omitempty"`
}
type ProgressGet struct {
	After *string `json:"after,omitempty"`
	View  *string `json:"view,omitempty"`
}
type ProgressLine struct {
	Depth        json.Number   `json:"depth"`
	FinishedAt   *string       `json:"finished_at,omitempty"`
	JustFinished string        `json:"just_finished"`
	Kind         string        `json:"kind"`
	Name         string        `json:"name"`
	Next         string        `json:"next"`
	Now          *string       `json:"now,omitempty"`
	Percent      *json.Number  `json:"percent,omitempty"`
	Stage        string        `json:"stage"`
	StartedAt    *string       `json:"started_at,omitempty"`
	State        *RunningState `json:"state,omitempty"`
	Waiting      json.Number   `json:"waiting"`
}
type ProgressRow struct {
	Line ProgressLine `json:"line"`
	Ref  string       `json:"ref"`
}
type ProgressUpdate struct {
	CardOrder   *[]string       `json:"card_order,omitempty"`
	Rows        *[]ProgressRow  `json:"rows,omitempty"`
	Stream      *StreamMetadata `json:"stream,omitempty"`
	Text        *string         `json:"text,omitempty"`
	Waiting     *[]WaitingRow   `json:"waiting,omitempty"`
	WaitingMore *json.Number    `json:"waiting_more,omitempty"`
}
type Protocol struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type Question struct {
	Card           *DecisionCard   `json:"card,omitempty"`
	ConversationID string          `json:"conversation_id"`
	Form           *QuestionForm   `json:"form,omitempty"`
	Stream         *StreamMetadata `json:"stream,omitempty"`
}
type QuestionAnswer struct {
	Answer   QuestionAnswerAction `json:"answer"`
	CardID   *string              `json:"card_id,omitempty"`
	Outcome  *DecisionOutcome     `json:"outcome,omitempty"`
	Revision *string              `json:"revision,omitempty"`
	Values   *FormValues          `json:"values,omitempty"`
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
	Step   string          `json:"step"`
	Stream *StreamMetadata `json:"stream,omitempty"`
}
type ReceiptRef struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Revision string `json:"revision"`
}
type RecordChip struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type RecordRequest struct {
	After     *string `json:"after,omitempty"`
	Day       *string `json:"day,omitempty"`
	Operation string  `json:"operation"`
	Ref       *string `json:"ref,omitempty"`
	Revision  *string `json:"revision,omitempty"`
	Search    *string `json:"search,omitempty"`
	Text      *string `json:"text,omitempty"`
	Token     *string `json:"token,omitempty"`
	View      *string `json:"view,omitempty"`
	Zone      *string `json:"zone,omitempty"`
}
type RecordResult struct {
	Code      *string     `json:"code,omitempty"`
	Next      *string     `json:"next,omitempty"`
	Operation string      `json:"operation"`
	Rows      []RecordRow `json:"rows"`
	Token     *string     `json:"token,omitempty"`
}
type RecordRow struct {
	Blocked  bool         `json:"blocked"`
	Chips    []RecordChip `json:"chips"`
	Kind     string       `json:"kind"`
	Note     string       `json:"note"`
	Overdue  bool         `json:"overdue"`
	Ref      string       `json:"ref"`
	Revision string       `json:"revision"`
	State    string       `json:"state"`
	Title    string       `json:"title"`
}
type ReplayCandidate struct {
	Name string `json:"name"`
	Text string `json:"text"`
}
type ReplayRequest struct {
	Candidates *[]ReplayCandidate `json:"candidates,omitempty"`
	Effort     *string            `json:"effort,omitempty"`
	Episode    *string            `json:"episode,omitempty"`
	Model      *string            `json:"model,omitempty"`
	TurnID     *string            `json:"turn_id,omitempty"`
}
type ReplayResult struct {
	ExcludedReason *string `json:"excluded_reason,omitempty"`
	Output         *string `json:"output,omitempty"`
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
	Body          *json.RawMessage  `json:"body,omitempty"`
	Completion    *CompletionStatus `json:"completion,omitempty"`
	Next          *json.Number      `json:"next,omitempty"`
	Provenance    ResultProvenance  `json:"provenance"`
	ResultID      string            `json:"result_id"`
	RevisedBy     string            `json:"revised_by"`
	Revision      json.Number       `json:"revision"`
	RunID         *string           `json:"run_id,omitempty"`
	SavedAs       *SavedReference   `json:"saved_as"`
	Status        ResultStatus      `json:"status"`
	Stream        *StreamMetadata   `json:"stream,omitempty"`
	Summary       string            `json:"summary"`
	Text          *string           `json:"text,omitempty"`
	TextTruncated *bool             `json:"text_truncated,omitempty"`
	Title         string            `json:"title"`
	Type          ResultType        `json:"type"`
}
type ResultEdit struct {
	BaseRevision json.Number     `json:"base_revision"`
	Body         json.RawMessage `json:"body"`
	ResultID     string          `json:"result_id"`
}
type ResultGet struct {
	Offset   *json.Number `json:"offset,omitempty"`
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
type Running struct {
	Next  *json.Number `json:"next,omitempty"`
	RunID *string      `json:"run_id,omitempty"`
	Text  *string      `json:"text,omitempty"`
}
type RunningGet struct {
	Action *string      `json:"action,omitempty"`
	Offset *json.Number `json:"offset,omitempty"`
	RunID  *string      `json:"run_id,omitempty"`
}
type RunningReason string

const (
	RunningReasonDependency   RunningReason = "dependency"
	RunningReasonArea         RunningReason = "area"
	RunningReasonQuota        RunningReason = "quota"
	RunningReasonHostCapacity RunningReason = "host_capacity"
	RunningReasonCiPending    RunningReason = "ci_pending"
	RunningReasonRunLimit     RunningReason = "run_limit"
	RunningReasonPerson       RunningReason = "person"
	RunningReasonBudget       RunningReason = "budget"
)

type RunningState string

const (
	RunningStateInProgress     RunningState = "in_progress"
	RunningStateWaiting        RunningState = "waiting"
	RunningStateNeedsInput     RunningState = "needs_input"
	RunningStateReadyForReview RunningState = "ready_for_review"
	RunningStateFailed         RunningState = "failed"
	RunningStateStopped        RunningState = "stopped"
	RunningStateDone           RunningState = "done"
	RunningStateUnknown        RunningState = "unknown"
)

type RunningUpdate struct {
	Reason  *RunningReason  `json:"reason,omitempty"`
	Removed *bool           `json:"removed,omitempty"`
	RunID   *string         `json:"run_id,omitempty"`
	State   *RunningState   `json:"state,omitempty"`
	Stream  *StreamMetadata `json:"stream,omitempty"`
	Summary *string         `json:"summary,omitempty"`
	Text    *string         `json:"text,omitempty"`
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
type Stop struct {
	ConversationID *string `json:"conversation_id,omitempty"`
	RunID          *string `json:"run_id,omitempty"`
}
type StopOutcome string

const (
	StopOutcomeCancelled StopOutcome = "cancelled"
	StopOutcomeComplete  StopOutcome = "complete"
	StopOutcomePartial   StopOutcome = "partial"
	StopOutcomeFailed    StopOutcome = "failed"
	StopOutcomeRequested StopOutcome = "requested"
)

type StopResult struct {
	Outcome StopOutcome `json:"outcome"`
	RunID   string      `json:"run_id"`
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
type ToolActivity struct {
	AgentID    *string              `json:"agent_id,omitempty"`
	CallID     string               `json:"call_id"`
	DurationMs *json.Number         `json:"duration_ms,omitempty"`
	Outcome    *ToolActivityOutcome `json:"outcome,omitempty"`
	Status     ToolActivityStatus   `json:"status"`
	Stream     *StreamMetadata      `json:"stream,omitempty"`
	Summary    *string              `json:"summary,omitempty"`
	Target     *string              `json:"target,omitempty"`
	ToolName   string               `json:"tool_name"`
}
type ToolActivityOutcome string

const (
	ToolActivityOutcomeOk     ToolActivityOutcome = "ok"
	ToolActivityOutcomeFailed ToolActivityOutcome = "failed"
)

type ToolActivityStatus string

const (
	ToolActivityStatusStarted  ToolActivityStatus = "started"
	ToolActivityStatusFinished ToolActivityStatus = "finished"
)

type TurnPhase string

const (
	TurnPhaseWaitingModel TurnPhase = "waiting_model"
	TurnPhaseStreaming    TurnPhase = "streaming"
	TurnPhaseTool         TurnPhase = "tool"
	TurnPhaseStopping     TurnPhase = "stopping"
)

type TurnState struct {
	Active   *bool           `json:"active,omitempty"`
	AgentID  *string         `json:"agent_id,omitempty"`
	Phase    *TurnPhase      `json:"phase,omitempty"`
	Stream   *StreamMetadata `json:"stream,omitempty"`
	Text     *string         `json:"text,omitempty"`
	ToolName *string         `json:"tool_name,omitempty"`
}
type Waiting struct {
	More  *json.Number  `json:"more,omitempty"`
	Rows  *[]WaitingRow `json:"rows,omitempty"`
	Total *json.Number  `json:"total,omitempty"`
}

// WaitingAnswer: Reserved: docs/design/host-protocol.md#deferred — The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.
type WaitingAnswer struct {
}
type WaitingGet struct {
	Limit *json.Number `json:"limit,omitempty"`
	Order *string      `json:"order,omitempty"`
}
type WaitingLine struct {
	AgentName      string      `json:"agent_name"`
	Answers        []string    `json:"answers"`
	Context        string      `json:"context"`
	Due            string      `json:"due"`
	Effort         json.Number `json:"effort"`
	FreeText       bool        `json:"free_text"`
	Kind           string      `json:"kind"`
	Recommendation string      `json:"recommendation"`
	Sentence       string      `json:"sentence"`
}
type WaitingRef struct {
	Key      string `json:"key"`
	Revision string `json:"revision"`
	Source   string `json:"source"`
}
type WaitingRow struct {
	Line WaitingLine `json:"line"`
	Ref  WaitingRef  `json:"ref"`
}
type WaitingUpdate struct {
	Stream *StreamMetadata `json:"stream,omitempty"`
	Text   *string         `json:"text,omitempty"`
}
type WebRequest struct {
	Key       *string `json:"key,omitempty"`
	Operation string  `json:"operation"`
}
type WebResult struct {
	Configured bool   `json:"configured"`
	Provider   string `json:"provider"`
}
type Welcome struct {
	Agents          []Agent      `json:"agents"`
	Capabilities    []Capability `json:"capabilities"`
	Epoch           string       `json:"epoch"`
	MainID          string       `json:"main_id"`
	PersonID        string       `json:"person_id"`
	ProtocolVersion string       `json:"protocol_version"`
}
type WorkRequest struct {
	AgentID       *string   `json:"agent_id,omitempty"`
	Base          *string   `json:"base,omitempty"`
	BindingID     *string   `json:"binding_id,omitempty"`
	Clone         *string   `json:"clone,omitempty"`
	Head          *string   `json:"head,omitempty"`
	Operation     *string   `json:"operation,omitempty"`
	PrNumber      *string   `json:"pr_number,omitempty"`
	ProtectedRefs *[]string `json:"protected_refs,omitempty"`
	Repository    *string   `json:"repository,omitempty"`
}
type WorkResult struct {
	Base        *string         `json:"base,omitempty"`
	BindingID   *string         `json:"binding_id,omitempty"`
	Cause       *string         `json:"cause,omitempty"`
	Checks      *[]GithubCheck  `json:"checks,omitempty"`
	Ci          *string         `json:"ci,omitempty"`
	Done        *bool           `json:"done,omitempty"`
	Head        *string         `json:"head,omitempty"`
	MergeCommit *string         `json:"merge_commit,omitempty"`
	PrNumber    *string         `json:"pr_number,omitempty"`
	Repository  *string         `json:"repository,omitempty"`
	RetryAt     *string         `json:"retry_at,omitempty"`
	Reviews     *[]GithubReview `json:"reviews,omitempty"`
	Revision    *string         `json:"revision,omitempty"`
	Source      *string         `json:"source,omitempty"`
	State       *string         `json:"state,omitempty"`
}
type WorkSwitchEvent struct {
	Evidence string `json:"evidence"`
	State    string `json:"state"`
}
type WorkSwitchRequest struct {
	Evidence  *string   `json:"evidence,omitempty"`
	Operation string    `json:"operation"`
	Snapshot  *string   `json:"snapshot,omitempty"`
	SwitchID  string    `json:"switch_id"`
	Tasks     *[]string `json:"tasks,omitempty"`
}
type WorkSwitchResult struct {
	Approval string            `json:"approval"`
	Events   []WorkSwitchEvent `json:"events"`
	Snapshot string            `json:"snapshot"`
	State    string            `json:"state"`
	SwitchID string            `json:"switch_id"`
	Tasks    []string          `json:"tasks"`
}
type WorkerLane struct {
	AgentID    string   `json:"agent_id"`
	AgentName  string   `json:"agent_name"`
	Details    []string `json:"details"`
	Generation string   `json:"generation"`
	LaneID     string   `json:"lane_id"`
	NodeID     string   `json:"node_id"`
	State      string   `json:"state"`
	TaskID     string   `json:"task_id"`
	ThreadID   string   `json:"thread_id"`
	Title      string   `json:"title"`
	TurnID     string   `json:"turn_id"`
	Worker     string   `json:"worker"`
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
func (ToolActivity) isPayload()         {}
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
func (StopResult) isPayload()           {}
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
func (WorkSwitchRequest) isPayload()    {}
func (WorkSwitchResult) isPayload()     {}
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
func (ExportRequest) isPayload()        {}
func (ExportResult) isPayload()         {}
func (ReplayRequest) isPayload()        {}
func (ReplayResult) isPayload()         {}
func (PlanRequest) isPayload()          {}
func (PlanResponse) isPayload()         {}
func (WebRequest) isPayload()           {}
func (WebResult) isPayload()            {}
func (RecordRequest) isPayload()        {}
func (RecordResult) isPayload()         {}

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
	case KindToolActivity:
		var p ToolActivity
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
	case KindStopResult:
		var p StopResult
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
	case KindWorkSwitchRequest:
		var p WorkSwitchRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWorkSwitchResult:
		var p WorkSwitchResult
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
	case KindExportRequest:
		var p ExportRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindExportResult:
		var p ExportResult
		err := json.Unmarshal(data, &p)
		return p, err
	case KindReplayRequest:
		var p ReplayRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindReplayResult:
		var p ReplayResult
		err := json.Unmarshal(data, &p)
		return p, err
	case KindPlanRequest:
		var p PlanRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindPlanResponse:
		var p PlanResponse
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWebRequest:
		var p WebRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindWebResult:
		var p WebResult
		err := json.Unmarshal(data, &p)
		return p, err
	case KindRecordRequest:
		var p RecordRequest
		err := json.Unmarshal(data, &p)
		return p, err
	case KindRecordResult:
		var p RecordResult
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

const clientSchemaJSON = `{
  "$comment": "SPDX-License-Identifier: Apache-2.0",
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Client 1.0 envelope",
  "description": "Client 1.0 kinds and specified payload fields. Reserved objects have no wire fields until the implementation issue named in their $comment defines them.",
  "type": "object",
  "properties": {
    "protocol": {
      "$ref": "#/$defs/protocol"
    },
    "kind": {
      "type": "string",
      "enum": [
        "hello",
        "welcome",
        "error",
        "conversation_start",
        "input",
        "interrupt",
        "history_get",
        "history",
        "subscribe",
        "output_delta",
        "item_done",
        "turn_state",
        "tool_activity",
        "approval_request",
        "approval_answer",
        "question",
        "question_answer",
        "result",
        "result_get",
        "result_edit",
        "result_save",
        "running_get",
        "running",
        "running_update",
        "stop",
        "stop_result",
        "follow_up",
        "background_notice",
        "progress_get",
        "progress",
        "progress_update",
        "progress_digest_get",
        "progress_digest",
        "waiting_get",
        "waiting",
        "waiting_update",
        "waiting_answer",
        "details_get",
        "details",
        "readiness_failure",
        "agent_request",
        "agent_result",
        "work_request",
        "work_result",
        "work_switch_request",
        "work_switch_result",
        "account_request",
        "account_result",
        "memory_request",
        "memory_result",
        "memory_import_progress",
        "memory_promotion_batch",
        "channel_request",
        "channel_result",
        "channel_state",
        "agent_talk_get",
        "agent_talk",
        "agent_talk_entry",
        "agent_talk_expand",
        "agent_talk_subscribe",
        "connected",
        "disconnected",
        "task_status",
        "run_status",
        "task_cancel",
        "multi_task_control",
        "sign_in_listen",
        "sign_in_listening",
        "sign_in_open",
        "sign_in_redirect",
        "snapshot",
        "resync",
        "resync_required",
        "restart_request",
        "restart_get",
        "restart_status",
        "export_request",
        "export_result",
        "replay_request",
        "replay_result",
        "plan_request",
        "plan_response",
        "web_request",
        "web_result",
        "record_request",
        "record_result"
      ]
    },
    "message_id": {
      "type": "string"
    },
    "attempt_id": {
      "type": "string"
    },
    "reply_to": {
      "anyOf": [
        {
          "type": "string"
        },
        {
          "type": "null"
        }
      ]
    },
    "sender": {
      "$ref": "#/$defs/sender"
    },
    "epoch": {
      "$ref": "#/$defs/epoch"
    },
    "sent_at": {
      "$ref": "#/$defs/utc"
    },
    "idempotency_key": {
      "anyOf": [
        {
          "type": "string"
        },
        {
          "type": "null"
        }
      ]
    },
    "effect_state": {
      "$ref": "#/$defs/effect_state"
    },
    "receipt_ref": {
      "anyOf": [
        {
          "$ref": "#/$defs/receipt_ref"
        },
        {
          "type": "null"
        }
      ]
    },
    "source_refs": {
      "type": "array",
      "items": {
        "$ref": "#/$defs/source_ref"
      }
    },
    "error": {
      "anyOf": [
        {
          "$ref": "#/$defs/error"
        },
        {
          "type": "null"
        }
      ]
    },
    "payload": {
      "type": "object"
    }
  },
  "required": [
    "protocol",
    "kind",
    "message_id",
    "attempt_id",
    "reply_to",
    "sender",
    "epoch",
    "sent_at",
    "idempotency_key",
    "effect_state",
    "receipt_ref",
    "source_refs",
    "error",
    "payload"
  ],
  "additionalProperties": false,
  "allOf": [
    {
      "if": {
        "properties": {
          "kind": {
            "const": "hello"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/hello"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "welcome"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/welcome"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "error"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/error_payload"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "conversation_start"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/conversation_start"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "input"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/input"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "interrupt"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/interrupt"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "history_get"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/history_get"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "history"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/history"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "subscribe"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/subscribe"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "output_delta"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/output_delta"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "item_done"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/item_done"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "turn_state"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/turn_state"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "tool_activity"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/tool_activity"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "approval_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/approval_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "approval_answer"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/approval_answer"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "question"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/question"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "question_answer"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/question_answer"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "result_get"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/result_get"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "result_edit"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/result_edit"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "result_save"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/result_save"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "running_get"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/running_get"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "running"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/running"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "running_update"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/running_update"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "stop"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/stop"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "follow_up"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/follow_up"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "background_notice"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/background_notice"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "progress_get"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/progress_get"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "progress"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/progress"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "progress_update"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/progress_update"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "progress_digest_get"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/progress_digest_get"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "progress_digest"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/progress_digest"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "waiting_get"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/waiting_get"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "waiting"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/waiting"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "waiting_update"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/waiting_update"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "waiting_answer"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/waiting_answer"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "details_get"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/details_get"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "details"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/details"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "readiness_failure"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/readiness_failure"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "agent_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/agent_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "agent_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/agent_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "work_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/work_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "work_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/work_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "account_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/account_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "account_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/account_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "memory_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/memory_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "memory_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/memory_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "memory_import_progress"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/memory_import_progress"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "memory_promotion_batch"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/memory_promotion_batch"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "channel_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/channel_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "channel_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/channel_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "channel_state"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/channel_state"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "agent_talk_get"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/agent_talk_get"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "agent_talk"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/agent_talk"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "agent_talk_entry"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/agent_talk_entry"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "agent_talk_expand"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/agent_talk_expand"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "agent_talk_subscribe"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/agent_talk_subscribe"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "connected"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/connected"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "disconnected"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/disconnected"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "task_status"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/task_status"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "run_status"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/run_status"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "task_cancel"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/task_cancel"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "multi_task_control"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/multi_task_control"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "sign_in_listen"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/sign_in_listen"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "sign_in_listening"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/sign_in_listening"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "sign_in_open"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/sign_in_open"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "sign_in_redirect"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/sign_in_redirect"
          }
        }
      }
    },
    {
      "if": {
        "anyOf": [
          {
            "properties": {
              "kind": {
                "const": "error"
              }
            }
          },
          {
            "properties": {
              "effect_state": {
                "enum": [
                  "refused",
                  "failed"
                ]
              }
            }
          }
        ]
      },
      "then": {
        "properties": {
          "error": {
            "$ref": "#/$defs/error"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "result_save"
          }
        }
      },
      "then": {
        "properties": {
          "idempotency_key": {
            "type": "string"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "snapshot"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/snapshot"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "resync"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/resync"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "resync_required"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/resync_required"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "restart_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/restart_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "restart_get"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/restart_get"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "restart_status"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/restart_status"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "export_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/export_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "export_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/export_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "replay_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/replay_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "replay_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/replay_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "stop_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/stop_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "plan_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/plan_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "plan_response"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/plan_response"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "web_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/web_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "web_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/web_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "record_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/record_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "record_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/record_result"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "work_switch_request"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/work_switch_request"
          }
        }
      }
    },
    {
      "if": {
        "properties": {
          "kind": {
            "const": "work_switch_result"
          }
        }
      },
      "then": {
        "properties": {
          "payload": {
            "$ref": "#/$defs/work_switch_result"
          }
        }
      }
    }
  ],
  "$defs": {
    "version": {
      "type": "string",
      "pattern": "^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$",
      "not": {
        "pattern": "\\s"
      }
    },
    "epoch": {
      "type": "string",
      "pattern": "^(0|[1-9][0-9]{0,18}|1[0-7][0-9]{18}|18[0-3][0-9]{17}|184[0-3][0-9]{16}|1844[0-5][0-9]{15}|18446[0-6][0-9]{14}|184467[0-3][0-9]{13}|1844674[0-3][0-9]{12}|184467440[0-6][0-9]{10}|1844674407[0-2][0-9]{9}|18446744073[0-6][0-9]{8}|1844674407370[0-8][0-9]{6}|18446744073709[0-4][0-9]{5}|184467440737095[0-4][0-9]{4}|18446744073709550[0-9]{3}|18446744073709551[0-5][0-9]{2}|1844674407370955160[0-9]{1}|1844674407370955161[0-4]|18446744073709551615)$",
      "not": {
        "pattern": "\\s"
      }
    },
    "utc": {
      "type": "string",
      "format": "date-time",
      "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?(Z|\\+00:00)$",
      "not": {
        "pattern": "\\s"
      }
    },
    "party": {
      "type": "string",
      "enum": [
        "core",
        "client",
        "node"
      ]
    },
    "effect_state": {
      "type": "string",
      "enum": [
        "none",
        "refused",
        "failed",
        "applied",
        "cancelled",
        "unknown"
      ],
      "x-go-prefix": "Effect"
    },
    "error_code": {
      "type": "string",
      "enum": [
        "unsupported_combination",
        "capability_not_negotiated",
        "malformed",
        "stale_epoch",
        "stale_revision",
        "not_authorized",
        "out_of_scope",
        "duplicate",
        "unavailable"
      ],
      "x-go-prefix": "Error"
    },
    "protocol": {
      "type": "object",
      "properties": {
        "name": {
          "const": "client"
        },
        "version": {
          "const": "1.0"
        }
      },
      "required": [
        "name",
        "version"
      ],
      "additionalProperties": false
    },
    "sender": {
      "type": "object",
      "properties": {
        "party": {
          "$ref": "#/$defs/party"
        },
        "id": {
          "type": "string"
        }
      },
      "required": [
        "party",
        "id"
      ],
      "additionalProperties": false
    },
    "receipt_ref": {
      "type": "object",
      "properties": {
        "kind": {
          "const": "receipt"
        },
        "id": {
          "type": "string"
        },
        "revision": {
          "type": "string"
        }
      },
      "required": [
        "kind",
        "id",
        "revision"
      ],
      "additionalProperties": false
    },
    "source_ref": {
      "type": "object",
      "properties": {
        "origin": {
          "type": "string"
        },
        "namespace": {
          "type": "string"
        },
        "id": {
          "type": "string"
        },
        "revision": {
          "type": "string"
        },
        "locator": {
          "type": "string"
        },
        "observed_at": {
          "$ref": "#/$defs/utc"
        },
        "observed_by": {
          "anyOf": [
            {
              "type": "string"
            },
            {
              "type": "null"
            }
          ]
        }
      },
      "required": [
        "origin",
        "namespace",
        "id",
        "revision",
        "locator",
        "observed_at",
        "observed_by"
      ],
      "additionalProperties": false
    },
    "error": {
      "type": "object",
      "properties": {
        "code": {
          "$ref": "#/$defs/error_code"
        },
        "detail": {
          "type": "string"
        }
      },
      "required": [
        "code",
        "detail"
      ],
      "additionalProperties": false
    },
    "capability": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string"
        },
        "version": {
          "$ref": "#/$defs/version"
        }
      },
      "required": [
        "name",
        "version"
      ],
      "additionalProperties": false
    },
    "agent": {
      "type": "object",
      "properties": {
        "agent_id": {
          "type": "string"
        },
        "name": {
          "type": "string"
        },
        "preset": {
          "anyOf": [
            {
              "type": "string"
            },
            {
              "type": "null"
            }
          ]
        }
      },
      "required": [
        "agent_id",
        "name",
        "preset"
      ],
      "additionalProperties": false
    },
    "hello": {
      "type": "object",
      "properties": {
        "party": {
          "const": "client",
          "x-go-type": "Party"
        },
        "id": {
          "type": "string"
        },
        "product_version": {
          "type": "string"
        },
        "protocol_versions": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/version"
          },
          "minItems": 1
        },
        "capabilities": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/capability"
          }
        },
        "peer": {
          "type": "string"
        },
        "platform": {
          "type": "string"
        }
      },
      "required": [
        "party",
        "id",
        "product_version",
        "protocol_versions",
        "capabilities"
      ],
      "additionalProperties": false
    },
    "welcome": {
      "type": "object",
      "properties": {
        "protocol_version": {
          "const": "1.0"
        },
        "capabilities": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/capability"
          }
        },
        "main_id": {
          "type": "string"
        },
        "epoch": {
          "$ref": "#/$defs/epoch"
        },
        "person_id": {
          "type": "string"
        },
        "agents": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/agent"
          }
        }
      },
      "required": [
        "protocol_version",
        "capabilities",
        "main_id",
        "epoch",
        "person_id",
        "agents"
      ],
      "additionalProperties": false
    },
    "error_payload": {
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "conversation_start": {
      "type": "object",
      "properties": {
        "agent_id": {
          "type": "string"
        }
      },
      "required": [
        "agent_id"
      ],
      "additionalProperties": false
    },
    "input": {
      "type": "object",
      "properties": {
        "conversation_id": {
          "type": "string",
          "minLength": 1
        },
        "input_id": {
          "type": "string",
          "minLength": 1
        },
        "text": {
          "type": "string",
          "minLength": 1,
          "maxLength": 16384
        },
        "mode": {
          "$ref": "#/$defs/input_mode"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "interrupt": {
      "type": "object",
      "properties": {
        "conversation_id": {
          "type": "string",
          "minLength": 1
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "history_get": {
      "type": "object",
      "properties": {
        "agent_id": {
          "type": "string",
          "minLength": 1
        },
        "conversation_id": {
          "type": "string",
          "minLength": 1
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "history": {
      "type": "object",
      "properties": {
        "conversation_id": {
          "type": "string",
          "minLength": 1
        },
        "snapshot": {
          "$ref": "#/$defs/snapshot"
        },
        "agent_id": {
          "type": "string",
          "minLength": 1
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "subscribe": {
      "type": "object",
      "properties": {
        "topic": {
          "$ref": "#/$defs/stream_topic"
        },
        "after": {
          "$ref": "#/$defs/stream_cursor"
        },
        "active": {
          "type": "boolean"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "output_delta": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        },
        "agent_id": {
          "type": "string",
          "minLength": 1
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "item_done": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        },
        "echo_key": {
          "type": "string",
          "minLength": 1
        },
        "speaker": {
          "$ref": "#/$defs/conversation_speaker"
        },
        "agent_id": {
          "type": "string",
          "minLength": 1
        },
        "person_id": {
          "type": "string",
          "minLength": 1
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "turn_state": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        },
        "active": {
          "type": "boolean"
        },
        "agent_id": {
          "type": "string",
          "minLength": 1
        },
        "phase": {
          "$ref": "#/$defs/turn_phase"
        },
        "tool_name": {
          "type": "string",
          "description": "Trusted tool display name from the registry display catalog; never arguments or output."
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "approval_request": {
      "type": "object",
      "properties": {
        "conversation_id": {
          "type": "string",
          "minLength": 1
        },
        "choices": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/approval_choice"
          },
          "minItems": 1
        },
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "card": {
          "$ref": "#/$defs/decision_card"
        }
      },
      "required": [
        "choices"
      ],
      "additionalProperties": false
    },
    "approval_answer": {
      "type": "object",
      "properties": {
        "choice": {
          "$ref": "#/$defs/approval_choice"
        },
        "card_id": {
          "type": "string",
          "minLength": 1
        },
        "revision": {
          "type": "string",
          "minLength": 1
        },
        "outcome": {
          "$ref": "#/$defs/decision_outcome"
        }
      },
      "required": [
        "choice"
      ],
      "additionalProperties": false
    },
    "question": {
      "type": "object",
      "properties": {
        "conversation_id": {
          "type": "string"
        },
        "form": {
          "$ref": "#/$defs/question_form"
        },
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "card": {
          "$ref": "#/$defs/decision_card"
        }
      },
      "required": [
        "conversation_id"
      ],
      "additionalProperties": false
    },
    "question_answer": {
      "type": "object",
      "properties": {
        "answer": {
          "$ref": "#/$defs/question_answer_action"
        },
        "values": {
          "$ref": "#/$defs/form_values"
        },
        "card_id": {
          "type": "string",
          "minLength": 1
        },
        "revision": {
          "type": "string",
          "minLength": 1
        },
        "outcome": {
          "$ref": "#/$defs/decision_outcome"
        }
      },
      "required": [
        "answer"
      ],
      "additionalProperties": false,
      "allOf": [
        {
          "if": {
            "properties": {
              "answer": {
                "const": "accept"
              }
            }
          },
          "then": {
            "required": [
              "values"
            ]
          },
          "else": {
            "not": {
              "required": [
                "values"
              ]
            }
          }
        }
      ]
    },
    "result": {
      "type": "object",
      "properties": {
        "result_id": {
          "type": "string"
        },
        "revision": {
          "$ref": "#/$defs/result_revision"
        },
        "type": {
          "$ref": "#/$defs/result_type"
        },
        "title": {
          "type": "string"
        },
        "summary": {
          "type": "string"
        },
        "text": {
          "type": "string"
        },
        "body": {
          "$ref": "#/$defs/result_body"
        },
        "status": {
          "$ref": "#/$defs/result_status"
        },
        "saved_as": {
          "anyOf": [
            {
              "$ref": "#/$defs/saved_reference"
            },
            {
              "type": "null"
            }
          ]
        },
        "revised_by": {
          "type": "string"
        },
        "provenance": {
          "$ref": "#/$defs/result_provenance"
        },
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "run_id": {
          "type": "string",
          "minLength": 1
        },
        "completion": {
          "$ref": "#/$defs/completion_status"
        },
        "next": {
          "type": "integer",
          "minimum": 1
        },
        "text_truncated": {
          "type": "boolean"
        }
      },
      "required": [
        "result_id",
        "revision",
        "type",
        "title",
        "summary",
        "status",
        "saved_as",
        "revised_by",
        "provenance"
      ],
      "additionalProperties": false,
      "allOf": [
        {
          "if": {
            "properties": {
              "status": {
                "const": "preview"
              }
            }
          },
          "then": {
            "properties": {
              "saved_as": {
                "type": "null"
              }
            }
          },
          "else": {
            "properties": {
              "saved_as": {
                "$ref": "#/$defs/saved_reference"
              }
            }
          }
        },
        {
          "if": {
            "required": [
              "body"
            ]
          },
          "then": {
            "required": [
              "text"
            ]
          }
        }
      ]
    },
    "result_get": {
      "type": "object",
      "properties": {
        "result_id": {
          "type": "string"
        },
        "revision": {
          "$ref": "#/$defs/result_revision"
        },
        "offset": {
          "type": "integer",
          "minimum": 0
        }
      },
      "required": [
        "result_id"
      ],
      "additionalProperties": false
    },
    "result_edit": {
      "type": "object",
      "properties": {
        "result_id": {
          "type": "string"
        },
        "base_revision": {
          "$ref": "#/$defs/result_revision"
        },
        "body": {
          "$ref": "#/$defs/result_body"
        }
      },
      "required": [
        "result_id",
        "base_revision",
        "body"
      ],
      "additionalProperties": false
    },
    "result_save": {
      "type": "object",
      "properties": {
        "result_id": {
          "type": "string"
        },
        "revision": {
          "$ref": "#/$defs/result_revision"
        },
        "destination": {
          "$ref": "#/$defs/save_destination"
        }
      },
      "required": [
        "result_id",
        "revision",
        "destination"
      ],
      "additionalProperties": false
    },
    "running_get": {
      "type": "object",
      "properties": {
        "run_id": {
          "type": "string",
          "minLength": 1
        },
        "action": {
          "type": "string",
          "enum": [
            "open",
            "dismiss"
          ]
        },
        "offset": {
          "type": "integer",
          "minimum": 0,
          "maximum": 9007199254740991
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "running": {
      "type": "object",
      "properties": {
        "run_id": {
          "type": "string"
        },
        "text": {
          "type": "string"
        },
        "next": {
          "type": "integer",
          "minimum": 1,
          "maximum": 9007199254740991
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "running_update": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        },
        "run_id": {
          "type": "string"
        },
        "removed": {
          "type": "boolean"
        },
        "state": {
          "$ref": "#/$defs/running_state"
        },
        "summary": {
          "type": "string"
        },
        "reason": {
          "$ref": "#/$defs/running_reason"
        }
      },
      "required": [],
      "additionalProperties": false,
      "allOf": [
        {
          "if": {
            "anyOf": [
              {
                "required": [
                  "state"
                ]
              },
              {
                "required": [
                  "summary"
                ]
              },
              {
                "required": [
                  "reason"
                ]
              }
            ]
          },
          "then": {
            "required": [
              "state",
              "summary"
            ]
          }
        }
      ]
    },
    "stop": {
      "type": "object",
      "properties": {
        "run_id": {
          "type": "string"
        },
        "conversation_id": {
          "type": "string",
          "minLength": 1
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "follow_up": {
      "type": "object",
      "properties": {
        "run_id": {
          "type": "string",
          "minLength": 1
        },
        "text": {
          "type": "string",
          "minLength": 1,
          "maxLength": 16384
        },
        "conversation_id": {
          "type": "string",
          "minLength": 1
        }
      },
      "required": [
        "run_id",
        "text"
      ],
      "additionalProperties": false
    },
    "background_notice": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "progress_get": {
      "type": "object",
      "properties": {
        "after": {
          "type": "string"
        },
        "view": {
          "type": "string",
          "enum": [
            "overview",
            "lanes"
          ]
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "progress": {
      "type": "object",
      "properties": {
        "lanes": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/worker_lane"
          }
        },
        "next_cursor": {
          "type": "string"
        },
        "card_order": {
          "type": "array",
          "items": {
            "type": "string"
          }
        },
        "rows": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/progress_row"
          }
        },
        "waiting": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/waiting_row"
          }
        },
        "waiting_more": {
          "type": "integer",
          "minimum": 0
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "progress_update": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        },
        "card_order": {
          "type": "array",
          "items": {
            "type": "string"
          }
        },
        "rows": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/progress_row"
          }
        },
        "waiting": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/waiting_row"
          }
        },
        "waiting_more": {
          "type": "integer",
          "minimum": 0
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "progress_digest_get": {
      "type": "object",
      "properties": {
        "order": {
          "type": "string",
          "enum": [
            "priority",
            "lightest"
          ]
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "progress_digest": {
      "type": "object",
      "properties": {
        "lines": {
          "type": "array",
          "maxItems": 5,
          "items": {
            "type": "string"
          }
        },
        "total": {
          "type": "integer",
          "minimum": 0
        },
        "more": {
          "type": "integer",
          "minimum": 0
        },
        "link": {
          "type": "string"
        }
      },
      "required": [
        "lines",
        "total",
        "more",
        "link"
      ],
      "additionalProperties": false
    },
    "waiting_get": {
      "type": "object",
      "properties": {
        "order": {
          "type": "string",
          "enum": [
            "priority",
            "lightest"
          ]
        },
        "limit": {
          "type": "integer",
          "minimum": 0
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "waiting": {
      "type": "object",
      "properties": {
        "rows": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/waiting_row"
          }
        },
        "total": {
          "type": "integer",
          "minimum": 0
        },
        "more": {
          "type": "integer",
          "minimum": 0
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "waiting_update": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "waiting_answer": {
      "$comment": "Reserved: docs/design/host-protocol.md#deferred \u2014 The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.",
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "details_get": {
      "$comment": "Reserved: docs/design/host-protocol.md#deferred \u2014 The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.",
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "details": {
      "$comment": "Reserved: docs/design/host-protocol.md#deferred \u2014 The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.",
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "readiness_failure": {
      "type": "object",
      "properties": {
        "step": {
          "type": "string"
        },
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        }
      },
      "required": [
        "step"
      ],
      "additionalProperties": false
    },
    "agent_request": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "list",
            "create",
            "choose",
            "rename",
            "delete",
            "growth_adopt",
            "growth_notices",
            "growth_undo"
          ]
        },
        "agent_id": {
          "type": "string",
          "minLength": 1
        },
        "name": {
          "type": "string",
          "minLength": 1
        },
        "candidate": {
          "$ref": "#/$defs/growth_candidate"
        },
        "revision": {
          "type": "string",
          "pattern": "^[1-9][0-9]{0,18}$"
        }
      },
      "additionalProperties": false,
      "required": []
    },
    "agent_result": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "minLength": 1
        },
        "agent_id": {
          "type": "string",
          "minLength": 1
        },
        "agents": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/agent"
          }
        },
        "revision": {
          "type": "string",
          "pattern": "^[1-9][0-9]{0,18}$"
        },
        "notices": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/growth_notice"
          }
        }
      },
      "additionalProperties": false,
      "required": []
    },
    "work_request": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "github_read",
            "github_watch",
            "github_bind",
            "github_accept",
            "github_merge",
            "github_observe_merge",
            "github_merge_status"
          ]
        },
        "repository": {
          "type": "string",
          "maxLength": 256
        },
        "pr_number": {
          "type": "string",
          "pattern": "^[1-9][0-9]*$",
          "maxLength": 10
        },
        "binding_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 128
        },
        "agent_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 128
        },
        "clone": {
          "type": "string",
          "minLength": 1,
          "maxLength": 4096
        },
        "base": {
          "type": "string",
          "minLength": 1,
          "maxLength": 256
        },
        "head": {
          "type": "string",
          "minLength": 1,
          "maxLength": 64
        },
        "protected_refs": {
          "type": "array",
          "minItems": 1,
          "maxItems": 64,
          "items": {
            "type": "string",
            "maxLength": 256
          }
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "work_result": {
      "type": "object",
      "properties": {
        "repository": {
          "type": "string"
        },
        "pr_number": {
          "type": "string"
        },
        "head": {
          "type": "string"
        },
        "state": {
          "type": "string"
        },
        "ci": {
          "type": "string",
          "enum": [
            "pending",
            "success",
            "failure"
          ]
        },
        "cause": {
          "type": "string"
        },
        "retry_at": {
          "type": "string"
        },
        "done": {
          "type": "boolean"
        },
        "revision": {
          "type": "string"
        },
        "checks": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/github_check"
          }
        },
        "reviews": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/github_review"
          }
        },
        "binding_id": {
          "type": "string"
        },
        "base": {
          "type": "string"
        },
        "merge_commit": {
          "type": "string"
        },
        "source": {
          "type": "string"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "account_request": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "sign_in",
            "list",
            "sign_out"
          ]
        },
        "provider": {
          "type": "string",
          "const": "chatgpt"
        },
        "purpose": {
          "type": "string",
          "enum": [
            "login",
            "provider"
          ]
        },
        "account_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        }
      },
      "required": [],
      "additionalProperties": false,
      "allOf": [
        {
          "if": {
            "anyOf": [
              {
                "required": [
                  "provider"
                ]
              },
              {
                "required": [
                  "purpose"
                ]
              },
              {
                "required": [
                  "account_id"
                ]
              }
            ]
          },
          "then": {
            "required": [
              "operation"
            ]
          }
        },
        {
          "if": {
            "required": [
              "operation"
            ],
            "properties": {
              "operation": {
                "const": "sign_in"
              }
            }
          },
          "then": {
            "required": [
              "provider",
              "purpose"
            ]
          }
        },
        {
          "if": {
            "required": [
              "operation"
            ],
            "properties": {
              "operation": {
                "const": "sign_out"
              }
            }
          },
          "then": {
            "required": [
              "account_id"
            ]
          }
        }
      ]
    },
    "account_result": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "sign_in",
            "list",
            "sign_out"
          ]
        },
        "account_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "status": {
          "type": "string",
          "enum": [
            "ok",
            "needs_sign_in",
            "not_eligible",
            "usage_capped"
          ]
        },
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        },
        "accounts": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/account_summary"
          }
        }
      },
      "required": [],
      "additionalProperties": false,
      "allOf": [
        {
          "if": {
            "anyOf": [
              {
                "required": [
                  "account_id"
                ]
              },
              {
                "required": [
                  "status"
                ]
              }
            ]
          },
          "then": {
            "required": [
              "operation",
              "account_id",
              "status"
            ]
          }
        },
        {
          "if": {
            "required": [
              "operation"
            ],
            "properties": {
              "operation": {
                "enum": [
                  "sign_in",
                  "sign_out"
                ]
              }
            }
          },
          "then": {
            "required": [
              "account_id",
              "status"
            ]
          }
        },
        {
          "if": {
            "required": [
              "operation"
            ],
            "properties": {
              "operation": {
                "const": "list"
              }
            }
          },
          "then": {
            "required": [
              "accounts"
            ]
          }
        }
      ]
    },
    "memory_request": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "list",
            "remember",
            "correct",
            "forget"
          ]
        },
        "agent_id": {
          "type": "string",
          "minLength": 1
        },
        "item_id": {
          "type": "string",
          "minLength": 1
        },
        "text": {
          "type": "string",
          "minLength": 1
        },
        "holder": {
          "type": "string",
          "minLength": 1
        },
        "scope": {
          "type": "string",
          "enum": [
            "local",
            "private",
            "never-leaves"
          ]
        },
        "after": {
          "type": "string",
          "minLength": 1
        }
      },
      "additionalProperties": false
    },
    "memory_result": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "minLength": 1
        },
        "item_id": {
          "type": "string",
          "minLength": 1
        },
        "items": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/memory_summary"
          }
        },
        "next": {
          "type": "string",
          "minLength": 1
        }
      },
      "additionalProperties": false
    },
    "memory_import_progress": {
      "$comment": "Reserved: docs/design/host-protocol.md#deferred \u2014 The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.",
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "memory_promotion_batch": {
      "$comment": "Reserved: docs/design/host-protocol.md#deferred \u2014 The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.",
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "channel_request": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "list",
            "connect",
            "pair",
            "bind",
            "unbind",
            "disconnect"
          ]
        },
        "account": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "token": {
          "type": "string",
          "minLength": 1,
          "maxLength": 256
        },
        "chat": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "topic": {
          "type": "string",
          "maxLength": 512
        },
        "agent_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "scope": {
          "$ref": "#/$defs/channel_scope"
        },
        "platform": {
          "type": "string",
          "enum": [
            "telegram",
            "discord"
          ]
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "channel_result": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "list",
            "connect",
            "pair",
            "bind",
            "unbind",
            "disconnect"
          ]
        },
        "connections": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/channel_connection"
          }
        },
        "pairing_code": {
          "type": "string",
          "minLength": 1,
          "maxLength": 32
        },
        "expires_at": {
          "type": "string",
          "minLength": 1,
          "maxLength": 64
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "channel_state": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        },
        "connection": {
          "$ref": "#/$defs/channel_connection"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "agent_talk_get": {
      "type": "object",
      "properties": {
        "agent_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "agent_talk": {
      "type": "object",
      "properties": {
        "entries": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/agent_talk_line"
          }
        },
        "exchange_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "events": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/agent_talk_event"
          }
        },
        "next_cursor": {
          "type": "string",
          "maxLength": 64
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "agent_talk_entry": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        },
        "entry": {
          "$ref": "#/$defs/agent_talk_line"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "agent_talk_expand": {
      "type": "object",
      "properties": {
        "exchange_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "cursor": {
          "type": "string",
          "maxLength": 64
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "agent_talk_subscribe": {
      "$comment": "Reserved: docs/design/host-protocol.md#deferred \u2014 The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.",
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "connected": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        },
        "shell_file_tools_available": {
          "type": "boolean"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "disconnected": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "task_status": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "run_status": {
      "type": "object",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "text": {
          "type": "string"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "task_cancel": {
      "$comment": "Reserved: docs/design/host-protocol.md#deferred \u2014 The payload fields of the client 1.0 kinds are set by the implementation issue that ships the first conversation product.",
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "multi_task_control": {
      "type": "object",
      "properties": {
        "action": {
          "type": "string",
          "enum": [
            "stop_all"
          ]
        },
        "conversation_id": {
          "type": "string",
          "minLength": 1
        },
        "count": {
          "type": "integer",
          "minimum": 0
        }
      },
      "required": [
        "action"
      ],
      "additionalProperties": false
    },
    "sign_in_listen": {
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false,
      "$comment": "Request a listener for the fixed /auth/callback path before opening the browser."
    },
    "sign_in_listening": {
      "type": "object",
      "properties": {
        "port": {
          "type": "integer",
          "minimum": 1,
          "maximum": 65535
        },
        "cannot_listen": {
          "type": "string",
          "minLength": 1,
          "maxLength": 256
        }
      },
      "required": [],
      "additionalProperties": false,
      "allOf": [
        {
          "oneOf": [
            {
              "not": {
                "anyOf": [
                  {
                    "required": [
                      "port"
                    ]
                  },
                  {
                    "required": [
                      "cannot_listen"
                    ]
                  }
                ]
              }
            },
            {
              "required": [
                "port"
              ],
              "not": {
                "anyOf": [
                  {
                    "required": [
                      "cannot_listen"
                    ]
                  }
                ]
              }
            },
            {
              "required": [
                "cannot_listen"
              ],
              "not": {
                "anyOf": [
                  {
                    "required": [
                      "port"
                    ]
                  }
                ]
              }
            }
          ]
        }
      ]
    },
    "sign_in_open": {
      "type": "object",
      "properties": {
        "url": {
          "type": "string",
          "minLength": 1,
          "maxLength": 16384,
          "pattern": "^https?://[^\\s]+$"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "sign_in_redirect": {
      "type": "object",
      "properties": {
        "code": {
          "type": "string",
          "minLength": 1,
          "maxLength": 4096
        },
        "state": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "iss": {
          "type": "string",
          "minLength": 1,
          "maxLength": 2048
        },
        "error": {
          "type": "string",
          "minLength": 1,
          "maxLength": 256
        },
        "error_description": {
          "type": "string",
          "minLength": 1,
          "maxLength": 4096
        },
        "error_uri": {
          "type": "string",
          "minLength": 1,
          "maxLength": 2048
        },
        "url": {
          "type": "string",
          "minLength": 1,
          "maxLength": 16384
        }
      },
      "required": [],
      "additionalProperties": false,
      "allOf": [
        {
          "oneOf": [
            {
              "not": {
                "anyOf": [
                  {
                    "required": [
                      "code"
                    ]
                  },
                  {
                    "required": [
                      "state"
                    ]
                  },
                  {
                    "required": [
                      "iss"
                    ]
                  },
                  {
                    "required": [
                      "error"
                    ]
                  },
                  {
                    "required": [
                      "error_description"
                    ]
                  },
                  {
                    "required": [
                      "error_uri"
                    ]
                  },
                  {
                    "required": [
                      "url"
                    ]
                  }
                ]
              }
            },
            {
              "required": [
                "url"
              ],
              "not": {
                "anyOf": [
                  {
                    "required": [
                      "code"
                    ]
                  },
                  {
                    "required": [
                      "state"
                    ]
                  },
                  {
                    "required": [
                      "iss"
                    ]
                  },
                  {
                    "required": [
                      "error"
                    ]
                  },
                  {
                    "required": [
                      "error_description"
                    ]
                  },
                  {
                    "required": [
                      "error_uri"
                    ]
                  }
                ]
              }
            },
            {
              "required": [
                "code",
                "state"
              ],
              "not": {
                "anyOf": [
                  {
                    "required": [
                      "url"
                    ]
                  },
                  {
                    "required": [
                      "error"
                    ]
                  },
                  {
                    "required": [
                      "error_description"
                    ]
                  },
                  {
                    "required": [
                      "error_uri"
                    ]
                  }
                ]
              }
            },
            {
              "required": [
                "error",
                "state"
              ],
              "not": {
                "anyOf": [
                  {
                    "required": [
                      "url"
                    ]
                  },
                  {
                    "required": [
                      "code"
                    ]
                  }
                ]
              }
            }
          ]
        }
      ]
    },
    "approval_choice": {
      "type": "string",
      "enum": [
        "once",
        "deny",
        "always_allow"
      ]
    },
    "form_field_type": {
      "type": "string",
      "enum": [
        "text",
        "number",
        "boolean",
        "choice"
      ]
    },
    "form_field": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string"
        },
        "type": {
          "$ref": "#/$defs/form_field_type"
        },
        "choices": {
          "type": "array",
          "items": {
            "type": "string"
          },
          "minItems": 1
        }
      },
      "required": [
        "name",
        "type"
      ],
      "additionalProperties": false,
      "allOf": [
        {
          "if": {
            "properties": {
              "type": {
                "const": "choice"
              }
            }
          },
          "then": {
            "required": [
              "choices"
            ]
          },
          "else": {
            "not": {
              "required": [
                "choices"
              ]
            }
          }
        }
      ]
    },
    "question_form": {
      "type": "object",
      "properties": {
        "plugin_name": {
          "type": "string"
        },
        "fields": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/form_field"
          }
        }
      },
      "required": [
        "plugin_name",
        "fields"
      ],
      "additionalProperties": false
    },
    "form_value": {
      "oneOf": [
        {
          "type": "string"
        },
        {
          "type": "number"
        },
        {
          "type": "boolean"
        }
      ]
    },
    "form_values": {
      "type": "object",
      "additionalProperties": {
        "$ref": "#/$defs/form_value"
      }
    },
    "question_answer_action": {
      "type": "string",
      "enum": [
        "accept",
        "decline",
        "cancel"
      ]
    },
    "result_type": {
      "type": "string",
      "enum": [
        "summary",
        "table",
        "diagram",
        "chart",
        "document_draft",
        "comparison",
        "plan_view",
        "file"
      ]
    },
    "result_status": {
      "type": "string",
      "enum": [
        "preview",
        "saved"
      ]
    },
    "result_revision": {
      "type": "integer",
      "minimum": 1
    },
    "result_body": {
      "x-go-type": "json.RawMessage",
      "allOf": [
        {
          "type": "object",
          "properties": {
            "entries": {
              "type": "array",
              "items": {
                "$ref": "#/$defs/plan_entry"
              },
              "minItems": 1,
              "maxItems": 1000
            },
            "confirm": {
              "anyOf": [
                {
                  "type": "string",
                  "minLength": 1
                },
                {
                  "type": "null"
                }
              ]
            }
          },
          "required": [],
          "additionalProperties": false,
          "allOf": [
            {
              "if": {
                "anyOf": [
                  {
                    "required": [
                      "entries"
                    ]
                  },
                  {
                    "required": [
                      "confirm"
                    ]
                  }
                ]
              },
              "then": {
                "required": [
                  "entries",
                  "confirm"
                ]
              }
            }
          ]
        }
      ]
    },
    "saved_reference": {
      "$comment": "Reserved: docs/design/host-protocol.md#deferred \u2014 The JSON Schema of the result bodies, their size limits and the payload fields of the result kinds are set by the implementation issue that ships results.",
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "save_destination": {
      "$comment": "Reserved: docs/design/host-protocol.md#deferred \u2014 The JSON Schema of the result bodies, their size limits and the payload fields of the result kinds are set by the implementation issue that ships results.",
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "result_provenance": {
      "type": "object",
      "properties": {
        "agent_id": {
          "type": "string"
        },
        "conversation_id": {
          "type": "string"
        },
        "turn_id": {
          "type": "string"
        },
        "task_id": {
          "anyOf": [
            {
              "type": "string"
            },
            {
              "type": "null"
            }
          ]
        }
      },
      "required": [
        "agent_id",
        "conversation_id",
        "turn_id",
        "task_id"
      ],
      "additionalProperties": false
    },
    "stream_topic": {
      "type": "string",
      "pattern": "^(conversation:[\\s\\S]+|results:[\\s\\S]+|running|progress|waiting|exchanges|accounts|channels|core)$"
    },
    "stream_cursor": {
      "type": "object",
      "properties": {
        "epoch": {
          "$ref": "#/$defs/epoch"
        },
        "seq": {
          "type": "integer",
          "minimum": 0,
          "maximum": 18446744073709551615
        }
      },
      "required": [
        "epoch",
        "seq"
      ],
      "additionalProperties": false
    },
    "stream_metadata": {
      "type": "object",
      "properties": {
        "topic": {
          "$ref": "#/$defs/stream_topic"
        },
        "cursor": {
          "$ref": "#/$defs/stream_cursor"
        },
        "key": {
          "type": "string",
          "minLength": 1
        },
        "revision": {
          "type": "integer",
          "minimum": 1,
          "maximum": 18446744073709551615
        }
      },
      "required": [
        "topic",
        "cursor",
        "key",
        "revision"
      ],
      "additionalProperties": false
    },
    "stream_item": {
      "type": "object",
      "properties": {
        "kind": {
          "type": "string",
          "enum": [
            "output_delta",
            "item_done",
            "turn_state",
            "tool_activity",
            "approval_request",
            "question",
            "result",
            "running_update",
            "progress_update",
            "waiting_update",
            "agent_talk_entry",
            "account_result",
            "channel_state",
            "task_status",
            "run_status",
            "background_notice",
            "connected",
            "disconnected",
            "readiness_failure"
          ],
          "x-go-type": "Kind"
        },
        "key": {
          "type": "string",
          "minLength": 1
        },
        "revision": {
          "type": "integer",
          "minimum": 1,
          "maximum": 18446744073709551615
        },
        "payload": {
          "x-go-type": "json.RawMessage"
        }
      },
      "required": [
        "kind",
        "key",
        "revision",
        "payload"
      ],
      "additionalProperties": false,
      "allOf": [
        {
          "if": {
            "properties": {
              "kind": {
                "const": "output_delta"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/output_delta"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "item_done"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/item_done"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "turn_state"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/turn_state"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "tool_activity"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/tool_activity"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "approval_request"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/approval_request"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "question"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/question"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "result"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/result"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "running_update"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/running_update"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "progress_update"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/progress_update"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "waiting_update"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/waiting_update"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "agent_talk_entry"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/agent_talk_entry"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "account_result"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/account_result"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "channel_state"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/channel_state"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "task_status"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/task_status"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "run_status"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/run_status"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "background_notice"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/background_notice"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "connected"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/connected"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "disconnected"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/disconnected"
              }
            }
          }
        },
        {
          "if": {
            "properties": {
              "kind": {
                "const": "readiness_failure"
              }
            }
          },
          "then": {
            "properties": {
              "payload": {
                "$ref": "#/$defs/readiness_failure"
              }
            }
          }
        }
      ]
    },
    "snapshot": {
      "type": "object",
      "properties": {
        "topic": {
          "$ref": "#/$defs/stream_topic"
        },
        "cursor": {
          "$ref": "#/$defs/stream_cursor"
        },
        "revocation_epoch": {
          "$ref": "#/$defs/epoch"
        },
        "items": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/stream_item"
          }
        }
      },
      "required": [
        "topic",
        "cursor",
        "revocation_epoch",
        "items"
      ],
      "additionalProperties": false
    },
    "resync": {
      "type": "object",
      "properties": {
        "topic": {
          "$ref": "#/$defs/stream_topic"
        },
        "cursor": {
          "$ref": "#/$defs/stream_cursor"
        }
      },
      "required": [
        "topic",
        "cursor"
      ],
      "additionalProperties": false
    },
    "resync_required": {
      "type": "object",
      "properties": {
        "topic": {
          "$ref": "#/$defs/stream_topic"
        },
        "cursor": {
          "$ref": "#/$defs/stream_cursor"
        }
      },
      "required": [
        "topic",
        "cursor"
      ],
      "additionalProperties": false
    },
    "restart_mode": {
      "type": "string",
      "enum": [
        "when_idle",
        "drain"
      ]
    },
    "restart_request": {
      "type": "object",
      "properties": {
        "mode": {
          "$ref": "#/$defs/restart_mode"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "restart_get": {
      "type": "object",
      "properties": {},
      "required": [],
      "additionalProperties": false
    },
    "restart_status": {
      "type": "object",
      "properties": {
        "owed": {
          "type": "boolean"
        },
        "request_id": {
          "type": "string",
          "pattern": "^[0-9a-f]{32}$"
        },
        "mode": {
          "$ref": "#/$defs/restart_mode"
        },
        "requested_epoch": {
          "$ref": "#/$defs/epoch"
        },
        "committed_epoch": {
          "$ref": "#/$defs/epoch"
        },
        "discharged_epoch": {
          "$ref": "#/$defs/epoch"
        }
      },
      "required": [
        "owed"
      ],
      "additionalProperties": false
    },
    "input_mode": {
      "type": "string",
      "enum": [
        "steer",
        "queue"
      ]
    },
    "conversation_speaker": {
      "type": "string",
      "enum": [
        "person",
        "agent",
        "system",
        "forwarded"
      ]
    },
    "account_summary": {
      "type": "object",
      "properties": {
        "account_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "provider": {
          "type": "string"
        },
        "label": {
          "type": "string"
        },
        "status": {
          "type": "string",
          "enum": [
            "ok",
            "needs_sign_in",
            "not_eligible",
            "usage_capped"
          ]
        },
        "plan": {
          "type": "string"
        }
      },
      "required": [
        "account_id",
        "provider",
        "label",
        "status"
      ],
      "additionalProperties": false
    },
    "memory_summary": {
      "type": "object",
      "properties": {
        "item_id": {
          "type": "string",
          "minLength": 1
        },
        "content": {
          "type": "string",
          "minLength": 1
        },
        "holder": {
          "type": "string",
          "minLength": 1
        },
        "scope": {
          "type": "string",
          "minLength": 1
        },
        "revision": {
          "type": "integer",
          "minimum": 1
        },
        "evidence": {
          "type": "array",
          "items": {
            "type": "string",
            "minLength": 1
          }
        }
      },
      "additionalProperties": false,
      "required": [
        "item_id",
        "content",
        "holder",
        "scope",
        "revision",
        "evidence"
      ]
    },
    "channel_scope": {
      "type": "object",
      "properties": {
        "reads": {
          "type": "string",
          "enum": [
            "dm_only",
            "addressed",
            "all_messages"
          ]
        },
        "writes": {
          "type": "string",
          "enum": [
            "reply_only",
            "may_start"
          ]
        },
        "tools": {
          "type": "boolean"
        },
        "memory": {
          "type": "boolean"
        }
      },
      "required": [
        "reads",
        "writes",
        "tools",
        "memory"
      ],
      "additionalProperties": false
    },
    "channel_binding": {
      "type": "object",
      "properties": {
        "chat": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "topic": {
          "type": "string"
        },
        "agent_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "scope": {
          "$ref": "#/$defs/channel_scope"
        },
        "unreachable": {
          "type": "boolean"
        }
      },
      "required": [
        "chat",
        "topic",
        "agent_id",
        "scope",
        "unreachable"
      ],
      "additionalProperties": false
    },
    "channel_connection": {
      "type": "object",
      "properties": {
        "platform": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "account": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "status": {
          "type": "string",
          "enum": [
            "needs_pairing",
            "connected",
            "disconnected",
            "unavailable"
          ]
        },
        "private_chat": {
          "type": "string"
        },
        "bindings": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/channel_binding"
          }
        }
      },
      "required": [
        "platform",
        "account",
        "status",
        "private_chat",
        "bindings"
      ],
      "additionalProperties": false
    },
    "agent_talk_line": {
      "type": "object",
      "properties": {
        "exchange_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "from_agent": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "to_agent": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "subject": {
          "type": "string",
          "minLength": 1,
          "maxLength": 16384
        },
        "state": {
          "type": "string",
          "enum": [
            "queued",
            "delivered",
            "read",
            "answered"
          ]
        },
        "outcome": {
          "type": "string",
          "enum": [
            "pending",
            "acknowledged",
            "checked",
            "expired",
            "round_limit"
          ]
        },
        "needs_person": {
          "type": "boolean"
        },
        "updated_at": {
          "type": "string",
          "minLength": 1,
          "maxLength": 64
        }
      },
      "required": [
        "exchange_id",
        "from_agent",
        "to_agent",
        "subject",
        "state",
        "outcome",
        "needs_person",
        "updated_at"
      ],
      "additionalProperties": false
    },
    "agent_talk_event": {
      "type": "object",
      "properties": {
        "agent_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "kind": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        },
        "text": {
          "type": "string"
        },
        "at": {
          "type": "string",
          "minLength": 1,
          "maxLength": 64
        }
      },
      "required": [
        "agent_id",
        "kind",
        "text",
        "at"
      ],
      "additionalProperties": false
    },
    "growth_candidate": {
      "type": "object",
      "properties": {
        "id": {
          "type": "string",
          "minLength": 1
        },
        "expected": {
          "type": "string",
          "pattern": "^[1-9][0-9]{0,18}$"
        },
        "snapshot": {
          "type": "string",
          "maxLength": 262144
        },
        "notice": {
          "type": "string",
          "minLength": 1
        }
      },
      "required": [
        "id",
        "expected",
        "snapshot",
        "notice"
      ],
      "additionalProperties": false
    },
    "growth_notice": {
      "type": "object",
      "properties": {
        "revision": {
          "type": "string",
          "pattern": "^[1-9][0-9]{0,18}$"
        },
        "notice": {
          "type": "string",
          "minLength": 1
        },
        "undo_revision": {
          "type": "string",
          "pattern": "^[1-9][0-9]{0,18}$"
        }
      },
      "required": [
        "revision",
        "notice",
        "undo_revision"
      ],
      "additionalProperties": false
    },
    "replay_candidate": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string",
          "enum": [
            "core.md",
            "default-voice.md",
            "user-voice.md",
            "growth-snapshot.md"
          ]
        },
        "text": {
          "type": "string",
          "maxLength": 524288
        }
      },
      "required": [
        "name",
        "text"
      ],
      "additionalProperties": false
    },
    "export_request": {
      "type": "object",
      "properties": {
        "agent_id": {
          "type": "string",
          "maxLength": 512
        },
        "conversations": {
          "type": "array",
          "items": {
            "type": "string",
            "maxLength": 512
          },
          "maxItems": 100
        },
        "since": {
          "$ref": "#/$defs/utc"
        },
        "until": {
          "$ref": "#/$defs/utc"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "export_result": {
      "type": "object",
      "properties": {
        "directory": {
          "type": "string"
        },
        "manifest": {
          "type": "string",
          "maxLength": 524288
        }
      },
      "required": [
        "directory",
        "manifest"
      ],
      "additionalProperties": false
    },
    "replay_request": {
      "type": "object",
      "properties": {
        "turn_id": {
          "type": "string",
          "maxLength": 512
        },
        "episode": {
          "type": "string",
          "maxLength": 524288
        },
        "candidates": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/replay_candidate"
          },
          "maxItems": 4
        },
        "model": {
          "type": "string",
          "maxLength": 512
        },
        "effort": {
          "type": "string",
          "maxLength": 512
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "replay_result": {
      "type": "object",
      "properties": {
        "output": {
          "type": "string",
          "maxLength": 524288
        },
        "excluded_reason": {
          "type": "string",
          "maxLength": 512
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "decision_card": {
      "type": "object",
      "properties": {
        "id": {
          "type": "string",
          "minLength": 1
        },
        "revision": {
          "type": "string",
          "minLength": 1
        },
        "agent": {
          "type": "string"
        },
        "recommendation": {
          "type": "string"
        },
        "summary": {
          "type": "string"
        },
        "targets": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/decision_target"
          }
        },
        "outcome": {
          "$ref": "#/$defs/decision_outcome"
        }
      },
      "required": [
        "id",
        "revision",
        "agent",
        "summary",
        "targets"
      ],
      "additionalProperties": false
    },
    "decision_target": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string",
          "minLength": 1
        },
        "value": {
          "type": "string"
        }
      },
      "required": [
        "name",
        "value"
      ],
      "additionalProperties": false
    },
    "decision_outcome": {
      "type": "string",
      "enum": [
        "resolved",
        "already_resolved",
        "withdrawn",
        "expired",
        "stale_epoch",
        "stale_group",
        "refused",
        "stopped"
      ]
    },
    "turn_phase": {
      "type": "string",
      "enum": [
        "waiting_model",
        "streaming",
        "tool",
        "stopping"
      ]
    },
    "tool_activity_status": {
      "type": "string",
      "enum": [
        "started",
        "finished"
      ]
    },
    "tool_activity_outcome": {
      "type": "string",
      "enum": [
        "ok",
        "failed"
      ]
    },
    "tool_activity": {
      "type": "object",
      "description": "Display-only tool lifecycle event. Arguments and raw output excluded. Optional Core-approved display target and summary. Live/replay only; not conversation/model history.",
      "properties": {
        "stream": {
          "$ref": "#/$defs/stream_metadata"
        },
        "agent_id": {
          "type": "string",
          "minLength": 1
        },
        "call_id": {
          "type": "string"
        },
        "tool_name": {
          "type": "string",
          "description": "Trusted display catalog label, not a model-provided tool identifier."
        },
        "status": {
          "$ref": "#/$defs/tool_activity_status"
        },
        "outcome": {
          "$ref": "#/$defs/tool_activity_outcome"
        },
        "duration_ms": {
          "type": "number",
          "minimum": 0
        },
        "target": {
          "type": "string",
          "maxLength": 160,
          "description": "Optional Core-approved display target. Never inferred from raw arguments by clients."
        },
        "summary": {
          "type": "string",
          "maxLength": 8192,
          "description": "Optional sanitized display summary explicitly supplied by Core, never raw tool output."
        }
      },
      "required": [
        "call_id",
        "tool_name",
        "status"
      ],
      "additionalProperties": false,
      "allOf": [
        {
          "if": {
            "properties": {
              "status": {
                "const": "finished"
              }
            }
          },
          "then": {
            "required": [
              "outcome",
              "duration_ms"
            ]
          },
          "else": {
            "not": {
              "anyOf": [
                {
                  "required": [
                    "outcome"
                  ]
                },
                {
                  "required": [
                    "duration_ms"
                  ]
                }
              ]
            }
          }
        }
      ]
    },
    "github_check": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string"
        },
        "state": {
          "type": "string"
        },
        "conclusion": {
          "type": "string"
        }
      },
      "required": [
        "name",
        "state",
        "conclusion"
      ],
      "additionalProperties": false
    },
    "github_comment": {
      "type": "object",
      "properties": {
        "id": {
          "type": "string"
        },
        "body": {
          "type": "string"
        },
        "path": {
          "type": "string"
        },
        "url": {
          "type": "string"
        }
      },
      "required": [
        "id",
        "body",
        "path",
        "url"
      ],
      "additionalProperties": false
    },
    "github_review": {
      "type": "object",
      "properties": {
        "id": {
          "type": "string"
        },
        "resolved": {
          "type": "boolean"
        },
        "outdated": {
          "type": "boolean"
        },
        "comments": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/github_comment"
          }
        }
      },
      "required": [
        "id",
        "resolved",
        "outdated",
        "comments"
      ],
      "additionalProperties": false
    },
    "stop_result": {
      "type": "object",
      "properties": {
        "run_id": {
          "type": "string"
        },
        "outcome": {
          "$ref": "#/$defs/stop_outcome"
        }
      },
      "required": [
        "run_id",
        "outcome"
      ],
      "additionalProperties": false
    },
    "stop_outcome": {
      "type": "string",
      "enum": [
        "cancelled",
        "complete",
        "partial",
        "failed",
        "requested"
      ]
    },
    "plan_request": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "accept_goal",
            "accept_project",
            "get",
            "confirm"
          ]
        },
        "agent_id": {
          "type": "string",
          "minLength": 1
        },
        "title": {
          "type": "string",
          "minLength": 1,
          "maxLength": 200
        },
        "done": {
          "type": "string",
          "minLength": 1,
          "maxLength": 4096
        },
        "result_id": {
          "type": "string",
          "minLength": 1
        },
        "revision": {
          "$ref": "#/$defs/result_revision"
        }
      },
      "required": [
        "operation",
        "agent_id"
      ],
      "additionalProperties": false
    },
    "plan_response": {
      "type": "object",
      "properties": {
        "record_id": {
          "type": "string",
          "minLength": 1
        },
        "revision": {
          "type": "string",
          "minLength": 1
        },
        "result": {
          "$ref": "#/$defs/result"
        },
        "records": {
          "type": "array",
          "items": {
            "type": "string",
            "minLength": 1
          }
        },
        "effect_state": {
          "$ref": "#/$defs/effect_state"
        }
      },
      "required": [],
      "additionalProperties": false
    },
    "plan_entry": {
      "type": "object",
      "properties": {
        "id": {
          "type": "string",
          "minLength": 1
        },
        "kind": {
          "type": "string",
          "enum": [
            "goal",
            "project",
            "todo",
            "area",
            "memo",
            "resource"
          ]
        },
        "title": {
          "type": "string",
          "minLength": 1
        },
        "state": {
          "type": "string",
          "enum": [
            "planned",
            "active",
            "blocked",
            "done",
            "cancelled"
          ]
        },
        "parent": {
          "anyOf": [
            {
              "type": "string",
              "minLength": 1
            },
            {
              "type": "null"
            }
          ]
        },
        "done": {
          "type": "string"
        },
        "stage": {
          "type": "string"
        },
        "depends_on": {
          "type": "array",
          "items": {
            "type": "string",
            "minLength": 1
          },
          "maxItems": 999
        }
      },
      "required": [
        "id",
        "kind",
        "title",
        "state",
        "parent"
      ],
      "additionalProperties": false
    },
    "plan_body": {
      "type": "object",
      "properties": {
        "entries": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/plan_entry"
          },
          "minItems": 1,
          "maxItems": 1000
        },
        "confirm": {
          "anyOf": [
            {
              "type": "string",
              "minLength": 1
            },
            {
              "type": "null"
            }
          ]
        }
      },
      "required": [
        "entries",
        "confirm"
      ],
      "additionalProperties": false
    },
    "web_request": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "status",
            "set",
            "clear"
          ]
        },
        "key": {
          "type": "string",
          "minLength": 1,
          "maxLength": 512
        }
      },
      "required": [
        "operation"
      ],
      "additionalProperties": false
    },
    "web_result": {
      "type": "object",
      "properties": {
        "provider": {
          "type": "string",
          "enum": [
            "firecrawl"
          ]
        },
        "configured": {
          "type": "boolean"
        }
      },
      "required": [
        "provider",
        "configured"
      ],
      "additionalProperties": false
    },
    "worker_lane": {
      "type": "object",
      "properties": {
        "lane_id": {
          "type": "string"
        },
        "task_id": {
          "type": "string"
        },
        "agent_id": {
          "type": "string"
        },
        "agent_name": {
          "type": "string"
        },
        "worker": {
          "type": "string"
        },
        "thread_id": {
          "type": "string"
        },
        "turn_id": {
          "type": "string"
        },
        "node_id": {
          "type": "string"
        },
        "state": {
          "type": "string"
        },
        "title": {
          "type": "string"
        },
        "generation": {
          "type": "string"
        },
        "details": {
          "type": "array",
          "items": {
            "type": "string"
          }
        }
      },
      "required": [
        "lane_id",
        "task_id",
        "agent_id",
        "agent_name",
        "worker",
        "thread_id",
        "turn_id",
        "node_id",
        "state",
        "title",
        "generation",
        "details"
      ],
      "additionalProperties": false
    },
    "progress_line": {
      "type": "object",
      "properties": {
        "started_at": {
          "type": "string",
          "format": "date-time"
        },
        "finished_at": {
          "type": "string",
          "format": "date-time"
        },
        "now": {
          "type": "string"
        },
        "name": {
          "type": "string"
        },
        "kind": {
          "type": "string"
        },
        "state": {
          "$ref": "#/$defs/running_state"
        },
        "stage": {
          "type": "string"
        },
        "percent": {
          "type": "integer",
          "minimum": 0,
          "maximum": 100
        },
        "just_finished": {
          "type": "string"
        },
        "next": {
          "type": "string"
        },
        "waiting": {
          "type": "integer",
          "minimum": 0
        },
        "depth": {
          "type": "integer",
          "minimum": 0
        }
      },
      "required": [
        "name",
        "kind",
        "stage",
        "just_finished",
        "next",
        "waiting",
        "depth"
      ],
      "additionalProperties": false
    },
    "progress_row": {
      "type": "object",
      "properties": {
        "ref": {
          "type": "string"
        },
        "line": {
          "$ref": "#/$defs/progress_line"
        }
      },
      "required": [
        "ref",
        "line"
      ],
      "additionalProperties": false
    },
    "waiting_ref": {
      "type": "object",
      "properties": {
        "source": {
          "type": "string"
        },
        "key": {
          "type": "string"
        },
        "revision": {
          "type": "string"
        }
      },
      "required": [
        "source",
        "key",
        "revision"
      ],
      "additionalProperties": false
    },
    "waiting_line": {
      "type": "object",
      "properties": {
        "kind": {
          "type": "string"
        },
        "sentence": {
          "type": "string"
        },
        "context": {
          "type": "string"
        },
        "recommendation": {
          "type": "string"
        },
        "agent_name": {
          "type": "string"
        },
        "answers": {
          "type": "array",
          "items": {
            "type": "string"
          }
        },
        "free_text": {
          "type": "boolean"
        },
        "effort": {
          "type": "integer",
          "minimum": 0
        },
        "due": {
          "type": "string"
        }
      },
      "required": [
        "kind",
        "sentence",
        "context",
        "recommendation",
        "agent_name",
        "answers",
        "free_text",
        "effort",
        "due"
      ],
      "additionalProperties": false
    },
    "waiting_row": {
      "type": "object",
      "properties": {
        "ref": {
          "$ref": "#/$defs/waiting_ref"
        },
        "line": {
          "$ref": "#/$defs/waiting_line"
        }
      },
      "required": [
        "ref",
        "line"
      ],
      "additionalProperties": false
    },
    "running_state": {
      "type": "string",
      "enum": [
        "in_progress",
        "waiting",
        "needs_input",
        "ready_for_review",
        "failed",
        "stopped",
        "done",
        "unknown"
      ]
    },
    "running_reason": {
      "type": "string",
      "enum": [
        "dependency",
        "area",
        "quota",
        "host_capacity",
        "ci_pending",
        "run_limit",
        "person",
        "budget"
      ]
    },
    "record_request": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "maxLength": 16
        },
        "view": {
          "type": "string",
          "maxLength": 32
        },
        "search": {
          "type": "string",
          "maxLength": 512
        },
        "after": {
          "type": "string",
          "maxLength": 256
        },
        "day": {
          "type": "string",
          "maxLength": 10
        },
        "zone": {
          "type": "string",
          "maxLength": 128
        },
        "ref": {
          "type": "string",
          "maxLength": 256
        },
        "revision": {
          "type": "string",
          "maxLength": 64
        },
        "text": {
          "type": "string",
          "maxLength": 4096
        },
        "token": {
          "type": "string",
          "maxLength": 128
        }
      },
      "required": [
        "operation"
      ],
      "additionalProperties": false
    },
    "record_chip": {
      "type": "object",
      "properties": {
        "kind": {
          "type": "string",
          "maxLength": 32
        },
        "value": {
          "type": "string",
          "maxLength": 4096
        }
      },
      "required": [
        "kind",
        "value"
      ],
      "additionalProperties": false
    },
    "record_row": {
      "type": "object",
      "properties": {
        "ref": {
          "type": "string",
          "maxLength": 256
        },
        "revision": {
          "type": "string",
          "maxLength": 64
        },
        "kind": {
          "type": "string",
          "maxLength": 64
        },
        "state": {
          "type": "string",
          "maxLength": 64
        },
        "title": {
          "type": "string",
          "maxLength": 512
        },
        "note": {
          "type": "string",
          "maxLength": 32768
        },
        "chips": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/record_chip"
          }
        },
        "overdue": {
          "type": "boolean"
        },
        "blocked": {
          "type": "boolean"
        }
      },
      "required": [
        "ref",
        "revision",
        "kind",
        "state",
        "title",
        "note",
        "chips",
        "overdue",
        "blocked"
      ],
      "additionalProperties": false
    },
    "record_result": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "maxLength": 16
        },
        "rows": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/record_row"
          },
          "maxItems": 50
        },
        "next": {
          "type": "string",
          "maxLength": 256
        },
        "token": {
          "type": "string",
          "maxLength": 128
        },
        "code": {
          "type": "string",
          "maxLength": 64
        }
      },
      "required": [
        "operation",
        "rows"
      ],
      "additionalProperties": false
    },
    "work_switch_request": {
      "type": "object",
      "properties": {
        "operation": {
          "type": "string",
          "enum": [
            "approve_switch",
            "activate_switch",
            "rollback_switch",
            "release_switch",
            "switch_status"
          ]
        },
        "switch_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 256
        },
        "tasks": {
          "type": "array",
          "minItems": 1,
          "maxItems": 256,
          "items": {
            "type": "string",
            "minLength": 1,
            "maxLength": 256
          }
        },
        "snapshot": {
          "type": "string",
          "pattern": "^[a-f0-9]{64}$"
        },
        "evidence": {
          "type": "string",
          "minLength": 1,
          "maxLength": 4096
        }
      },
      "required": [
        "operation",
        "switch_id"
      ],
      "additionalProperties": false
    },
    "work_switch_result": {
      "type": "object",
      "properties": {
        "switch_id": {
          "type": "string",
          "minLength": 1,
          "maxLength": 256
        },
        "state": {
          "type": "string",
          "enum": [
            "approved",
            "active",
            "rollback_pending",
            "rolled_back"
          ]
        },
        "approval": {
          "type": "string",
          "minLength": 1,
          "maxLength": 256
        },
        "snapshot": {
          "type": "string"
        },
        "tasks": {
          "type": "array",
          "items": {
            "type": "string",
            "minLength": 1,
            "maxLength": 256
          }
        },
        "events": {
          "type": "array",
          "items": {
            "$ref": "#/$defs/work_switch_event"
          }
        }
      },
      "required": [
        "switch_id",
        "state",
        "approval",
        "snapshot",
        "tasks",
        "events"
      ],
      "additionalProperties": false
    },
    "work_switch_event": {
      "type": "object",
      "properties": {
        "state": {
          "type": "string",
          "minLength": 1,
          "maxLength": 256
        },
        "evidence": {
          "type": "string"
        }
      },
      "required": [
        "state",
        "evidence"
      ],
      "additionalProperties": false
    },
    "completion_state": {
      "type": "string",
      "enum": [
        "done",
        "claimed"
      ]
    },
    "completion_status": {
      "type": "object",
      "properties": {
        "state": {
          "$ref": "#/$defs/completion_state"
        },
        "label": {
          "type": "string"
        }
      },
      "required": [
        "state",
        "label"
      ],
      "additionalProperties": false
    }
  }
}
`
