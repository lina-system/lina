// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"

	"github.com/thisisjun786/lina/kit/protocol"
)

var ChannelCapability = protocol.Capability{Name: "channels", Version: "1.0"}

// ChannelService is Core's existing channel control flow. The socket adapter
// carries authenticated session facts, never identities supplied in a form.
type ChannelService interface {
	Channel(context.Context, Session, protocol.ChannelRequest) (protocol.ChannelResult, error)
}

func ChannelOperations(service ChannelService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindChannelRequest: {
		Capability: ChannelCapability,
		Handle: func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
			p, ok := e.Payload.(protocol.ChannelRequest)
			if !ok || !validChannelRequest(p) {
				return Result{}, &OperationError{Code: protocol.ErrorMalformed}
			}
			if service == nil {
				return Result{}, &OperationError{Code: protocol.ErrorUnavailable}
			}
			if *p.Operation != "list" && !session.CanDecide {
				return Result{}, &OperationError{Code: protocol.ErrorNotAuthorized}
			}
			answer, err := service.Channel(ctx, session, p)
			effect := protocol.EffectNone
			if *p.Operation != "list" {
				effect = protocol.EffectApplied
			}
			return Result{Kind: protocol.KindChannelResult, Payload: answer, Effect: effect}, err
		},
	}}
}

func validChannelRequest(p protocol.ChannelRequest) bool {
	if p.Operation == nil {
		return false
	}
	address := p.Chat != nil || p.Topic != nil || p.AgentID != nil || p.Scope != nil
	switch *p.Operation {
	case "list":
		return p.Account == nil && p.Token == nil && !address
	case "connect":
		return p.Account == nil && p.Token != nil && !address
	case "pair", "disconnect":
		return p.Account != nil && p.Token == nil && !address
	case "bind":
		return p.Account != nil && p.Token == nil && p.AgentID != nil && p.Scope != nil
	case "unbind":
		return p.Account != nil && p.Token == nil && p.Chat != nil && p.AgentID == nil && p.Scope == nil
	}
	return false
}
