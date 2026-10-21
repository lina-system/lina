// SPDX-License-Identifier: Apache-2.0

// Package provider defines the model boundary shared by Core and its adapters.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/secret"
)

type ProviderID string

const (
	ChatGPT ProviderID = "chatgpt"
	Claude  ProviderID = "claude"
	Grok    ProviderID = "grok"
	Gemini  ProviderID = "gemini"
)

type Class uint8

const (
	ModelAPI Class = iota + 1
	AgentRuntime
)

type AuthKind uint8

const (
	LoopbackOAuth AuthKind = iota + 1
	LongLivedToken
	APIKey
	ObservedCLILogin
)

type AccountID string
type ModelRef string
type Effort string

type RunKind string

const (
	Turn            RunKind = "turn"
	Background      RunKind = "background"
	BackgroundJob   RunKind = "background_job"
	Worker          RunKind = "worker"
	WorkerSubthread RunKind = "worker_subthread"
	Auxiliary       RunKind = "auxiliary"
	Replay          RunKind = "replay"
)

func (k RunKind) Valid() bool {
	switch k {
	case Turn, Background, BackgroundJob, Worker, WorkerSubthread, Auxiliary, Replay:
		return true
	}
	return false
}

type FailureKind string

const (
	NeedsSignIn FailureKind = "needs_sign_in"
	NotEligible FailureKind = "not_eligible"
	UsageCapped FailureKind = "usage_capped"
	Unavailable FailureKind = "unavailable"
	NotAccepted FailureKind = "not_accepted"
)

func (k FailureKind) Valid() bool {
	switch k {
	case NeedsSignIn, NotEligible, UsageCapped, Unavailable, NotAccepted:
		return true
	}
	return false
}
func (k FailureKind) MarshalJSON() ([]byte, error) {
	if !k.Valid() {
		return nil, errors.New("invalid provider failure")
	}
	return json.Marshal(string(k))
}
func (k *FailureKind) UnmarshalJSON(b []byte) error {
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return err
	}
	next := FailureKind(text)
	if !next.Valid() {
		return errors.New("invalid provider failure")
	}
	*k = next
	return nil
}

// Failure never retains a provider body or message. Code and Type are the
// provider's machine-readable identifiers, for classification rather than logs.
type Failure struct {
	Kind                  FailureKind
	Code, Type, RequestID string
	Status                int
}

func (f *Failure) Error() string {
	if !f.Kind.Valid() {
		return "invalid_provider_failure"
	}
	return string(f.Kind)
}

// Credential is supplied per call; adapters never open the secret store.
// Its bearer can only be applied to a request, and cannot be printed or encoded.
type Credential struct {
	Account AccountID
	Kind    AuthKind
	bearer  secret.Secret
}

func NewCredential(account AccountID, kind AuthKind, bearer string) Credential {
	return Credential{Account: account, Kind: kind, bearer: secret.New(bearer)}
}
func (Credential) String() string   { return "[credential]" }
func (Credential) GoString() string { return "[credential]" }
func (c Credential) Authorize(r *http.Request) error {
	if err := secret.InjectHeader(r.Header, "Authorization", "Bearer ", c.bearer); err != nil {
		return &Failure{Kind: NeedsSignIn}
	}
	return nil
}

type Adapter interface {
	Provider() ProviderID
	Class() Class
	Models(context.Context, lease.Lease, Credential) (ModelList, error)
	Quota(context.Context, Credential) ([]QuotaRow, error)
	Stream(context.Context, lease.Lease, Request) (Stream, error)
}
type ModelList struct {
	Models   []ModelRef
	Observed time.Time
}

// Item retains the exact provider JSON, including opaque reasoning state.
type Item = json.RawMessage
type ToolDecl = json.RawMessage
type FieldList []string
type Request struct {
	Credential   Credential
	Model        ModelRef
	Effort       Effort
	Instructions string
	Items        []Item
	Tools        []ToolDecl
	Fields       FieldList
	Purpose      RunKind
	Bridge       ToolBridge
}
type ToolBridge interface {
	Call(context.Context, ToolCall) (ToolResult, error)
}
type ToolCall struct {
	ID, Name, Arguments string
	Custom              bool
}
type ToolResult struct {
	ID     string
	Output Item
}
type EventKind uint8

const (
	// Delta contains visible output text only, never tool arguments or input.
	Delta EventKind = iota + 1
	ItemDone
	ToolCallReady
	Terminal
)

type Event struct {
	Kind  EventKind
	Index int64
	Delta string
	Item  Item
	Call  *ToolCall
}
type Stream interface {
	Next(context.Context) (Event, error)
	Meta() Meta
	Close() error
}
type Meta struct {
	RequestID        string
	Class            Class
	Usage            Usage
	RuntimeSessionID string
}

// Money is a reported estimate in millionths of the named currency.
type Money struct {
	Currency string `json:"currency"`
	Micros   int64  `json:"micros"`
}
type Usage struct {
	Input        *int64 `json:"input"`
	Cached       *int64 `json:"cached"`
	CacheWrite   *int64 `json:"cache_write"`
	Output       *int64 `json:"output"`
	Reasoning    *int64 `json:"reasoning"`
	CostEstimate *Money `json:"cost_estimate"`
}

func (u Usage) Valid() bool {
	for _, n := range []*int64{u.Input, u.Cached, u.CacheWrite, u.Output, u.Reasoning} {
		if n != nil && *n < 0 {
			return false
		}
	}
	return u.CostEstimate == nil || u.CostEstimate.Micros >= 0 && u.CostEstimate.Currency != ""
}

type Window string

const (
	UnknownWindow Window = "unknown"
	FiveHour      Window = "five_hour"
	Weekly        Window = "weekly"
	Daily         Window = "daily"
	PerMinute     Window = "per_minute"
	SpendCap      Window = "spend_cap"
)

type QuotaState string

const (
	QuotaUnknown QuotaState = "unknown"
	QuotaOK      QuotaState = "ok"
	QuotaWarning QuotaState = "warning"
	QuotaBlocked QuotaState = "blocked"
)

type QuotaSource string

const (
	ResponseHeader QuotaSource = "response_header"
	ProviderEvent  QuotaSource = "event"
	Endpoint       QuotaSource = "endpoint"
	ErrorCode      QuotaSource = "error_code"
	OwnCount       QuotaSource = "own_count"
)

type QuotaRow struct {
	Provider ProviderID
	Account  AccountID
	Window   Window
	UsedPct  *float64
	ResetsAt *time.Time
	State    QuotaState
	Source   QuotaSource
	Observed time.Time
}
