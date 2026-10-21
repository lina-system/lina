// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

var TalkCapability = protocol.Capability{Name: "exchanges", Version: "1.0"}

// TalkService projects the existing exchange log. It authorizes the person and
// agents before reading either summaries or exchange ledger content.
type TalkService interface {
	Talk(context.Context, Session, protocol.AgentTalkGet) (protocol.AgentTalk, error)
	ExpandTalk(context.Context, Session, protocol.AgentTalkExpand) (protocol.AgentTalk, error)
}

func TalkOperations(service TalkService) map[protocol.Kind]Operation {
	handle := func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
		if service == nil {
			return Result{}, &OperationError{Code: protocol.ErrorUnavailable}
		}
		var answer protocol.AgentTalk
		var err error
		switch p := e.Payload.(type) {
		case protocol.AgentTalkGet:
			answer, err = service.Talk(ctx, session, p)
		case protocol.AgentTalkExpand:
			if p.ExchangeID == nil {
				return Result{}, &OperationError{Code: protocol.ErrorMalformed}
			}
			answer, err = service.ExpandTalk(ctx, session, p)
		default:
			return Result{}, &OperationError{Code: protocol.ErrorMalformed}
		}
		return Result{Kind: protocol.KindAgentTalk, Payload: answer}, err
	}
	return map[protocol.Kind]Operation{
		protocol.KindAgentTalkGet:    {Capability: TalkCapability, Handle: handle},
		protocol.KindAgentTalkExpand: {Capability: TalkCapability, Handle: handle},
	}
}
