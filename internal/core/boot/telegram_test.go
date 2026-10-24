// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestTelegramSettingsRequireOwnerDeciderAndCurrentEpoch(t *testing.T) {
	r := controlRuntime(t)
	op := r.identity.Operations[protocol.KindTelegramRequest]
	if op.Handle == nil {
		t.Fatal("Telegram not registered")
	}
	owner := clientsrv.Session{PersonID: r.person, Epoch: r.epoch, CanDecide: true}
	for _, s := range []clientsrv.Session{{PersonID: "other", Epoch: r.epoch, CanDecide: true}, {PersonID: r.person, Epoch: r.epoch}, {PersonID: r.person, Epoch: r.epoch + 1, CanDecide: true}} {
		_, err := op.Handle(t.Context(), s, protocol.Envelope{Payload: protocol.TelegramRequest{Operation: "list"}})
		var refused *clientsrv.OperationError
		if !errors.As(err, &refused) || refused.Code != protocol.ErrorNotAuthorized {
			t.Fatal("owner boundary bypass", err)
		}
	}
	if _, err := op.Handle(t.Context(), owner, protocol.Envelope{Payload: protocol.TelegramRequest{Operation: "list"}}); err != nil {
		t.Fatal(err)
	}
}
