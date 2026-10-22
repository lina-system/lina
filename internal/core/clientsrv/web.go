// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

type WebService interface {
	Web(context.Context, Session, protocol.WebRequest) (protocol.WebResult, error)
}

func WebOperations(service WebService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindWebRequest: {Capability: protocol.Capability{Name: "web", Version: "1.0"}, Handle: func(ctx context.Context, s Session, e protocol.Envelope) (Result, error) {
		p, ok := e.Payload.(protocol.WebRequest)
		if !ok {
			return Result{}, &OperationError{Code: protocol.ErrorMalformed}
		}
		result, err := service.Web(ctx, s, p)
		effect := protocol.EffectNone
		if err == nil && p.Operation != "status" {
			effect = protocol.EffectApplied
		}
		return Result{Kind: protocol.KindWebResult, Payload: result, Effect: effect}, err
	}}}
}
