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

// MessengerExtractionJob is the whole-main job that learns from the person's
// own messenger records. It is separate from the per-agent conversation
// extraction job and has its own budget, cadence and adoption record.
const MessengerExtractionJob = "messenger-extraction"

// ScheduleMessengerExtraction keeps one immutable pending batch. A batch with
// no utterance advances the scanned source cursors without a model task. A
// failed or abandoned call keeps its reservation; the next scan rebuilds a
// fresh task from the unchanged source cursors.
func (s *Service) ScheduleMessengerExtraction(ctx context.Context, id, person string, batch memory.MessengerBatch) error {
	d, ok := s.cfg.Definitions[id]
	if !ok || person == "" || batch.Person != person || batch.Person == "" {
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
		for _, source := range batch.Sources {
			var cursor int64
			if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT through FROM messenger_learning_cursor WHERE platform=? AND account=?),0)`,
				source.Platform, source.Account).Scan(&cursor); err != nil {
				return err
			}
			if cursor != source.After {
				return jobapi.ErrConflict
			}
		}
		var seq int64
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
			return messengerProgress(ctx, tx, batch.Sources)
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO moirai_tasks VALUES(?,?,?)", id, seq+1, []byte(input))
		return err
	})
}

func messengerProgress(ctx context.Context, tx *canon.Tx, sources []memory.MessengerSourceProgress) error {
	for _, source := range sources {
		if _, err := tx.ExecContext(ctx, `INSERT INTO messenger_learning_cursor(platform,account,through) VALUES(?,?,?)
ON CONFLICT(platform,account) DO UPDATE SET through=excluded.through`, source.Platform, source.Account, source.Through); err != nil {
			return err
		}
	}
	return nil
}

// MessengerExtractionApply accepts only the result Core actually dispatched and
// commits items, source cursors and the job receipt in one transaction.
func MessengerExtractionApply(id, person string, resolve memory.ResolveEvidence) func(context.Context, *canon.Tx, jobapi.Task, json.RawMessage) error {
	return func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
		var cached []byte
		if err := tx.QueryRowContext(ctx, "SELECT output FROM moirai_aux WHERE job=? AND sequence=?", id, task.Sequence).Scan(&cached); err != nil {
			return jobapi.ErrInvalid
		}
		var a, b bytes.Buffer
		if json.Compact(&a, cached) != nil || json.Compact(&b, output) != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
			return jobapi.ErrInvalid
		}
		var batch memory.MessengerBatch
		if json.Unmarshal(task.Input, &batch) != nil || batch.Person != person {
			return jobapi.ErrInvalid
		}
		// Obsolete work is consumed without writing items or advancing cursors.
		return memory.MessengerExtractionApply(person, resolve)(ctx, tx, task, output)
	}
}
