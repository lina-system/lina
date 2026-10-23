// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// LauncherCapability admits a launcher line and its short replies. The line
// lands in an agent's main conversation; the launcher owns no conversation.
var LauncherCapability = protocol.Capability{Name: "launcher", Version: "1.0"}

// LauncherService resolves the last or named agent and submits the line to its
// main conversation through the existing admission and turn flow.
type LauncherService interface {
	Launcher(context.Context, Session, protocol.LauncherRequest) (protocol.LauncherResult, error)
}

func LauncherOperations(service LauncherService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindLauncherRequest: {
		Capability: LauncherCapability,
		Handle: func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
			p, ok := e.Payload.(protocol.LauncherRequest)
			if service == nil || !ok || p.RequestID == "" || p.Text == "" {
				return Result{}, &OperationError{Code: protocol.ErrorMalformed}
			}
			answer, err := service.Launcher(ctx, session, p)
			if err != nil {
				return Result{}, err
			}
			return Result{Kind: protocol.KindLauncherResult, Payload: answer, Effect: protocol.EffectApplied}, nil
		},
	}}
}
