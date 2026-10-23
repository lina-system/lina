// SPDX-License-Identifier: Apache-2.0
package authority_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/authority"
	"testing"
)

func TestWorkerAnswerHasSignedRecord(t *testing.T) {
	t.Parallel()
	f := setup(t)
	a := requestWorker(t, f)
	if err := f.service.Answer(t.Context(), a.ID, owner(f), authority.Decision{Choice: authority.Once}); err != nil {
		t.Fatal(err)
	}
	answer, err := f.service.ClaimWorkerAnswer(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(answer)
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if len(record["WorkerProof"]) == 0 {
		t.Fatal("authenticated worker approval has no signed record")
	}
	if err := authority.VerifyWorkerApproval(f.service.WorkerApprovalKey(), answer.WorkerProof); err != nil {
		t.Fatal(err)
	}
	if answer.WorkerProof.Decision.ApprovalID != a.ID || answer.WorkerProof.Decision.Person != a.Person || string(answer.WorkerProof.Decision.Request) != string(a.Arguments) {
		t.Fatal("proof lost original request or owner")
	}
	proof := *answer.WorkerProof
	proof.Decision.Choice = authority.Deny
	if err := authority.VerifyWorkerApproval(f.service.WorkerApprovalKey(), &proof); err == nil {
		t.Fatal("tampered decision verified")
	}
	foreign, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := authority.VerifyWorkerApproval(foreign, answer.WorkerProof); err == nil {
		t.Fatal("foreign machine verified")
	}
	if err := authority.VerifyWorkerApproval(f.service.WorkerApprovalKey(), nil); err == nil {
		t.Fatal("unsigned answer verified")
	}
}
