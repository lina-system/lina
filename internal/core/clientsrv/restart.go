// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"errors"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/protocol"
)

var lifecycleCapability = protocol.Capability{Name: "lifecycle", Version: "1.0"}

func restartStatus(r ctlfiles.RestartRequest) protocol.RestartStatus {
	s := protocol.RestartStatus{Owed: r.Owed()}
	if r.ID != "" {
		s.RequestID, s.RequestedEpoch = &r.ID, &r.Epoch
		mode := protocol.RestartMode(r.Mode)
		s.Mode = &mode
		if r.CommittedEpoch != "" {
			s.CommittedEpoch = &r.CommittedEpoch
		}
		if r.DischargedEpoch != "" {
			s.DischargedEpoch = &r.DischargedEpoch
		}
	}
	return s
}

// The returned release must run after encoding the acknowledgment, even if the
// client disconnects. Otherwise an idle restart can close its own reply socket.
func restartReply(ctx context.Context, identity Identity, request protocol.Envelope, answer *protocol.Envelope) func() {
	finish := func() {}
	if request.Kind == protocol.KindRestartGet {
		answer.Kind, answer.Payload = protocol.KindRestartStatus, restartStatus(identity.Restart.Status())
		return finish
	}
	if identity.Recovering != nil && identity.Recovering() {
		refuse(answer, protocol.ErrorUnavailable, "Core is recovering")
		return finish
	}
	hold, err := identity.Restart.HoldReply(ctx)
	if err != nil {
		refuse(answer, protocol.ErrorUnavailable, "Core is draining")
		return finish
	}
	finish = hold.Release
	payload := request.Payload.(protocol.RestartRequest)
	mode := ""
	if payload.Mode != nil {
		mode = string(*payload.Mode)
	}
	receipt, err := identity.Restart.Request(ctx, mode)
	if err != nil {
		code := protocol.ErrorUnavailable
		if errors.Is(err, lifecycle.ErrPending) {
			code = protocol.ErrorDuplicate
		}
		refuse(answer, code, "restart request could not be accepted")
		return finish
	}
	answer.Kind, answer.Payload = protocol.KindRestartStatus, restartStatus(receipt)
	answer.EffectState = protocol.EffectApplied
	answer.ReceiptRef = &protocol.ReceiptRef{Kind: "receipt", ID: receipt.ID, Revision: "1"}
	return finish
}
