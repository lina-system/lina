// SPDX-License-Identifier: Apache-2.0
package turn

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// OutcomeSchema is a separate numbered migration after the published Schema
// and ControlSchema, for both new and existing installations.
const OutcomeSchema = canon.TurnOutcomeSchema

type Quality struct{ Corrections, Repeats, Rework, NotAsked int }

type TurnOutcome struct {
	Meter    budget.Snapshot
	Handle   budget.Handle
	Gathered budget.Handoff
	Quality  Quality
}

// PublishPromotion is supplied to runs.Config by Core's composition root. It
// publishes the handle using the admission transaction, so both rows commit or
// neither does. Recovery may then release an unstarted promotion by that handle.
func PublishPromotion(ctx context.Context, tx *canon.Tx, conversation string, handle budget.Handle, h budget.Handoff) error {
	if err := active(ctx, tx, h.Turn); err != nil {
		return err
	}
	var owner string
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT r.conversation,o.outcome FROM turn_runs r JOIN turn_outcome o ON o.turn_id=r.id WHERE r.id=?", h.Turn).Scan(&owner, &raw); err != nil {
		return err
	}
	if owner != conversation || handle.ID == "" {
		return ErrInvalid
	}
	var outcome TurnOutcome
	if err := json.Unmarshal(raw, &outcome); err != nil {
		return err
	}
	outcome.Handle, outcome.Gathered = handle, h
	return saveOutcome(ctx, tx, h.Turn, outcome)
}

func saveOutcome(ctx context.Context, tx *canon.Tx, id string, o TurnOutcome) error {
	data, err := json.Marshal(o)
	if err != nil {
		return err
	}
	masked, err := secrets.Mask(string(data))
	if err != nil {
		return err
	}
	data = []byte(masked.Text())
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
