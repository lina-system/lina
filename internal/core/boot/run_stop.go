// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"strings"

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
	if strings.HasPrefix(*request.RunID, "a2a:") {
		return r.stopA2A(ctx, s, request, e.MessageID)
	}
	if workerReference(*request.RunID) {
		err := r.ownerWorkerControl(ctx, s, *request.RunID, request.ConversationID, e.MessageID, "")
		return clientsrv.Result{Kind: protocol.KindStopResult, Effect: protocol.EffectApplied, Payload: protocol.StopResult{RunID: *request.RunID, Outcome: protocol.StopOutcomeRequested}}, err
	}
	record, err := r.controlledRun(ctx, s, *request.RunID, request.ConversationID)
	if err != nil {
		return clientsrv.Result{}, err
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
