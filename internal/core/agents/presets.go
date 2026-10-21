// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"context"
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func presetAgents(ctx context.Context, tx *canon.Tx, id string) ([]Agent, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,name,preset,revision FROM agents WHERE preset=? ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var agents []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.Name, &a.Preset, &a.Revision); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// UpdatePreset publishes all affected agents atomically. Their filled settings,
// emotion, user voice and growth remain theirs, including while in the trash.
func (s *Store) UpdatePreset(ctx context.Context, p Preset, person string) error {
	if !present(p.ID) || !present(person) {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, name := range p.BlankSettings {
		if !present(name) || seen[name] {
			return ErrInvalid
		}
		seen[name] = true
	}
	var before []Agent
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; before, err = presetAgents(ctx, tx, p.ID); return err })
	if err != nil {
		return err
	}
	commits := make(map[string]string)
	for _, a := range before {
		old, err := s.revision(ctx, a.ID, a.Revision)
		if err != nil {
			return err
		}
		if old.Layers.Core == p.Core && old.Layers.DefaultVoice == p.DefaultVoice {
			continue
		}
		old.Layers.Core = p.Core
		old.Layers.DefaultVoice = p.DefaultVoice
		commit, err := s.save(ctx, a.ID, old.Commit, old.Layers, person)
		if err != nil {
			return err
		}
		commits[a.ID] = commit
	}
	raw, _ := json.Marshal(p)
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		current, err := presetAgents(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if len(before) != len(current) {
			return ErrConflict
		}
		for i, a := range current {
			if a != before[i] {
				return ErrConflict
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO agent_presets VALUES(?,?) ON CONFLICT(id) DO UPDATE SET record=excluded.record", p.ID, raw); err != nil {
			return err
		}
		for _, a := range before {
			if commit, ok := commits[a.ID]; ok {
				if err := publish(ctx, tx, a.ID, a.Revision+1, commit, person); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
