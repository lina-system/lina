// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/persona"
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
	return s.updatePreset(ctx, p, person, nil)
}

func (s *Store) updatePreset(ctx context.Context, p Preset, person string, expected *Preset) error {
	if !present(p.ID) || !present(person) {
		return ErrInvalid
	}
	key := ""
	if persona.IsStarter(p.ID) {
		key = p.ID
	}
	if p.WorkMethod == "" {
		p.WorkMethod = persona.EmptyWorkMethod(key)
	}
	method, err := persona.ParseWorkMethod([]byte(p.WorkMethod), key)
	if err != nil {
		return ErrInvalid
	}
	p.WorkMethod = method
	triple, _ := json.Marshal(struct{ Core, Voice, Method string }{p.Core, p.DefaultVoice, method})
	p.Revision = persona.WorkMethodRevision(string(triple))
	seen := map[string]bool{}
	for _, name := range p.BlankSettings {
		if !present(name) || seen[name] {
			return ErrInvalid
		}
		seen[name] = true
	}
	var before []Agent
	err = s.db.Read(ctx, func(tx *canon.Tx) error { var err error; before, err = presetAgents(ctx, tx, p.ID); return err })
	if err != nil {
		return err
	}
	commits := make(map[string]string)
	for _, a := range before {
		old, err := s.revision(ctx, a.ID, a.Revision)
		if err != nil {
			return err
		}
		if old.Layers.Core == p.Core && old.Layers.DefaultVoice == p.DefaultVoice && old.WorkMethod == p.WorkMethod {
			continue
		}
		old.Layers.Core = p.Core
		old.Layers.DefaultVoice = p.DefaultVoice
		commit, err := s.save(ctx, a.ID, old.Commit, old.Layers, person, p.WorkMethod)
		if err != nil {
			return err
		}
		commits[a.ID] = commit
	}
	raw, _ := json.Marshal(p)
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if expected != nil {
			current, err := preset(ctx, tx, p.ID)
			if errors.Is(err, ErrNotFound) {
				current, err = Preset{ID: p.ID}, nil
			}
			if err != nil {
				return err
			}
			if !samePreset(current, *expected) {
				return ErrConflict
			}
		}
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

// UpdatePresetLayers preserves preset metadata owned by other features while
// publishing the shipped pair. Existing agents keep all agent-owned state.
func (s *Store) UpdatePresetLayers(ctx context.Context, id, core, voice, person string) error {
	return s.updatePresetMaterial(ctx, id, core, voice, nil, person)
}

// UpdatePresetMaterial publishes the triple without changing blank definitions.
func (s *Store) UpdatePresetMaterial(ctx context.Context, id, core, voice, method, person string) error {
	return s.updatePresetMaterial(ctx, id, core, voice, &method, person)
}
func (s *Store) updatePresetMaterial(ctx context.Context, id, core, voice string, method *string, person string) error {
	var before Preset
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		before, err = preset(ctx, tx, id)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		before, err = Preset{ID: id}, nil
	}
	if err != nil {
		return err
	}
	next := before
	next.Core, next.DefaultVoice = core, voice
	if method != nil {
		next.WorkMethod = *method
	}
	return s.updatePreset(ctx, next, person, &before)
}

// Only the exact shipped placeholders are eligible. Owner-supplied material and
// each agent's personal layers remain intact; existing turns keep their revision.
func (s *Store) upgradeLinaPlaceholders(ctx context.Context, person string) error {
	const core = "<!-- SPDX-License-Identifier: Apache-2.0 -->\nLINA preset core placeholder. The project owner supplies the persona material.\n"
	const voice = "<!-- SPDX-License-Identifier: Apache-2.0 -->\nLINA preset default voice placeholder. The project owner supplies the voice material.\n"
	for {
		var before Preset
		err := s.db.Read(ctx, func(tx *canon.Tx) error {
			var err error
			before, err = preset(ctx, tx, "lina")
			return err
		})
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		next := before
		if next.Core == core {
			next.Core = ""
		}
		if next.DefaultVoice == voice {
			next.DefaultVoice = ""
		}
		if samePreset(before, next) {
			return nil
		}
		err = s.updatePreset(ctx, next, person, &before)
		if !errors.Is(err, ErrConflict) {
			return err
		}
	}
}
