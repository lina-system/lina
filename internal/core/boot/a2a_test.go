// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/a2a"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
	"testing"
	"time"
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

func TestA2ABinaryResultRetainsVerifiedBytes(t *testing.T) {
	r, _ := turnRuntime(t)
	data := []byte{0xff, 0, 1, 2, 3}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	c := a2a.Call{ID: "binary-call", Origin: r.a2aOrigin("binary-turn"), Profile: a2a.Profile{SHA256: digest}, Verified: true, Validated: true, VerifiedHash: digest, State: "verified", Evidence: []a2a.Evidence{{Artifact: "binary", MediaType: "application/octet-stream", Body: data, SHA256: digest, Source: "https://peer.example", Observed: time.Now()}}}
	raw, _ := json.Marshal(c)
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO a2a_calls(id,person,agent,conversation,connection,record) VALUES(?,?,?,?,?,?)", c.ID, r.person, r.agent, r.conversation, "peer", raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.deliverA2A(t.Context(), c); err != nil {
		t.Fatal("binary delivery:", err)
	}
	value, err := r.planResults.Read(t.Context(), result.Scope{PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, TurnID: "read"}, result.ReadRequest{ID: "a2a:" + c.ID})
	if err != nil {
		t.Fatal(err)
	}
	var file result.File
	if value.Type != result.FileType || json.Unmarshal(value.Body, &file) != nil {
		t.Fatal("not a file result")
	}
	retained, err := r.assets.Read(t.Context(), file.Asset.ID, int64(file.Asset.Revision))
	if err != nil || !bytes.Equal(retained, data) {
		t.Fatal("artifact bytes changed", err)
	}
}
