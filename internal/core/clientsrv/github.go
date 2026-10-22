// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
)

var GitHubCapability = protocol.Capability{Name: "github", Version: "1.0"}

type GitHubService interface {
	ObserveGitHub(context.Context, Session, protocol.WorkRequest) (protocol.WorkResult, error)
}

func GitHubOperations(service GitHubService) map[protocol.Kind]Operation {
	return map[protocol.Kind]Operation{protocol.KindWorkRequest: {Capability: GitHubCapability, Handle: func(ctx context.Context, s Session, e protocol.Envelope) (Result, error) {
		p, ok := e.Payload.(protocol.WorkRequest)
		if !ok || p.Operation == nil || p.Repository == nil || p.PrNumber == nil {
			return Result{}, &OperationError{Code: protocol.ErrorMalformed}
		}
		r, err := service.ObserveGitHub(ctx, s, p)
		return Result{Kind: protocol.KindWorkResult, Payload: r, Effect: protocol.EffectNone}, err
	}}}
}
