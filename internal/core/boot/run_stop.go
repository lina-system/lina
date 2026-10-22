// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

func (r *conversationRuntime) stopRun(ctx context.Context, s clientsrv.Session, e protocol.Envelope) (clientsrv.Result, error) {
	if err := r.authorize(s, nil); err != nil {
		return clientsrv.Result{}, err
	}
	request := e.Payload.(protocol.Stop)
	if request.RunID == nil || *request.RunID == "" {
		return clientsrv.Result{}, malformedManagement()
	}
	if r.background == nil {
		return clientsrv.Result{}, &clientsrv.OperationError{Code: protocol.ErrorUnavailable}
	}
	record, err := r.background.service.Get(ctx, *request.RunID)
	if err != nil || record.Origin.Person != s.PersonID {
		return clientsrv.Result{}, outsideManagement()
	}
	if err := r.background.service.Stop(ctx, record.ID); err != nil {
		return clientsrv.Result{}, err
	}
	record, err = r.background.service.Get(ctx, record.ID)
	if err != nil {
		return clientsrv.Result{}, err
	}
	r.background.notify()
	return clientsrv.Result{Kind: protocol.KindStopResult, Effect: protocol.EffectApplied, Payload: protocol.StopResult{RunID: record.ID, Outcome: protocol.StopOutcome(record.Outcome)}}, nil
}
