// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

// StateRevision is an opaque compare-and-swap handle for the agent's mutable
// persona records. It does not mint a revision of the four fixed voice layers.
func StateRevision(state State) string {
	raw, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// FillBlank is called by the Moirai flow after canonical event confirmation.
// Preset-defined keys were copied when this agent was created. Neither a fill
// nor later preset publication can change that per-agent definition set.
func (s *Store) FillBlank(ctx context.Context, id, expected, name, value, event, person string) (bool, error) {
	masked, err := secrets.Mask(value)
	if err != nil || masked.Text() != value || !utf8.ValidString(value) || !present(value) || len(value) > 8192 || !present(name) || !present(event) || !present(person) || expected == "" {
		return false, ErrInvalid
	}
	filled := false
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := get(ctx, tx, id, false); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT state FROM agents WHERE id=?", id).Scan(&raw); err != nil {
			return err
		}
		var state State
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		if StateRevision(state) != expected {
			return ErrConflict
		}
		previous, defined := state.Settings[name]
		if !defined {
			return ErrInvalid
		}
		if previous != "" {
			if previous == value {
				return nil
			}
			return ErrConflict
		}
		if state.SettingSources == nil {
			state.SettingSources = map[string]string{}
		}
		state.Settings[name] = value
		state.SettingSources[name] = event
		raw, err = json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET state=? WHERE id=?", raw, id); err != nil {
			return err
		}
		if err := action(ctx, tx, id, person, "fill_setting:"+name, 0); err != nil {
			return err
		}
		filled = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return filled, nil
}
