// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// MessengerCapability covers the person's own messenger accounts: connecting
// one, signing in to its platform and disconnecting it. Reading the sources is
// Core's own work and needs no capability.
var MessengerCapability = protocol.Capability{Name: "messengers", Version: "1.0"}

// MessengerService is Core's control flow for messenger sources. The socket
// adapter carries authenticated session facts; a payload never names the person
// or the account it belongs to.
type MessengerService interface {
	// Messenger answers one control request. Sign-in finishes through the
	// shared browser relay, so its answer is the relay's result.
	Messenger(context.Context, Session, protocol.MessengerRequest, *SignInRelay) (Result, error)
}

func MessengerOperations(service MessengerService, relay *SignInRelay) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindMessengerRequest: {
		Capability: MessengerCapability,
		Handle: func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
			p, ok := e.Payload.(protocol.MessengerRequest)
			if !ok || p.Operation == nil {
				return Result{}, &OperationError{Code: protocol.ErrorMalformed}
			}
			if service == nil {
				return Result{}, &OperationError{Code: protocol.ErrorUnavailable}
			}
			if *p.Operation != "list" && !session.CanDecide {
				return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
			}
			// Sign-in shares the client's browser relay and finishes with the
			// relay's own result; every other operation answers at once.
			return service.Messenger(ctx, session, p, relay)
		},
	}}
}
