// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

// ManagementService composes existing Core stores in boot. The transport owns
// no storage, scope policy or model execution.
type ManagementService interface {
	Agent(context.Context, Session, protocol.AgentRequest) (protocol.AgentResult, error)
	Memory(context.Context, Session, protocol.MemoryRequest) (protocol.MemoryResult, error)
}

func ManagementOperations(service ManagementService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{
		protocol.KindAgentRequest: {Capability: protocol.Capability{Name: "agents", Version: "1.0"}, Handle: func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
			p, ok := e.Payload.(protocol.AgentRequest)
			if !ok || p.Operation == nil {
				return Result{}, &OperationError{Code: protocol.ErrorMalformed}
			}
			answer, err := service.Agent(ctx, session, p)
			effect := protocol.EffectApplied
			if *p.Operation == "list" || *p.Operation == "choose" {
				effect = protocol.EffectNone
			}
			return Result{Kind: protocol.KindAgentResult, Payload: answer, Effect: effect}, err
		}},
		protocol.KindMemoryRequest: {Capability: protocol.Capability{Name: "memory", Version: "1.0"}, Handle: func(ctx context.Context, session Session, e protocol.Envelope) (Result, error) {
			p, ok := e.Payload.(protocol.MemoryRequest)
			if !ok || p.Operation == nil {
				return Result{}, &OperationError{Code: protocol.ErrorMalformed}
			}
			answer, err := service.Memory(ctx, session, p)
			effect := protocol.EffectApplied
			if *p.Operation == "list" {
				effect = protocol.EffectNone
			}
			return Result{Kind: protocol.KindMemoryResult, Payload: answer, Effect: effect}, err
		}},
	}
}
