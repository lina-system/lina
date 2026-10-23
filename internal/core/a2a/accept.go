// SPDX-License-Identifier: Apache-2.0
package a2a

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Accept is available only through Core's eligible owner surface. The owner
// verifies an inspected artifact by its retained hash; remote text and model
// tools cannot call this operation or override a configured automatic check.
func (s *Service) Accept(ctx context.Context, o Origin, id, digest string) (Call, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	call, err := s.Call(ctx, o, id)
	if err != nil {
		return Call{}, err
	}
	if o.Epoch != s.Epoch || call.Profile.SHA256 != "" || !call.Validated || call.State != "unverified" || call.Verified {
		return Call{}, ErrScope
	}
	found := false
	for _, e := range call.Evidence {
		if e.SHA256 == digest {
			found = true
		}
	}
	if !found {
		return Call{}, ErrEvidence
	}
	call.Verified = true
	call.VerifiedHash = digest
	call.State = "verified"
	call.AcceptedBy = o.Person
	call.AcceptedAt = time.Now().UTC()
	call.Delivered = false
	raw, _ := json.Marshal(call)
	err = s.DB.Write(ctx, func(tx *canon.Tx) error {
		if err := s.epoch(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE a2a_calls SET record=? WHERE id=?", raw, call.ID)
		return err
	})
	return call, err
}
