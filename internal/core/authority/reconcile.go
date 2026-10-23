// SPDX-License-Identifier: Apache-2.0
package authority

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/tools"
)

// ReconcileApplied records a trusted readback for a non-running unknown effect.
// The immutable admission binding must still match the snapshot read before the
// observation. Receipt settlement and release of that exact target reservation
// commit together, without issuing the effect or granting a new one.
func (s *Service) ReconcileApplied(ctx context.Context, expected Effect, result tools.Result) error {
	if expected.Key == "" || result.State != tools.Applied || result.Revision == "" || !json.Valid(result.Body) {
		return tools.ErrInvalid
	}
	binding := func(e Effect) []byte {
		e.State, e.Result = tools.None, tools.Result{}
		raw, _ := json.Marshal(e)
		return raw
	}
	return s.store.Write(ctx, func(tx *canon.Tx) error {
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		e, _, running, err := readEffect(ctx, tx, expected.Key)
		if err != nil {
			return err
		}
		if running {
			return tools.ErrInProgress
		}
		if e.Epoch > s.cfg.Epoch || !bytes.Equal(binding(e), binding(expected)) {
			return tools.ErrKeyConflict
		}
		if e.State == tools.Applied {
			// An old confirming read must not clear a subsequent writer's slot.
			if e.Result.Revision != result.Revision {
				return tools.ErrKeyConflict
			}
			return nil
		}
		if e.State != tools.Unknown {
			return tools.ErrKeyConflict
		}
		if e.Write {
			updated, err := tx.ExecContext(ctx, "UPDATE authority_targets SET revision=?,agent=?,active_key='' WHERE target=? AND active_key=?", result.Revision, e.Agent, e.Target, e.Key)
			if err != nil {
				return err
			}
			count, err := updated.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return tools.ErrKeyConflict
			}
		}
		e.State, e.Result = tools.Applied, result
		return saveEffect(ctx, tx, e, false)
	})
}

// CancelInterrupted settles a dead local invocation only when its trusted owner
// can reconcile the underlying effects in the same canon snapshot. Cancellation
// does not undo those effects or assert they never happened. The old call is
// never dispatched again; a new call must obtain fresh authority. Unrecognized
// effects and current-epoch calls retain their reservations.
// Call during startup after the owning ledger recovers and before admission.
func (s *Service) CancelInterrupted(ctx context.Context, reconcile func(context.Context, *canon.Tx, Effect) (tools.Result, bool, error)) error {
	return s.store.Write(ctx, func(tx *canon.Tx) error {
		if err := s.checkEpoch(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT record FROM authority_effects WHERE running=0")
		if err != nil {
			return err
		}
		var pending []Effect
		for rows.Next() {
			var raw []byte
			var e Effect
			if err = rows.Scan(&raw); err == nil {
				err = json.Unmarshal(raw, &e)
			}
			if err != nil {
				rows.Close()
				return err
			}
			if e.State == tools.Unknown && e.Epoch < s.cfg.Epoch {
				pending = append(pending, e)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range pending {
			result, recognized, err := reconcile(ctx, tx, e)
			if err != nil {
				return err
			}
			if !recognized {
				continue
			}
			if result.State != tools.Cancelled || result.Revision == "" || !json.Valid(result.Body) {
				return tools.ErrInvalid
			}
			if e.Write {
				updated, err := tx.ExecContext(ctx, "UPDATE authority_targets SET revision=?,agent='',active_key='' WHERE target=? AND active_key=?", result.Revision, e.Target, e.Key)
				if err != nil {
					return err
				}
				n, err := updated.RowsAffected()
				if err != nil {
					return err
				}
				if n != 1 {
					return tools.ErrKeyConflict
				}
			}
			e.State, e.Result = tools.Cancelled, result
			if err := saveEffect(ctx, tx, e, false); err != nil {
				return err
			}
		}
		return nil
	})
}
