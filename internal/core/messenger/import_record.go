// SPDX-License-Identifier: Apache-2.0
package messenger

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// ImportRecordRow is the retained record of one memory import: the run it
// belongs to, what it wrote, and the person's learning choice with its count.
type ImportRecordRow struct {
	Batch             ImportBatch
	System, Instance  string
	Person            string
	ConfigHash        string
	SpoolHash         string
	Learning          bool
	LearningMessages  int
	LearningThrough   time.Time
	Report            any
	BackupGeneration  string
}

// RecordImport retains the run's report with the batch it wrote. The row is the
// adapter's account of itself; the source copy it read is already gone.
func (s *Store) RecordImport(ctx context.Context, record ImportRecordRow) error {
	if !validName(record.Batch.ID) || !validName(record.System) || !validName(record.Instance) ||
		!validName(record.Person) || record.LearningMessages < 0 {
		return ErrInvalid
	}
	if !record.Learning {
		record.LearningMessages = 0
		record.LearningThrough = time.Time{}
	}
	encoded, err := json.Marshal(record.Report)
	if err != nil {
		return err
	}
	created := record.Batch.CreatedAt
	if created.IsZero() {
		created = s.now()
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := ensureImportBatch(ctx, tx, record.Batch, s.now()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO messenger_import(batch_id,person,system,instance,config_hash,spool_hash,learning,learning_messages,learning_through,report,backup_generation,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,'',?)
ON CONFLICT(batch_id) DO UPDATE SET report=excluded.report,
 learning=excluded.learning,learning_messages=excluded.learning_messages,learning_through=excluded.learning_through`,
			record.Batch.ID, record.Person, record.System, record.Instance, record.ConfigHash, record.SpoolHash,
			boolInt(record.Learning), record.LearningMessages, formatOptional(record.LearningThrough), string(encoded),
			created.UTC().Format(time.RFC3339Nano))
		return err
	})
}

// SetImportBackup names the backup generation that captured the run's records.
func (s *Store) SetImportBackup(ctx context.Context, batch, generation string) error {
	if !validName(batch) || generation == "" {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE messenger_import SET backup_generation=? WHERE batch_id=?", generation, batch)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// SetLearningWatermark records how far a source's history is learned from. An
// import that covers older history moves it back to the start of that history
// when the person turned learning on, and forward to its end when they left it
// off, so untouched history stays search-only either way.
func (s *Store) SetLearningWatermark(ctx context.Context, person, platform, account string, at time.Time) error {
	if !validName(person) || !validName(platform) || !validName(account) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE messenger_source SET learning_watermark=? WHERE platform=? AND account=? AND person=?",
			formatOptional(at), platform, account, person)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
