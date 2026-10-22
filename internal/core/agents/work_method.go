// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/persona"
)

type WorkMethodFill struct {
	Value  json.RawMessage `json:"value"`
	Source string          `json:"source"`
}
type WorkMethodSetting struct {
	WorkMethodFill
	Retired bool `json:"retired"`
}
type WorkMethodSettings struct {
	Revision string                       `json:"revision"`
	Fill     []string                     `json:"fill"`
	Values   map[string]json.RawMessage   `json:"values"`
	Filled   map[string]WorkMethodSetting `json:"filled"`
}

// ProjectWorkMethod combines one prepared record with an agent's evidence-backed
// fills. Retired values remain visible but never override the preset's defaults.
func ProjectWorkMethod(p Preparation, state State) WorkMethodSettings {
	record := p.WorkMethod
	if record == "" {
		record = persona.EmptyWorkMethod("")
	}
	result := WorkMethodSettings{Revision: p.WorkMethodRevision, Fill: persona.WorkMethodFillKeys(record), Values: persona.WorkMethodValues(record), Filled: map[string]WorkMethodSetting{}}
	for key, fill := range state.WorkMethodFills {
		_, err := persona.FillWorkMethod(record, key, fill.Value)
		active := slices.Contains(result.Fill, key) && err == nil
		result.Filled[key] = WorkMethodSetting{WorkMethodFill: fill, Retired: !active}
		if active {
			result.Values[key] = append(json.RawMessage(nil), fill.Value...)
		}
	}
	return result
}

// FillWorkMethod is the conversation writer's CAS write after person-event
// confirmation. Both persona and state snapshots must still be current.
func (s *Store) FillWorkMethod(ctx context.Context, id string, revision int64, expected, key string, value json.RawMessage, event, person string) (bool, error) {
	if expected == "" || !present(event) || !present(person) {
		return false, ErrInvalid
	}
	prepared, err := s.revision(ctx, id, revision)
	if err != nil {
		return false, err
	}
	updated, err := persona.FillWorkMethod(prepared.WorkMethod, key, value)
	if err != nil {
		return false, ErrInvalid
	}
	value = persona.WorkMethodValues(updated)[key]
	filled := false
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		a, err := get(ctx, tx, id, false)
		if err != nil {
			return err
		}
		if a.Revision != revision {
			return ErrConflict
		}
		state, err := readState(ctx, tx, id)
		if err != nil {
			return err
		}
		if StateRevision(state) != expected {
			return ErrConflict
		}
		if previous, ok := state.WorkMethodFills[key]; ok {
			if bytes.Equal(previous.Value, value) {
				return nil
			}
			return ErrConflict
		}
		if state.WorkMethodFills == nil {
			state.WorkMethodFills = map[string]WorkMethodFill{}
		}
		state.WorkMethodFills[key] = WorkMethodFill{Value: value, Source: event}
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE agents SET state=? WHERE id=?", raw, id); err != nil {
			return err
		}
		if err := action(ctx, tx, id, person, "fill_work_method:"+key, 0); err != nil {
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
