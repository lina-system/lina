// SPDX-License-Identifier: Apache-2.0
package assets

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Trash hides all revisions without moving or deleting their bytes. The
// retraction/backup coordinator consumes the durable transition records for its
// removal list and owns the 30-day expiry and permanent-deletion workflow.
func (s *Store) Trash(ctx context.Context, id string, actor Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.setTrash(ctx, id, actor, true)
	return err
}

func (s *Store) Restore(ctx context.Context, id string, actor Actor) (Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setTrash(ctx, id, actor, false)
}

func (s *Store) setTrash(ctx context.Context, id string, actor Actor, trash bool) (Asset, error) {
	if !actor.valid() {
		return Asset{}, ErrInvalid
	}
	a, err := s.get(ctx, id, true)
	if err != nil {
		return Asset{}, err
	}
	if (a.TrashedAt != 0) == trash {
		return a, nil
	}
	now := time.Now().UnixNano()
	a.TrashedAt = 0
	action := "restore"
	if trash {
		a.TrashedAt = now
		action = "trash"
	}
	a.Actor = actor
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE assets SET trashed_at=?,person_id=?,agent_id=? WHERE id=?", a.TrashedAt, actor.PersonID, actor.AgentID, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO asset_trash_events(asset_id,action,at,person_id,agent_id) VALUES (?,?,?,?,?)", id, action, now, actor.PersonID, actor.AgentID)
		return err
	})
	if err != nil {
		return Asset{}, err
	}
	return a, nil
}

// ListTrash is an explicit administrative view, not a normal asset read.
func (s *Store) ListTrash(ctx context.Context) (items []Asset, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, assetQuery+" WHERE a.trashed_at != 0 ORDER BY a.id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				return err
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	return
}
