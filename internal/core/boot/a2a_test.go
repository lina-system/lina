// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

func TestA2AOwnerSurfaceInstalled(t *testing.T) {
	r, _ := turnRuntime(t)
	op, ok := r.identity.Operations[protocol.KindA2aRequest]
	if !ok || op.Handle == nil || op.Capability != (protocol.Capability{Name: "a2a", Version: "1.0"}) {
		t.Fatal("running Core has no A2A owner connection surface")
	}
}

func TestA2ASettingsRequireEligibleOwner(t *testing.T) {
	r, _ := turnRuntime(t)
	op := r.identity.Operations[protocol.KindA2aRequest]
	for _, s := range []clientsrv.Session{{PersonID: r.person, Epoch: r.epoch}, {PersonID: "other", Epoch: r.epoch, CanDecide: true}, {PersonID: r.person, Epoch: r.epoch + 1, CanDecide: true}} {
		if _, err := op.Handle(t.Context(), s, protocol.Envelope{Payload: protocol.A2aRequest{Operation: "list"}}); err == nil {
			t.Fatal("settings accepted without owner authority")
		}
	}
}
