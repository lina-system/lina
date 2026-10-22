// SPDX-License-Identifier: Apache-2.0
package agents

import "context"

// ChangeVoice is the user-voice mutation used by the Moirai persona-change
// flow after binding the person's request to this agent. Preparation keeps an
// already pinned turn on its previous revision.
func (s *Store) ChangeVoice(ctx context.Context, id string, expected int64, voice, person string) (Agent, error) {
	current, err := s.revision(ctx, id, expected)
	if err != nil {
		return Agent{}, err
	}
	current.Layers.UserVoice = voice
	return s.Revise(ctx, id, expected, current.Layers, person)
}

// UndoVoice restores the preceding user voice as a new revision. The handle
// names the revision being undone; unrelated or intervening edits are refused.
func (s *Store) UndoVoice(ctx context.Context, id string, expected int64, person string) (Agent, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Agent{}, err
	}
	if a.Revision != expected {
		return Agent{}, ErrConflict
	}
	if expected <= 1 {
		return Agent{}, ErrInvalid
	}
	current, err := s.revision(ctx, id, expected)
	if err != nil {
		return Agent{}, err
	}
	previous, err := s.revision(ctx, id, expected-1)
	if err != nil {
		return Agent{}, err
	}
	restored := current.Layers
	restored.UserVoice = previous.Layers.UserVoice
	if restored != previous.Layers || current.Layers.UserVoice == previous.Layers.UserVoice {
		return Agent{}, ErrInvalid
	}
	return s.Revise(ctx, id, expected, restored, person)
}
