// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
)

func (s *Store) List(ctx context.Context) ([]Agent, error) {
	result := []Agent{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id,name,preset,revision FROM agents WHERE trashed_at=0 ORDER BY name,id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Agent
			if err := rows.Scan(&a.ID, &a.Name, &a.Preset, &a.Revision); err != nil {
				return err
			}
			result = append(result, a)
		}
		return rows.Err()
	})
	return result, err
}

func (s *Store) Rename(ctx context.Context, id, name, person string) (Agent, error) {
	if !present(name) || !present(person) {
		return Agent{}, ErrInvalid
	}
	var a Agent
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		var err error
		a, err = get(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET name=? WHERE id=?", name, id); err != nil {
			return err
		}
		a.Name = name
		return action(ctx, tx, id, person, "rename", 0)
	})
	return a, err
}
