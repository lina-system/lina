// SPDX-License-Identifier: Apache-2.0

package turn

import (
	"context"
	"database/sql"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// Partial is visible text from an unfinished response, never model context.
// Round and Index retain the loop's zero-based response identity.
type Partial struct {
	Round int
	Index int64
	Text  string
}

// Stopped binds visible partials to a committed interruption or failure.
// An empty NoticeID means no such terminal outcome was committed for this input.
type Stopped struct {
	TurnID, NoticeID string
	Partials         []Partial
}

func savePartial(ctx context.Context, tx *canon.Tx, id string, p Partial) error {
	masked, err := secrets.Mask(p.Text)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO turn_partials(turn_id,round,ordinal,body) VALUES(?,?,?,?)", id, p.Round, p.Index, masked.Text())
	return err
}

// Stopped reads the existing turn store for a durable input event. It exposes
// only aborted or failed turns with a committed notice. The notice identifies
// the outcome; unfinished text never becomes a completed assistant message.
// It never admits a model message or retries.
func (s *Store) Stopped(ctx context.Context, inputEvent string) (Stopped, error) {
	var stopped Stopped
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT r.id,c.notice_event FROM turn_runs r
   JOIN turn_control c ON c.turn_id=r.id
   WHERE c.input_event=? AND r.state IN ('aborted','failed') AND c.notice_event IS NOT NULL`, inputEvent).Scan(&stopped.TurnID, &stopped.NoticeID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT round,ordinal,body FROM turn_partials WHERE turn_id=? ORDER BY round,ordinal", stopped.TurnID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p Partial
			if err := rows.Scan(&p.Round, &p.Index, &p.Text); err != nil {
				return err
			}
			// Old databases can contain partials saved before storage masking.
			masked, err := secrets.Mask(p.Text)
			if err != nil {
				return err
			}
			p.Text = masked.Text()
			stopped.Partials = append(stopped.Partials, p)
		}
		return rows.Err()
	})
	return stopped, err
}
