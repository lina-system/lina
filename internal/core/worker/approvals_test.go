// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/authority"
	"testing"
	"time"
)

func TestSignedApprovalBindsExactWorkerRequest(t *testing.T) {
	key, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	th := Thread{ID: "thread", TurnID: "turn", TaskID: "task", AttemptID: "attempt", ProcessGeneration: 2, BindingEpoch: 3}
	request := Request{ID: "request", Method: "permission", Params: json.RawMessage(`{"path":"result.txt"}`)}
	d := authority.WorkerApprovalDecision{Version: 1, ApprovalID: "approval", Person: "owner", Epoch: 1, Binding: ApprovalBinding(th, request), Method: request.Method, Request: request.Params, Choice: authority.Once, AnsweredAt: time.Now()}
	raw, _ := json.Marshal(d)
	proof := &authority.WorkerApprovalProof{Decision: d, Signature: ed25519.Sign(private, append([]byte("lina-worker-approval-v1\n"), raw...))}
	if allow, err := VerifyApproval(key, th, request, proof); err != nil || !allow {
		t.Fatal(allow, err)
	}
	for _, mutate := range []func(*Thread){func(t *Thread) { t.ID = "other" }, func(t *Thread) { t.TurnID = "other" }, func(t *Thread) { t.TaskID = "other" }, func(t *Thread) { t.AttemptID = "other" }, func(t *Thread) { t.ProcessGeneration++ }, func(t *Thread) { t.BindingEpoch++ }} {
		other := th
		mutate(&other)
		if _, err := VerifyApproval(key, other, request, proof); err == nil {
			t.Fatalf("foreign execution accepted: %+v", other)
		}
	}
	for _, changed := range []Request{{ID: "other", Method: request.Method, Params: request.Params}, {ID: request.ID, Method: "other", Params: request.Params}, {ID: request.ID, Method: request.Method, Params: json.RawMessage(`{"path":"other.txt"}`)}} {
		if _, err := VerifyApproval(key, th, changed, proof); err == nil {
			t.Fatal("foreign request accepted")
		}
	}
	proof.Decision.Choice = authority.Deny
	if _, err := VerifyApproval(key, th, request, proof); err == nil {
		t.Fatal("changed decision accepted")
	}
}
