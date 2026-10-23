// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

func (s *Service) ExtractionCursor(ctx context.Context, id string) (int64, error) {
	var cursor int64
	err := s.cfg.Store.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT coalesce((SELECT through FROM memory_extraction_cursor WHERE job=?),0)", id).Scan(&cursor)
	})
	return cursor, err
}

// ScheduleExtraction keeps one immutable pending batch. Failed calls retain
// their reservation and charge; a fresh task retries from the committed source
// cursor, at the job cadence. A live call or cached result is never replaced.
func (s *Service) ScheduleExtraction(ctx context.Context, id string, batch memory.ExtractionBatch) error {
	d, ok := s.cfg.Definitions[id]
	if !ok || batch.After < 0 || batch.Through < batch.After || batch.Agent != d.Agent {
		return jobapi.ErrInvalid
	}
	input, err := json.Marshal(batch)
	if err != nil || !d.validPayload(input) {
		return jobapi.ErrInvalid
	}
	s.auxMu.Lock()
	defer s.auxMu.Unlock()
	return s.apply(ctx, s.cfg.Epoch, func(tx *canon.Tx) error {
		j, err := readJob(ctx, tx, id)
		if err != nil || !j.Enabled {
			return err
		}
		var cursor, seq int64
		if err := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT through FROM memory_extraction_cursor WHERE job=?),0)", id).Scan(&cursor); err != nil {
			return err
		}
		if cursor != batch.After {
			return jobapi.ErrConflict
		}
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0) FROM moirai_tasks WHERE job=?", id).Scan(&seq); err != nil {
			return err
		}
		if seq > j.Watermark {
			var reservation string
			var output []byte
			err := tx.QueryRowContext(ctx, "SELECT reservation,output FROM moirai_aux WHERE job=? AND sequence=?", id, seq).Scan(&reservation, &output)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if output != nil || s.auxActive[reservation] {
				return nil
			}
			j.Watermark = seq
			j.NextRun = s.cfg.Now().Add(j.Every)
			if err := saveJob(ctx, tx, j); err != nil {
				return err
			}
		}
		if len(batch.Utterances) == 0 {
			return extractionProgress(ctx, tx, id, batch.Through)
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO moirai_tasks VALUES(?,?,?)", id, seq+1, []byte(input))
		return err
	})
}

func extractionProgress(ctx context.Context, tx *canon.Tx, id string, through int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO memory_extraction_cursor VALUES(?,?)
ON CONFLICT(job) DO UPDATE SET through=excluded.through`, id, through)
	return err
}

// ExtractionApply accepts only the result Core actually dispatched, and moves
// the source cursor in the same transaction as memory and the job receipt.
func ExtractionApply(id, agent, person string, resolve memory.ResolveEvidence) func(context.Context, *canon.Tx, jobapi.Task, json.RawMessage) error {
	return func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
		var cached []byte
		if err := tx.QueryRowContext(ctx, "SELECT output FROM moirai_aux WHERE job=? AND sequence=?", id, task.Sequence).Scan(&cached); err != nil {
			return jobapi.ErrInvalid
		}
		var a, b bytes.Buffer
		if json.Compact(&a, cached) != nil || json.Compact(&b, output) != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
			return jobapi.ErrInvalid
		}
		var batch memory.ExtractionBatch
		if json.Unmarshal(task.Input, &batch) != nil || batch.Agent != agent || batch.Person != person {
			return jobapi.ErrInvalid
		}
		// The memory callback consumes obsolete work without writing. Check it
		// explicitly here so that obsolescence cannot advance the source cursor.
		if err := memory.CheckExtractionTx(ctx, tx, batch, resolve); err != nil {
			if errors.Is(err, memory.ErrJobConflict) {
				return nil
			}
			return err
		}
		if err := memory.ExtractionApply(agent, person, resolve)(ctx, tx, task, output); err != nil {
			return err
		}
		return extractionProgress(ctx, tx, id, batch.Through)
	}
}
