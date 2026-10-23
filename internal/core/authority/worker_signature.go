// SPDX-License-Identifier: Apache-2.0
package authority

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// WorkerRequestBinding is supplied by Core's adapter, never by worker text.
type WorkerRequestBinding struct {
	TaskID, AttemptID, ThreadID, TurnID, RequestID, RequestHash string
	ProcessGeneration, BindingEpoch                             uint64
}

// WorkerApprovalProof quotes the displayed request and binds the exact original
// request hash. A public key carried by a proof is never a trust anchor.
type WorkerApprovalProof struct {
	PublicKey []byte                 `json:"public_key"`
	Decision  WorkerApprovalDecision `json:"decision"`
	Signature []byte                 `json:"signature"`
}
type WorkerApprovalDecision struct {
	Version    int                   `json:"version"`
	ApprovalID string                `json:"approval_id"`
	Person     string                `json:"person"`
	Epoch      uint64                `json:"epoch"`
	Binding    *WorkerRequestBinding `json:"binding,omitempty"`
	Method     string                `json:"method"`
	Request    json.RawMessage       `json:"request"`
	Choice     Choice                `json:"choice"`
	AnsweredAt time.Time             `json:"answered_at"`
}

func WorkerRequestHash(raw json.RawMessage) string {
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func (s *Service) WorkerApprovalKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), s.workerSigningKey.Public().(ed25519.PublicKey)...)
}
func (s *Service) signWorkerApproval(a Approval) *WorkerApprovalProof {
	d := WorkerApprovalDecision{Version: 1, ApprovalID: a.ID, Person: a.AnsweredBy, Epoch: a.Epoch, Binding: a.WorkerBinding, Method: a.Tool, Request: a.Arguments, Choice: a.Choice, AnsweredAt: a.AnsweredAt}
	raw, _ := json.Marshal(d)
	return &WorkerApprovalProof{PublicKey: s.WorkerApprovalKey(), Decision: d, Signature: ed25519.Sign(s.workerSigningKey, append([]byte("lina-worker-approval-v1\n"), raw...))}
}

// VerifyWorkerApproval requires the key pinned through trusted adapter setup.
func VerifyWorkerApproval(key ed25519.PublicKey, proof *WorkerApprovalProof) error {
	if len(key) != ed25519.PublicKeySize || proof == nil {
		return errors.New("missing trusted worker approval signature")
	}
	d := proof.Decision
	if d.Version != 1 || d.ApprovalID == "" || d.Person == "" || d.Epoch == 0 || d.AnsweredAt.IsZero() || d.Choice != Once && d.Choice != Deny || !json.Valid(d.Request) {
		return errors.New("invalid signed worker approval")
	}
	raw, err := json.Marshal(d)
	if err != nil || !ed25519.Verify(key, append([]byte("lina-worker-approval-v1\n"), raw...), proof.Signature) {
		return errors.New("invalid worker approval signature")
	}
	return nil
}
