// SPDX-License-Identifier: Apache-2.0
package authority

import (
	"context"
	"database/sql"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/grant"
)

// replayDenial restores the sentinel, never interpreting a diagnostic as a new
// authorization decision. Older records predate DenialCode; their basis and
// revision fields retain the original refusal without changing key identity.
func replayDenial(e Effect) error {
	code := e.DenialCode
	if code == "" {
		code = e.Basis
		// Legacy grant refusals were recorded only for stale writes. The
		// mismatch may be with the grant's revision, not the observed target.
		if code == "grant" && e.Write {
			code = grant.ErrStaleRevision.Error()
		}
	}
	for _, denial := range []error{ErrDenied, ErrNoSurface, grant.ErrStaleRevision, ErrPresetLimit} {
		if code == denial.Error() {
			return denial
		}
	}
	return ErrDenied
}

// stopEffectApproval commits with the refusal so an answer cannot revive a key
// whose durable result is already terminal. Preserve answered denial records.
func stopEffectApproval(ctx context.Context, tx *canon.Tx, key string) (bool, error) {
	a, _, err := readApproval(ctx, tx, "SELECT fingerprint,record FROM authority_approvals WHERE effect_key=?", key)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if a.State != Pending && a.State != Approved {
		return false, nil
	}
	a.State = Stopped
	return true, saveApproval(ctx, tx, a)
}
