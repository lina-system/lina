// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

var ConversationCapability = protocol.Capability{Name: "conversation", Version: "1.0"}

// ConversationService is supplied by boot, which composes the existing turn,
// lane and ledger flows. The socket adapter owns no conversation engine.
type ConversationService interface {
	History(context.Context, Session, protocol.HistoryGet) (protocol.History, error)
	Input(context.Context, Session, protocol.Input) (protocol.ItemDone, error)
	Interrupt(context.Context, Session, protocol.Interrupt) error
}

func ConversationOperations(service ConversationService) map[protocol.Kind]Operation {
	handle := func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
		malformed := &OperationError{Code: protocol.ErrorMalformed}
		if service == nil {
			return Result{}, &OperationError{Code: protocol.ErrorUnavailable}
		}
		switch p := e.Payload.(type) {
		case protocol.Input:
			if p.ConversationID == nil || *p.ConversationID == "" || p.InputID == nil || *p.InputID == "" || p.Text == nil || *p.Text == "" {
				return Result{}, malformed
			}
			answer, err := service.Input(ctx, session, p)
			return Result{Kind: protocol.KindItemDone, Payload: answer, Effect: protocol.EffectApplied}, err
		case protocol.Interrupt:
			if p.ConversationID == nil || *p.ConversationID == "" {
				return Result{}, malformed
			}
			err := service.Interrupt(ctx, session, p)
			return Result{Kind: protocol.KindTurnState, Payload: protocol.TurnState{}, Effect: protocol.EffectApplied}, err
		case protocol.HistoryGet:
			answer, err := service.History(ctx, session, p)
			return Result{Kind: protocol.KindHistory, Payload: answer}, err
		case protocol.ConversationStart:
			answer, err := service.History(ctx, session, protocol.HistoryGet{AgentID: &p.AgentID})
			return Result{Kind: protocol.KindHistory, Payload: answer}, err
		}
		return Result{}, malformed
	}
	operations := make(map[protocol.Kind]Operation)
	for _, kind := range []protocol.Kind{protocol.KindConversationStart, protocol.KindInput, protocol.KindInterrupt, protocol.KindHistoryGet} {
		operations[kind] = Operation{Capability: ConversationCapability, Handle: handle}
	}
	return operations
}
