// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/canon"
)

const AutomatedMessageLimit = 12

var ErrMessageLimit = errors.New("worker coordination limit reached; a person or manager decision is needed")

// AdmitMessage counts a new automatic exchange once across retries/restarts.
// It grants no authority to perform the requested work.
func AdmitMessage(ctx context.Context, db *canon.Store, task, id, body string) error {
	if task == "" || id == "" {
		return errors.New("missing worker message identity")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	return db.Write(ctx, func(tx *canon.Tx) error {
		var prior string
		err := tx.QueryRowContext(ctx, "SELECT fingerprint FROM worker_message_admissions WHERE task_id=? AND message_id=?", task, id).Scan(&prior)
		if err == nil {
			if prior != hash {
				return errors.New("worker message identity conflict")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO worker_message_budget(task_id,used) VALUES(?,0)", task); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE worker_message_budget SET used=used+1 WHERE task_id=? AND used<?", task, AutomatedMessageLimit)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrMessageLimit
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO worker_message_admissions(task_id,message_id,fingerprint) VALUES(?,?,?)", task, id, hash)
		return err
	})
}

// ResetMessages accepts a trusted decision identity only. Replaying an old
// person input or manager control does not replenish the automatic budget.
func ResetMessages(ctx context.Context, db *canon.Store, task, decision string) error {
	if task == "" || decision == "" {
		return errors.New("missing worker decision identity")
	}
	return db.Write(ctx, func(tx *canon.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO worker_message_decisions(task_id,decision_id) VALUES(?,?)", task, decision)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO worker_message_budget(task_id,used) VALUES(?,0) ON CONFLICT(task_id) DO UPDATE SET used=0", task)
		return err
	})
}
