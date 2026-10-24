// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// WebexCapability covers the person's own messenger accounts: connecting
// one, signing in to its platform and disconnecting it. Reading the sources is
// Core's own work and needs no capability.
var WebexCapability = protocol.Capability{Name: "webex", Version: "1.0"}

// WebexService is Core's control flow for messenger sources. The socket
// adapter carries authenticated session facts; a payload never names the person
// or the account it belongs to.
type WebexService interface {
	// Messenger answers one control request. Sign-in finishes through the
	// shared browser relay, so its answer is the relay's result.
	Messenger(context.Context, Session, protocol.WebexRequest, *SignInRelay) (Result, error)
}

func WebexOperations(service WebexService, relay *SignInRelay) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindWebexRequest: {
		Capability: WebexCapability,
		Handle: func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
			p, ok := e.Payload.(protocol.WebexRequest)
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
