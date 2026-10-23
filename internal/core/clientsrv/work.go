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
