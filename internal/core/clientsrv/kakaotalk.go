// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

// KakaoTalkCapability covers the person's own KakaoTalk account: its connect
// card, the add-on's passcode sign-in and disconnecting it. Background sync runs
// in Core independently of client connections.
var KakaoTalkCapability = protocol.Capability{Name: "kakaotalk", Version: "1.0"}

// KakaoTalkService is Core's control flow for the KakaoTalk source. The socket
// adapter carries authenticated session facts, so the connection that accepted
// the terms is the one whose sign-in answers are relayed.
type KakaoTalkService interface {
	KakaoTalk(context.Context, Session, protocol.KakaotalkRequest) (Result, error)
}

// KakaoTalkOperations routes the source's control requests. Only a client that
// may decide for this person and negotiated the capability reaches the service.
func KakaoTalkOperations(service KakaoTalkService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindKakaotalkRequest: {
		Capability: KakaoTalkCapability,
		Handle: func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
			p, ok := e.Payload.(protocol.KakaotalkRequest)
			if !ok || p.Operation == "" {
				return Result{}, &OperationError{Code: protocol.ErrorMalformed}
			}
			if service == nil {
				return Result{}, &OperationError{Code: protocol.ErrorUnavailable}
			}
			if !session.CanDecide {
				return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
			}
			return service.KakaoTalk(ctx, session, p)
		},
	}}
}
