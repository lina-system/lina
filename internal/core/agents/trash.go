// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

const TrashRetention = 30 * 24 * time.Hour

func (s *Store) Bind(ctx context.Context, channel, id, person string) error {
	if !present(channel) || !present(person) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := get(ctx, tx, id, false); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO agent_bindings VALUES(?,?,?) ON CONFLICT(channel) DO UPDATE SET agent_id=excluded.agent_id,person_id=excluded.person_id", channel, id, person)
		return err
	})
}
func (s *Store) ResolveBinding(ctx context.Context, channel string) (string, error) {
	var id string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return found(tx.QueryRowContext(ctx, "SELECT b.agent_id FROM agent_bindings b JOIN agents a ON a.id=b.agent_id WHERE b.channel=? AND a.trashed_at=0", channel).Scan(&id))
	})
	return id, err
}
func (s *Store) Delete(ctx context.Context, id, person string, now time.Time) error {
	if !present(person) || now.UnixNano() <= 0 {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := get(ctx, tx, id, true); err != nil {
			return err
		}
		// A repeat deletion must not extend the recovery window.
		result, err := tx.ExecContext(ctx, "UPDATE agents SET trashed_at=? WHERE id=? AND trashed_at=0", now.UnixNano(), id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM agent_bindings WHERE agent_id=?", id); err != nil {
			return err
		}
		return action(ctx, tx, id, person, "trash", now.UnixNano())
	})
}
func (s *Store) Restore(ctx context.Context, id, person string, now time.Time) error {
	if !present(person) || now.UnixNano() <= 0 {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		var at int64
		if err := tx.QueryRowContext(ctx, "SELECT trashed_at FROM agents WHERE id=?", id).Scan(&at); err != nil {
			return found(err)
		}
		if at == 0 {
			return nil
		}
		if now.Before(time.Unix(0, at)) {
			return ErrInvalid
		}
		if !now.Before(time.Unix(0, at).Add(TrashRetention)) {
			return ErrExpired
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET trashed_at=0 WHERE id=?", id); err != nil {
			return err
		}
		return action(ctx, tx, id, person, "restore", now.UnixNano())
	})
}

type Tombstone struct {
	AgentID   string
	DeletedAt time.Time
}

func (s *Store) Tombstone(ctx context.Context, id string) (Tombstone, error) {
	var at int64
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return found(tx.QueryRowContext(ctx, "SELECT deleted_at FROM agent_tombstones WHERE agent_id=?", id).Scan(&at))
	})
	if err != nil {
		return Tombstone{}, err
	}
	return Tombstone{id, time.Unix(0, at)}, nil
}

// Expire first removes access to content and records tombstones atomically.
// Filesystem cleanup is retryable; tombstones remain even when removal fails.
// The deletion coordinator supplies time and propagates deletion to other stores.
func (s *Store) Expire(ctx context.Context, now time.Time) error {
	if now.UnixNano() <= 0 {
		return ErrInvalid
	}
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id,trashed_at FROM agents WHERE trashed_at>0 AND trashed_at<=?", now.Add(-TrashRetention).UnixNano())
		if err != nil {
			return err
		}
		type due struct {
			id string
			at int64
		}
		var expired []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.id, &d.at); err != nil {
				rows.Close()
				return err
			}
			expired = append(expired, d)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, d := range expired {
			if _, err := tx.ExecContext(ctx, "INSERT INTO agent_tombstones VALUES(?,?)", d.id, time.Unix(0, d.at).Add(TrashRetention).UnixNano()); err != nil {
				return err
			}
			for _, table := range []string{"agent_preparations", "agent_bindings", "agent_revisions"} {
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE agent_id=?", d.id); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM agents WHERE id=?", d.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var ids []string
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT agent_id FROM agent_tombstones")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validID(id) {
			return ErrCorrupt
		}
		if err := s.root.RemoveAll(repoPath(id)); err != nil {
			return err
		}
	}
	return syncDir(s.root, "versions/agents")
}
