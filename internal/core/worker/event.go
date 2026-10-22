// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/kit/provider"
)

type EventKind uint8

const (
	ProgressEvent EventKind = iota + 1
	UsageEvent
	FinishEvent
	QuestionEvent
	ApprovalEvent
	ToolCallEvent
)

type Event struct {
	Thread   Thread
	Kind     EventKind
	Progress *Progress
	Usage    *provider.Usage
	Finish   *Finish
	Question *Request
	Approval *Request
	ToolCall *ToolCall
}

type Progress struct {
	ItemID, Phase, Text string
}

type FinishStatus string

const (
	Done        FinishStatus = "done"
	Failed      FinishStatus = "failed"
	Interrupted FinishStatus = "interrupted"
)

type FailureKind string

const (
	QuotaFailure      FailureKind = "quota"
	RateLimitFailure  FailureKind = "rate_limit"
	LoginFailure      FailureKind = "login_expiry"
	ConnectionFailure FailureKind = "connection"
	WorkerFailure     FailureKind = "worker"
)

// Finish ends a turn, not the task. Only a receipt and Core's verification can
// establish delivery. Interrupted is emitted after the worker confirms it.
type Finish struct {
	Status  FinishStatus
	Failure FailureKind
	Code    string
}

func (f Finish) valid() bool {
	switch f.Status {
	case Done, Interrupted:
		return f.Failure == ""
	case Failed:
		switch f.Failure {
		case QuotaFailure, RateLimitFailure, LoginFailure, ConnectionFailure, WorkerFailure:
			return true
		}
	}
	return false
}

// Validate is the common ingress check. The exhaustive analyzer guards this
// switch so adding an event kind also requires defining its payload handling.
func (e Event) Validate() error {
	count := 0
	for _, present := range []bool{e.Progress != nil, e.Usage != nil, e.Finish != nil, e.Question != nil, e.Approval != nil, e.ToolCall != nil} {
		if present {
			count++
		}
	}
	if count != 1 || e.Thread.ID == "" {
		return errors.New("worker event requires one payload and a thread")
	}
	valid := false
	switch e.Kind {
	case ProgressEvent:
		valid = e.Progress != nil
	case UsageEvent:
		valid = e.Usage != nil && e.Usage.Valid()
	case FinishEvent:
		valid = e.Finish != nil && e.Finish.valid()
	case QuestionEvent:
		valid = validRequest(e.Question)
	case ApprovalEvent:
		valid = validRequest(e.Approval)
	case ToolCallEvent:
		valid = e.ToolCall != nil && e.ToolCall.ID != "" && e.ToolCall.Name != "" && json.Valid(e.ToolCall.Arguments)
	}
	if !valid {
		return errors.New("invalid worker event kind or payload")
	}
	return nil
}

func validRequest(r *Request) bool {
	return r != nil && r.ID != "" && r.Method != "" && json.Valid(r.Params)
}
