// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func overlaps(a, b string) bool {
	within := func(root, path string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a)
}
func taskDirectory(t Task, dir string) (bool, error) {
	for _, root := range t.ArtifactRoots {
		real, err := filepath.EvalSymlinks(root)
		if err != nil {
			// An unavailable root cannot hide a lexical overlap. Disjoint unavailable
			// roots cannot currently contain this existing directory.
			if overlaps(root, dir) {
				return false, err
			}
			continue
		}
		if overlaps(real, dir) {
			return true, nil
		}
	}
	return false, nil
}

// ReserveWorker runs before any worker process or turn starts. It records even
// untracked sessions, so a later transfer cannot adopt their directory silently.
func (s *Service) ReserveWorker(ctx context.Context, id, dir string) error {
	if id == "" || !filepath.IsAbs(dir) {
		return ErrInvalid
	}
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT record FROM work_cards")
		if err != nil {
			return err
		}
		var task string
		for rows.Next() {
			var raw []byte
			var card Card
			if err = rows.Scan(&raw); err != nil {
				break
			}
			if err = json.Unmarshal(raw, &card); err != nil {
				break
			}
			if !card.Task.ExternalControl {
				continue
			}
			var match bool
			match, err = taskDirectory(card.Task, dir)
			if err != nil {
				break
			}
			if !match {
				continue
			}
			if task != "" {
				err = ErrState
				break
			}
			task = card.Task.ID
			if card.Active {
				err = ErrActiveRun
				break
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if task != "" {
			if err = mayDirect(ctx, tx, task); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO work_worker_reservations(worker_id,task_id,directory) VALUES(?,?,?)", id, task, dir)
		return err
	})
}
func (s *Service) CheckWorker(ctx context.Context, id string) error {
	return s.db.Read(ctx, func(tx *canon.Tx) error {
		var task string
		if err := tx.QueryRowContext(ctx, "SELECT task_id FROM work_worker_reservations WHERE worker_id=?", id).Scan(&task); err != nil {
			return err
		}
		if task == "" {
			return nil
		}
		return mayDirect(ctx, tx, task)
	})
}

// ReleaseWorker requires an observed terminal turn (or proof no turn started).
// Disconnection, timeout and Core recovery do not call this method.
func (s *Service) ReleaseWorker(ctx context.Context, id string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM work_worker_reservations WHERE worker_id=?", id)
		return err
	})
}
func workerInDirectory(ctx context.Context, tx *canon.Tx, t Task) error {
	rows, err := tx.QueryContext(ctx, "SELECT directory FROM work_worker_reservations")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var dir string
		if err = rows.Scan(&dir); err != nil {
			return err
		}
		match, err := taskDirectory(t, dir)
		if err != nil {
			return err
		}
		if match {
			return ErrActiveRun
		}
	}
	return rows.Err()
}
