// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"context"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Select records an explicit conversation choice independently of initialization.
func (s *Store) Select(ctx context.Context, id, person string) error {
	if !present(person) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := get(ctx, tx, id, false); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO agent_selection VALUES(?,?)
ON CONFLICT(person_id) DO UPDATE SET agent_id=excluded.agent_id`, person, id)
		return err
	})
}

// Selected excludes trashed and expired identities. The caller decides how a
// new surface opens when that person's last conversation is no longer available.
func (s *Store) Selected(ctx context.Context, person string) (Agent, error) {
	var a Agent
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT agent_id FROM agent_selection WHERE person_id=?", person).Scan(&id); err != nil {
			return found(err)
		}
		var err error
		a, err = get(ctx, tx, id, false)
		return err
	})
	return a, err
}
