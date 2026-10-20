// SPDX-License-Identifier: Apache-2.0
package diag

import "encoding/json"

// Each enum has a closed wire vocabulary. Unknown numeric values fail before
// persistence; there is no conversion from arbitrary text to an emitter field.

type Component uint8

const (
	Conversation Component = iota + 1
	Task
	Memory
	Materials
	Node
	App
	OS
)

var componentNames = [...]string{"", "conversation", "task", "memory", "materials", "node", "app", "os"}

func (v Component) valid() bool { return int(v) < len(componentNames) && v != 0 }
func (v Component) MarshalJSON() ([]byte, error) {
	if !v.valid() {
		return nil, ErrInvalid
	}
	return json.Marshal(componentNames[v])
}
func (v *Component) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return ErrInvalid
	}
	for i, name := range componentNames {
		if name != "" && text == name {
			*v = Component(i)
			return nil
		}
	}
	return ErrInvalid
}

type Result uint8

const (
	Pending Result = iota + 1
	Success
	Failure
	Cancelled
	Denied
	Unknown
)

var resultNames = [...]string{"", "pending", "success", "failure", "cancelled", "denied", "unknown"}

func (v Result) valid() bool { return int(v) < len(resultNames) && v != 0 }
func (v Result) MarshalJSON() ([]byte, error) {
	if !v.valid() {
		return nil, ErrInvalid
	}
	return json.Marshal(resultNames[v])
}
func (v *Result) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return ErrInvalid
	}
	for i, name := range resultNames {
		if name != "" && text == name {
			*v = Result(i)
			return nil
		}
	}
	return ErrInvalid
}

type CauseCode uint8

const (
	BootStarted CauseCode = iota + 1
	TurnStarted
	TaskStarted
	OperationStarted
	OperationSucceeded
	OperationFailed
	ModelFailed
	TaskStopped
	StorageFailed
	PermissionDenied
	NetworkFailed
	EffectUnknown
)

var causecodeNames = [...]string{"", "boot_started", "turn_started", "task_started", "operation_started", "operation_succeeded", "operation_failed", "model_failed", "task_stopped", "storage_failed", "permission_denied", "network_failed", "effect_unknown"}

func (v CauseCode) valid() bool { return int(v) < len(causecodeNames) && v != 0 }
func (v CauseCode) MarshalJSON() ([]byte, error) {
	if !v.valid() {
		return nil, ErrInvalid
	}
	return json.Marshal(causecodeNames[v])
}
func (v *CauseCode) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return ErrInvalid
	}
	for i, name := range causecodeNames {
		if name != "" && text == name {
			*v = CauseCode(i)
			return nil
		}
	}
	return ErrInvalid
}

type Action uint8

const (
	None Action = iota + 1
	Continue
	ReportFailure
	Reconcile
	RequestApproval
)

var actionNames = [...]string{"", "none", "continue", "report_failure", "reconcile", "request_approval"}

func (v Action) valid() bool { return int(v) < len(actionNames) && v != 0 }
func (v Action) MarshalJSON() ([]byte, error) {
	if !v.valid() {
		return nil, ErrInvalid
	}
	return json.Marshal(actionNames[v])
}
func (v *Action) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return ErrInvalid
	}
	for i, name := range actionNames {
		if name != "" && text == name {
			*v = Action(i)
			return nil
		}
	}
	return ErrInvalid
}

type ErrorClass uint8

const (
	NoError ErrorClass = iota
	Model
	Interrupted
	Storage
	Permission
	Network
	Internal
)

var errorclassNames = [...]string{"", "model", "interrupted", "storage", "permission", "network", "internal"}

func (v ErrorClass) valid() bool { return int(v) < len(errorclassNames) }
func (v ErrorClass) MarshalJSON() ([]byte, error) {
	if !v.valid() {
		return nil, ErrInvalid
	}
	if v == NoError {
		return []byte("null"), nil
	}
	return json.Marshal(errorclassNames[v])
}
func (v *ErrorClass) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*v = NoError
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return ErrInvalid
	}
	for i, name := range errorclassNames {
		if name != "" && text == name {
			*v = ErrorClass(i)
			return nil
		}
	}
	return ErrInvalid
}
