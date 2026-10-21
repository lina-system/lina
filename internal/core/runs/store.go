// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/materials"
)

// Schema is installed in a numbered migration by the Core composition root,
// like turn.Schema. Runs never open or migrate a second writer.
const Schema = `
CREATE TABLE background_runs (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 record BLOB NOT NULL
);
`

func (s *Service) read(ctx context.Context, id string) (r Record, err error) {
	err = s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		var b []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM background_runs WHERE id=?", id).Scan(&b); err != nil {
			return err
		}
		return json.Unmarshal(b, &r)
	})
	return
}
func (s *Service) records(ctx context.Context) (out []Record, err error) {
	err = s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM background_runs ORDER BY seq")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b []byte
			var r Record
			if err := rows.Scan(&b); err != nil {
				return err
			}
			if err := json.Unmarshal(b, &r); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return
}
func (s *Service) save(ctx context.Context, r Record, insert bool) error {
	r.Updated = s.cfg.Now().UnixNano()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	err = s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		if insert {
			_, err := tx.ExecContext(ctx, "INSERT INTO background_runs(id,record) VALUES(?,?)", r.ID, b)
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE background_runs SET record=? WHERE id=?", b, r.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n != 1 {
			return ErrState
		}
		return err
	})
	if err == nil {
		s.signal()
	}
	return err
}
func (s *Service) Get(ctx context.Context, id string) (Record, error) { return s.read(ctx, id) }

// Notices is consumed by the conversation owner only at a turn boundary (or by
// the home-card owner for jobs, with an empty conversation). Acknowledgement is
// separate so a failed delivery remains pending across restart.
func (s *Service) Notices(ctx context.Context, conversation string) ([]Notice, error) {
	rows, err := s.records(ctx)
	if err != nil {
		return nil, err
	}
	var out []Notice
	for _, r := range rows {
		if r.State == Landed && !r.Noticed && r.Origin.Conversation == conversation {
			citations, err := materials.Check(ctx, s.cfg.Assets, r.Artifact.Citations)
			if err != nil {
				return nil, err
			}
			outcome := r.Outcome
			for _, c := range citations {
				if !c.Resolved && outcome == Complete {
					outcome = Partial
				}
			}
			checks := append([]CheckResult(nil), r.Checks...)
			for i := range checks {
				if checks[i].Check.Kind == CitationsResolve {
					for _, c := range citations {
						if !c.Resolved {
							checks[i].Passed = false
						}
					}
				}
			}
			out = append(out, Notice{ID: r.ID, Origin: r.Origin, Outcome: outcome, Result: r.Brief.Result, Material: r.Material, Citations: citations, Checks: checks, Reason: r.Reason})
		}
	}
	return out, nil
}
func (s *Service) Acknowledge(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	if r.State != Landed {
		return ErrState
	}
	r.Noticed = true
	return s.save(ctx, r, false)
}
