// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"crypto/ed25519"
	"errors"
	"github.com/thisisjun786/lina/internal/core/authority"
)

func ApprovalBinding(th Thread, request Request) *authority.WorkerRequestBinding {
	return &authority.WorkerRequestBinding{TaskID: th.TaskID, AttemptID: th.AttemptID, ThreadID: th.ID, TurnID: th.TurnID, ProcessGeneration: th.ProcessGeneration, BindingEpoch: th.BindingEpoch, RequestID: request.ID, RequestHash: authority.WorkerRequestHash(request.Params)}
}

// VerifyApproval binds the authenticated decision to this pending native
// request before the adapter selects its once-only protocol response.
func VerifyApproval(key ed25519.PublicKey, th Thread, request Request, proof *authority.WorkerApprovalProof) (bool, error) {
	if err := authority.VerifyWorkerApproval(key, proof); err != nil {
		return false, err
	}
	d := proof.Decision
	if d.Binding == nil || *d.Binding != *ApprovalBinding(th, request) || d.Method != request.Method {
		return false, errors.New("worker approval binding mismatch")
	}
	return d.Choice == authority.Once, nil
}
