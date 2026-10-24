// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/protocol"
)

// bindingService is implemented by the conversation router, which resolves the
// conversation that owns the input event.
type bindingService interface {
	Binding(context.Context, clientsrv.Session, protocol.BindingGet) (protocol.Binding, error)
	MoveBinding(context.Context, clientsrv.Session, protocol.BindingMove, string) (protocol.BindingMoveResult, error)
	BindingStats(context.Context, clientsrv.Session) (protocol.BindingStats, error)
	ExportBindingCases(context.Context, clientsrv.Session) (protocol.BindingExportResult, error)
}

var _ bindingService = (*conversationRouter)(nil)

// bindingView resolves the conversation, agent and event that own the person
// input. The event is returned whole because the chip reads that person's
// state and choices.
func (s *conversationRouter) bindingView(ctx context.Context, session clientsrv.Session, inputID string) (*conversationRuntime, ledger.Event, error) {
	if err := s.root.authorize(session, nil); err != nil {
		return nil, ledger.Event{}, err
	}
	event, err := s.root.journal().Get(ctx, inputID)
	if err != nil {
		return nil, ledger.Event{}, outsideManagement()
	}
	if event.PersonID != session.PersonID || event.SpeakerKind != guard.Person || event.AgentID == "" {
		return nil, ledger.Event{}, outsideManagement()
	}
	if event.ConversationID == "main:"+event.AgentID {
		view, err := s.view(ctx, session, event.AgentID)
		return view, event, err
	}
	return newConversationView(s.root, event.AgentID, event.ConversationID), event, nil
}

func (s *conversationRouter) Binding(ctx context.Context, session clientsrv.Session, p protocol.BindingGet) (protocol.Binding, error) {
	view, event, err := s.bindingView(ctx, session, p.InputID)
	if err != nil {
		return protocol.Binding{}, err
	}
	out := protocol.Binding{InputID: p.InputID, Candidates: []protocol.BindingCandidate{}, Moves: []protocol.BindingMoveResult{}}
	err = view.db.Read(ctx, func(tx *canon.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, "SELECT body FROM conv_event WHERE id=? AND scaffold_kind=?", inputBindingID(p.InputID), inputBindingKind).Scan(&raw); err != nil {
			return err
		}
		var decision binding.Decision
		if err := json.Unmarshal([]byte(raw), &decision); err != nil {
			return err
		}
		choices, err := readInputChoices(ctx, tx, event)
		if err != nil {
			return err
		}
		current := currentChoice(choices)
		offers := binding.Offers(decision, current.Target)
		for _, offer := range offers {
			name := offer.Name
			if name == "" {
				name = offer.Target.Class
			}
			out.Candidates = append(out.Candidates, protocol.BindingCandidate{ID: candidateID(offer.Target), Title: name, Kind: offer.Target.Kind, State: offer.Target.Class})
		}
		out.Target = targetWire(current.Target)
		chip, err := view.boundChip(ctx, tx, event)
		if err != nil {
			return err
		}
		out.Chip = *chipWire(p.InputID, chip)
		rows, err := tx.QueryContext(ctx, "SELECT record FROM binding_moves WHERE input_event=? ORDER BY moved_at, id", p.InputID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body []byte
			if err = rows.Scan(&body); err != nil {
				return err
			}
			var move binding.Move
			if json.Unmarshal(body, &move) != nil {
				continue
			}
			out.Moves = append(out.Moves, moveWire(move))
		}
		return rows.Err()
	})
	if err != nil {
		return protocol.Binding{}, managementError(err)
	}
	return out, nil
}

// candidateID names a candidate on the wire without inventing one: an existing
// item keeps its id, while new and none use their class.
func candidateID(t binding.Target) string {
	if t.Class == "existing" {
		return t.ID
	}
	return t.Class
}

func (s *conversationRouter) MoveBinding(ctx context.Context, session clientsrv.Session, p protocol.BindingMove, key string) (protocol.BindingMoveResult, error) {
	view, _, err := s.bindingView(ctx, session, p.InputID)
	if err != nil {
		return protocol.BindingMoveResult{}, err
	}
	move, err := view.moveBinding(ctx, p.InputID, targetFromWire(p.Target), key)
	if err != nil {
		if errors.Is(err, errOutsideBinding) {
			return protocol.BindingMoveResult{}, &clientsrv.OperationError{Code: protocol.ErrorMalformed}
		}
		return protocol.BindingMoveResult{}, managementError(err)
	}
	return moveWire(move), nil
}

func (s *conversationRouter) BindingStats(ctx context.Context, session clientsrv.Session) (protocol.BindingStats, error) {
	if err := s.root.authorize(session, nil); err != nil {
		return protocol.BindingStats{}, err
	}
	stats, err := s.root.bindingStats(ctx)
	if err != nil {
		return protocol.BindingStats{}, managementError(err)
	}
	out := protocol.BindingStats{Bindings: number(stats.Bindings), Moves: number(stats.Moves), PerHundred: number(stats.PerHundred), Deciders: []protocol.BindingDeciderCount{}}
	for _, c := range stats.Deciders {
		out.Deciders = append(out.Deciders, protocol.BindingDeciderCount{Decider: c.Decider, Bindings: number(c.Bindings), Moves: number(c.Moves), PerHundred: number(c.PerHundred)})
	}
	return out, nil
}

func (s *conversationRouter) ExportBindingCases(ctx context.Context, session clientsrv.Session) (protocol.BindingExportResult, error) {
	if err := s.root.authorize(session, nil); err != nil {
		return protocol.BindingExportResult{}, err
	}
	dir, cases, err := s.root.exportBindingCases(ctx)
	if err != nil {
		return protocol.BindingExportResult{}, managementError(err)
	}
	return protocol.BindingExportResult{Directory: dir, Cases: number(cases)}, nil
}

func moveWire(move binding.Move) protocol.BindingMoveResult {
	out := protocol.BindingMoveResult{MoveID: move.ID, InputID: move.InputEvent, From: targetWire(move.From), To: targetWire(move.To), Decider: move.Decider, Effects: []protocol.BindingEffectOutcome{}}
	for _, effect := range move.Effects {
		item := protocol.BindingEffectOutcome{Kind: effect.Kind, Item: effect.Item, Moved: effect.Moved}
		if effect.Reason != "" {
			reason := effect.Reason
			item.Reason = &reason
		}
		if effect.Receipt != "" {
			receipt := effect.Receipt
			item.Receipt = &receipt
		}
		out.Effects = append(out.Effects, item)
	}
	return out
}
