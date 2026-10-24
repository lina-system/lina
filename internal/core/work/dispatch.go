// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// DispatchID is the id of the dispatch a tool call admits in a turn, and of the
// task it creates. A goal action dispatches with its own id as the call, so a
// reader joins a goal step to its card through the action.
func DispatchID(turn, call string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(turn+"\x00"+call)))
}

func overlaps(a, b string) bool {
	within := func(root, path string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return within(a, b) || within(b, a)
}
func taskDirectory(t Task, dir string) (bool, error) {
	for _, root := range t.ArtifactRoots {
		real, err := artifactDirectory(root)
		if err != nil {
			return false, err
		}
		if overlaps(real, dir) {
			return true, nil
		}
	}
	return false, nil
}

// A worker can create missing artifact directories beneath its workdir. Resolve
// the existing prefix before comparing paths, retaining every missing segment.
func artifactDirectory(root string) (string, error) {
	ancestor := root
	for {
		real, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			suffix, err := filepath.Rel(ancestor, root)
			if err != nil {
				return "", err
			}
			return filepath.Join(real, suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// An existing dangling symlink is unresolved, not a missing name that
		// can be appended lexically. Other inspection errors also hold admission.
		if _, statErr := os.Lstat(ancestor); !errors.Is(statErr, os.ErrNotExist) {
			return "", errors.Join(err, statErr)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		ancestor = parent
	}
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
	return s.ReserveWorkerDirectories(ctx, []WorkerDirectory{{ID: id, Path: dir}})
}

// WorkerDirectory identifies one directory reservation for an execution.
type WorkerDirectory struct{ ID, Path string }

// ReserveWorkerDirectories fences every directory of one execution atomically.
// Missing directories are reserved before their creation. Multiple directories
// belonging to the same external task retain one task binding and all paths.
// Callers check and release the complete set of reservation IDs together.
func (s *Service) ReserveWorkerDirectories(ctx context.Context, directories []WorkerDirectory) error {
	if len(directories) == 0 {
		return ErrInvalid
	}
	resolved := make([]WorkerDirectory, 0, len(directories))
	ids := make(map[string]bool)
	for _, directory := range directories {
		if directory.ID == "" || ids[directory.ID] || !filepath.IsAbs(directory.Path) || filepath.Clean(directory.Path) != directory.Path {
			return ErrInvalid
		}
		ids[directory.ID] = true
		real, err := artifactDirectory(directory.Path)
		if err != nil {
			return err
		}
		resolved = append(resolved, WorkerDirectory{ID: directory.ID, Path: real})
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		bound := make(map[string]bool)
		for _, directory := range resolved {
			if err := reserveWorkerDirectory(ctx, tx, directory, bound); err != nil {
				return err
			}
		}
		return nil
	})
}

func reserveWorkerDirectory(ctx context.Context, tx *canon.Tx, directory WorkerDirectory, bound map[string]bool) error {
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
		match, err = taskDirectory(card.Task, directory.Path)
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
		if bound[task] {
			task = ""
		} else {
			bound[task] = true
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO work_worker_reservations(worker_id,task_id,directory) VALUES(?,?,?)", directory.ID, task, directory.Path)
	return err
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
func (s *Service) ReleaseWorker(ctx context.Context, ids ...string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "DELETE FROM work_worker_reservations WHERE worker_id=?", id); err != nil {
				return err
			}
		}
		return nil
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
