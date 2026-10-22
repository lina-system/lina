// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/tools"
)

// invalidateAuthority runs only against the private restored snapshot, before
// publication. Receipts survive, but no captured permission can dispatch work.
// Process-local leases are never serialized; durable claims are cleared here.
func invalidateAuthority(ctx context.Context, db *canon.Store) error {
	return db.Write(ctx, func(tx *canon.Tx) error {
		if err := rewriteRestoreRecords(ctx, tx, "authority_effects", "effect_key", func(id string, raw []byte) ([]byte, error) {
			var e authority.Effect
			if err := json.Unmarshal(raw, &e); err != nil {
				return nil, err
			}
			if e.Key != id {
				return nil, errors.New("invalid restored effect")
			}
			switch e.State {
			case tools.None:
				e.State = tools.Unknown
				e.Result = tools.Result{State: tools.Unknown}
				return json.Marshal(e)
			case tools.Unknown, tools.Applied, tools.Failed, tools.Cancelled, tools.Refused:
				return raw, nil
			default:
				return nil, errors.New("invalid restored effect state")
			}
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE authority_effects SET running=0"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE authority_targets SET active_key=''"); err != nil {
			return err
		}
		// A captured queue predates any delivery after the generation. Even an
		// unclaimed or retryable send has an unknown outcome after restoration.
		// Preserve parts and receipts, but neither dispatch nor settle old claims.
		if _, err := tx.ExecContext(ctx, "UPDATE channel_outbound SET state='unknown' WHERE state IN ('queued','sending')"); err != nil {
			return err
		}
		if err := rewriteRestoreRecords(ctx, tx, "authority_approvals", "approval_id", func(id string, raw []byte) ([]byte, error) {
			var a authority.Approval
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
			if a.ID != id {
				return nil, errors.New("invalid restored approval")
			}
			switch a.State {
			case authority.Pending, authority.Approved:
				a.State = authority.Stopped
				return json.Marshal(a)
			case authority.Denied, authority.Stopped, authority.Consumed:
				return raw, nil
			default:
				return nil, errors.New("invalid restored approval state")
			}
		}); err != nil {
			return err
		}
		if err := rewriteRestoreRecords(ctx, tx, "authority_standing", "grant_id", func(id string, raw []byte) ([]byte, error) {
			var g authority.StandingGrant
			if err := json.Unmarshal(raw, &g); err != nil {
				return nil, err
			}
			if g.ID != id {
				return nil, errors.New("invalid restored grant")
			}
			g.Revoked = true
			return json.Marshal(g)
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM agent_preparations"); err != nil {
			return err
		}
		return rewriteRestoreRecords(ctx, tx, "accounts", "id", func(id string, raw []byte) ([]byte, error) {
			var a accounts.Account
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
			if string(a.ID) != id {
				return nil, errors.New("invalid restored account")
			}
			a.Status = accounts.NeedsSignIn
			a.AccessExpires = nil
			a.RefreshExpires = nil
			return json.Marshal(a)
		})
	})
}

// Table and key are source constants, never manifest or CLI input. Read all
// records before updating: the canon writer uses one SQLite connection.
func rewriteRestoreRecords(ctx context.Context, tx *canon.Tx, table, key string, change func(string, []byte) ([]byte, error)) error {
	rows, err := tx.QueryContext(ctx, "SELECT "+key+",record FROM "+table)
	if err != nil {
		return err
	}
	type record struct {
		id  string
		raw []byte
	}
	var records []record
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, r := range records {
		raw, err := change(r.id, r.raw)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET record=? WHERE "+key+"=?", raw, r.id); err != nil {
			return err
		}
	}
	return nil
}
