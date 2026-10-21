// SPDX-License-Identifier: Apache-2.0
package turn

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
)

// OutcomeSchema is installed with the turn migration; existing installations
// install it as a new numbered migration, never by opening another writer.
const OutcomeSchema = `CREATE TABLE turn_outcome (
 turn_id TEXT PRIMARY KEY NOT NULL REFERENCES turn_runs(id),
 outcome BLOB NOT NULL,
 corrections INTEGER NOT NULL DEFAULT 0 CHECK(corrections>=0),
 repeats INTEGER NOT NULL DEFAULT 0 CHECK(repeats>=0),
 rework INTEGER NOT NULL DEFAULT 0 CHECK(rework>=0),
 not_asked INTEGER NOT NULL DEFAULT 0 CHECK(not_asked>=0)
);`

type Quality struct{ Corrections, Repeats, Rework, NotAsked int }

type TurnOutcome struct {
	Meter    budget.Snapshot
	Handle   budget.Handle
	Gathered budget.Handoff
	Quality  Quality
}

func saveOutcome(ctx context.Context, tx *canon.Tx, id string, o TurnOutcome) error {
	data, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO turn_outcome(turn_id,outcome) VALUES(?,?) ON CONFLICT(turn_id) DO UPDATE SET outcome=excluded.outcome", id, data)
	return err
}

func (s *Store) Outcome(ctx context.Context, id string) (o TurnOutcome, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		var q Quality
		if err := tx.QueryRowContext(ctx, "SELECT outcome,corrections,repeats,rework,not_asked FROM turn_outcome WHERE turn_id=?", id).Scan(&raw, &q.Corrections, &q.Repeats, &q.Rework, &q.NotAsked); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
		o.Quality = q
		return nil
	})
	return o, err
}

// SetQuality stores the reactions job's absolute counters, so replaying the
// same classification cannot double-count it. It never tunes a budget.
func (s *Store) SetQuality(ctx context.Context, id string, q Quality) error {
	if q.Corrections < 0 || q.Repeats < 0 || q.Rework < 0 || q.NotAsked < 0 {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		r, err := tx.ExecContext(ctx, "UPDATE turn_outcome SET corrections=?,repeats=?,rework=?,not_asked=? WHERE turn_id=?", q.Corrections, q.Repeats, q.Rework, q.NotAsked, id)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err == nil && n != 1 {
			return ErrInvalid
		}
		return err
	})
}
