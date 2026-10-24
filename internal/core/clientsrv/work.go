// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

var WorkCapability = protocol.Capability{Name: "work", Version: "1.0"}

type WorkService interface {
	Work(context.Context, Session, protocol.WorkSwitchRequest, string) (protocol.WorkSwitchResult, error)
}

func WorkOperations(service WorkService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindWorkSwitchRequest: {Capability: WorkCapability, Handle: func(ctx context.Context, s Session, e protocol.Envelope) (Result, error) {
		p, ok := e.Payload.(protocol.WorkSwitchRequest)
		if !ok {
			return Result{}, &OperationError{Code: protocol.ErrorMalformed}
		}
		if !s.CanDecide {
			return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
		}
		answer, err := service.Work(ctx, s, p, e.MessageID)
		effect := protocol.EffectApplied
		if p.Operation == "switch_status" {
			effect = protocol.EffectNone
		}
		return Result{Kind: protocol.KindWorkSwitchResult, Payload: answer, Effect: effect}, err
	}}}
}

// WorkControlService records the person's order, pause, resume and stop choices.
// It reports whether the choice changed the goal, so a choice that recorded
// nothing is not answered as an applied effect.
type WorkControlService interface {
	WorkControl(context.Context, Session, protocol.WorkControlRequest, string) (protocol.WorkControlResult, bool, error)
}

// WorkControlOperations installs the control kinds under the negotiated work
// capability. A control changes only the ledger projection; the next scheduling
// pass applies it.
func WorkControlOperations(service WorkControlService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindWorkControlRequest: {Capability: WorkCapability, Handle: func(ctx context.Context, s Session, e protocol.Envelope) (Result, error) {
		p, ok := e.Payload.(protocol.WorkControlRequest)
		if !ok {
			return Result{}, &OperationError{Code: protocol.ErrorMalformed}
		}
		if !s.CanDecide {
			return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
		}
		answer, applied, err := service.WorkControl(ctx, s, p, e.MessageID)
		effect := protocol.EffectNone
		if applied {
			effect = protocol.EffectApplied
		}
		return Result{Kind: protocol.KindWorkControlResult, Payload: answer, Effect: effect}, err
	}}}
}

// WorkLimitService records the person's run limit choice. It reports whether the
// choice changed the limit, so a repeated choice is not answered as applied.
type WorkLimitService interface {
	WorkLimit(context.Context, Session, protocol.WorkLimitRequest, string) (protocol.WorkLimitResult, bool, error)
}

// WorkLimitOperations installs the run limit kind under the negotiated work
// capability. The choice changes the durable limit only; the next scheduling
// pass applies it, exactly as a change of capacity does.
func WorkLimitOperations(service WorkLimitService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindWorkLimitRequest: {Capability: WorkCapability, Handle: func(ctx context.Context, s Session, e protocol.Envelope) (Result, error) {
		p, ok := e.Payload.(protocol.WorkLimitRequest)
		if !ok {
			return Result{}, &OperationError{Code: protocol.ErrorMalformed}
		}
		if !s.CanDecide || s.PersonID == "" {
			return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
		}
		answer, applied, err := service.WorkLimit(ctx, s, p, e.MessageID)
		effect := protocol.EffectNone
		if applied {
			effect = protocol.EffectApplied
		}
		return Result{Kind: protocol.KindWorkLimitResult, Payload: answer, Effect: effect}, err
	}}}
}
