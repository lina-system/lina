// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/progress"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) startWorkerLanes() {
	r.identity.Operations[protocol.KindProgressGet] = clientsrv.Operation{
		Capability: protocol.Capability{Name: "worker_lanes", Version: "1.0"},
		Handle: func(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
			if err := r.authorize(s, nil); err != nil {
				return clientsrv.Result{}, err
			}
			agents, err := r.agents.List(ctx)
			if err != nil {
				return clientsrv.Result{}, err
			}
			names := map[string]string{}
			for _, a := range agents {
				names[a.ID] = a.Name
			}
			after := ""
			if p := e.Payload.(protocol.ProgressGet); p.After != nil {
				after = *p.After
			}
			p, err := progress.Lanes(ctx, r.db, s.PersonID, after, names)
			return clientsrv.Result{Kind: protocol.KindProgress, Payload: p}, err
		},
	}
}
