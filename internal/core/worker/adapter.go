// SPDX-License-Identifier: Apache-2.0

// Package worker is Core's boundary to installed coding tools. Adapters use the
// person's setup; they neither receive provider credentials nor impose settings.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// Adapter owns its protocol session and event stream. Start retains the activity
// lease for the session's work; the caller releases it after work has ended.
// Events are ordered observations, never proof that a product effect succeeded.
// An adapter must signal lost observations as a connection failure and let Core
// reconcile with Snapshot. It must not silently drop completion or requests.
type Adapter interface {
	Name() string
	Version(context.Context) (string, error)
	Capabilities() CapabilitySet
	Start(context.Context, lease.Lease, StartSpec) (Thread, error)
	Input(context.Context, Thread, Input) error
	Interrupt(context.Context, Thread, Reason) error
	Events() <-chan Event
	Answer(context.Context, Thread, Answer) error
	Quota(context.Context) ([]provider.QuotaRow, error)
	Snapshot(context.Context, Thread) (ThreadSnapshot, error)
}

type Capability string

const (
	StreamProgress   Capability = "progress"
	ReportUsage      Capability = "usage"
	SignalCompletion Capability = "completion"
	AskQuestions     Capability = "questions"
	RequestApprovals Capability = "approvals"
	WorkTools        Capability = "work_tools"
	ReportQuota      Capability = "quota"
	SteerTurn        Capability = "steer"
)

type CapabilitySet []Capability

// Require checks the dispatch's needs against the declaration at thread start.
// Unknown capabilities are never evidence that an adapter implements a feature.
func (s CapabilitySet) Require(required ...Capability) error {
	for _, capability := range required {
		switch capability {
		case StreamProgress, ReportUsage, SignalCompletion, AskQuestions, RequestApprovals, WorkTools, ReportQuota, SteerTurn:
		default:
			return fmt.Errorf("unknown worker capability %q", capability)
		}
		found := false
		for _, have := range s {
			if have == capability {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("worker lacks capability %q", capability)
		}
	}
	return nil
}

// StartSpec contains only per-task inputs. Empty model/effort use the worker's
// configuration. There is deliberately no provider credential or sandbox field.
type StartSpec struct {
	TaskID, AttemptID, Workdir, TaskPacket string
	Model                                  provider.ModelRef
	Effort                                 provider.Effort
	Tools                                  []Tool
	Required                               CapabilitySet
}

type Tool struct {
	Namespace, Name, Description string
	InputSchema                  json.RawMessage
}

// Thread is an identity bound by Core. Every control operation rechecks all four
// coordinates (ID, TurnID, ProcessGeneration, BindingEpoch) before sending input.
// Task/attempt attribution comes from Core, never from a worker's tool arguments.
type Thread struct {
	ID, TurnID, TaskID, AttemptID   string
	ProcessGeneration, BindingEpoch uint64
}

// Input continues an idle thread or steers Thread.TurnID when it is active.
// Pending questions/approvals block ordinary input. Cancellation of the call's
// context is not a request to interrupt the worker's turn.
type Input struct{ Text string }

// ErrInputRefused means the adapter rejected input before sending a request.
// It must never describe a transport failure or an ambiguous remote outcome.
var ErrInputRefused = errors.New("worker input refused before dispatch")

// ErrNeedsSignIn is a definitive authentication refusal during Start, before
// any prompt or task input. The adapter must close and join its partial child
// before returning it. Other start errors can have unknown effects.
var ErrNeedsSignIn = errors.New("worker needs sign-in")

type Reason string

const (
	UserCancel       Reason = "user"
	BudgetCap        Reason = "budget_cap"
	NodePolicyCancel Reason = "node_policy"
	StallRecovery    Reason = "stall_recovery"
)

type ThreadState string

const (
	Idle    ThreadState = "idle"
	Active  ThreadState = "active"
	Waiting ThreadState = "waiting"
	Dead    ThreadState = "dead"
	Unknown ThreadState = "unknown"
)

type ThreadSnapshot struct {
	// Output is the latest completed assistant message in the current turn.
	Output                     string
	Thread                     Thread
	State                      ThreadState
	Worker, Version, Workdir   string
	Model                      provider.ModelRef
	Effort                     provider.Effort
	Capabilities               CapabilitySet
	InstructionSources, Skills []string
	Pending                    []Request
}

// Request preserves the worker-specific request body. The adapter keeps the
// original wire ID behind this opaque ID, including whether it was string/int.
// Core routes it to the originating conversation without rewriting arguments.
type Request struct {
	ID, Method string
	Params     json.RawMessage
}

type Answer struct {
	RequestID string
	Result    json.RawMessage
}

type ToolCall struct {
	// CallID preserves the worker wire call id; ID is the opaque Answer request id.
	CallID              string
	ID, Namespace, Name string
	Arguments           json.RawMessage
}
