// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// MiniAppService is the person-facing registry and host surface. It lists the
// person's apps, opens one through a one-time link and records the person's
// allow, deny, keep, undo, restore and remove choices.
type MiniAppService interface {
	MiniApp(context.Context, Session, protocol.MiniAppRequest) (protocol.MiniAppResult, error)
}

func MiniAppOperations(service MiniAppService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindMiniAppRequest: {Capability: protocol.Capability{Name: "mini_apps", Version: "1.0"}, Handle: func(ctx context.Context, s Session, e protocol.Envelope) (Result, error) {
		p, ok := e.Payload.(protocol.MiniAppRequest)
		if !ok {
			return Result{}, &OperationError{Code: protocol.ErrorMalformed}
		}
		answer, err := service.MiniApp(ctx, s, p)
		effect := protocol.EffectApplied
		switch p.Operation {
		case "list", "open", "history":
			effect = protocol.EffectNone
		}
		return Result{Kind: protocol.KindMiniAppResult, Payload: answer, Effect: effect}, err
	}}}
}
