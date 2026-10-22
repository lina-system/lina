// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/persona"
)

type FirstRun struct {
	Agent          Agent
	SignInProvider string
}

// Default returns the original first conversation without initializing data or
// substituting another identity when that starter has been deleted.
func (s *Store) Default(ctx context.Context) (Agent, error) {
	var a Agent
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, "SELECT agent_id FROM agent_first_run WHERE singleton=1").Scan(&id); err != nil {
			return found(err)
		}
		var err error
		a, err = get(ctx, tx, id, false)
		return err
	})
	return a, err
}

func (s *Store) FirstRun(ctx context.Context, person string, usableAccount bool) (FirstRun, error) {
	if !present(person) {
		return FirstRun{}, ErrInvalid
	}
	if err := s.upgradeLinaPlaceholders(ctx, person); err != nil {
		return FirstRun{}, err
	}
	var id string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT agent_id FROM agent_first_run WHERE singleton=1").Scan(&id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		for _, starter := range persona.Starters() {
			if err := s.ensureStarter(ctx, starter, person); err != nil {
				return FirstRun{}, err
			}
		}
		err = s.db.Write(ctx, func(tx *canon.Tx) error {
			if err := tx.QueryRowContext(ctx, "SELECT agent_id FROM agent_starters WHERE preset='lina'").Scan(&id); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO agent_first_run VALUES(1,?) ON CONFLICT(singleton) DO NOTHING", id)
			return err
		})
	}
	if err != nil {
		return FirstRun{}, err
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return FirstRun{}, err
	}
	result := FirstRun{Agent: a}
	if !usableAccount {
		result.SignInProvider = "chatgpt"
	}
	return result, nil
}

// A receipt and its agent publish in one canon transaction. The receipt is
// independent of the mutable name, trash state and eventual tombstone. A retry
// can therefore finish a partial installation without replacing any identity.
func (s *Store) ensureStarter(ctx context.Context, starter persona.Starter, person string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var exists bool
		err := s.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM agent_starters WHERE preset=?)", starter.Key).Scan(&exists)
		})
		if err != nil || exists {
			return err
		}
		// Core publishes shipped layers before calling us. A missing catalog
		// entry is isolated to this preset, and never borrows another's layers.
		raw, _ := json.Marshal(Preset{ID: starter.Key})
		err = s.db.Write(ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO agent_presets VALUES(?,?) ON CONFLICT(id) DO NOTHING", starter.Key, raw)
			return err
		})
		if err != nil {
			return err
		}
		_, err = s.create(ctx, Create{Name: starter.Name, PersonID: person, Preset: starter.Key}, true)
		if !errors.Is(err, ErrConflict) {
			return err
		}
	}
}
