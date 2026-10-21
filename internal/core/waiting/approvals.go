// SPDX-License-Identifier: Apache-2.0
package waiting

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
)

// ApprovalSource reads the existing approval records. The trusted connection
// owner supplies Decider; no waiting-row payload can invent authentication,
// change the surface, or turn text into an approval.
type ApprovalSource struct {
	Service   *authority.Service
	Decider   authority.Decider
	AgentName func(string) string
}

func (s *ApprovalSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT approval_id FROM authority_approvals WHERE json_extract(record,'$.Person')=? AND json_extract(record,'$.State')=? ORDER BY approval_id", person, authority.Pending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *ApprovalSource) Read(ctx context.Context, tx *canon.Tx, id string) (Item, error) {
	var data []byte
	if err := tx.QueryRowContext(ctx, "SELECT record FROM authority_approvals WHERE approval_id=?", id).Scan(&data); err != nil {
		return Item{}, err
	}
	var a authority.Approval
	if err := json.Unmarshal(data, &a); err != nil {
		return Item{}, err
	}
	v := view.Waiting{Kind: view.Decide, Sentence: a.What, Effort: view.FiveMinutes}
	if s.AgentName != nil {
		v.AgentName = s.AgentName(a.Agent)
	}
	for _, choice := range a.Choices {
		v.Answers = append(v.Answers, string(choice))
	}
	return Item{Person: a.Person, Agent: a.Agent, Revision: a.Epoch, Reason: OutsideEffect, Blocking: true, Irreversible: !a.Undoable, Finished: a.State != authority.Pending, View: v}, nil
}

// The authority's terminal state already marks the source done.
func (s *ApprovalSource) Done(context.Context, *canon.Tx, string) error { return nil }
func (s *ApprovalSource) AnswerRecorded(ctx context.Context, person string, ref Ref, value string) (Decision, error) {
	if s.Service == nil {
		return Decision{}, ErrInvalid
	}
	if person != s.Decider.Person {
		return Decision{}, ErrPerson
	}
	if ref.Revision != s.Decider.Epoch {
		return Decision{}, ErrStale
	}
	// Authority re-reads pending state, epoch, owner and choice in its fenced
	// transaction and records both the person and requesting agent there.
	if err := s.Service.Answer(ctx, ref.ID, s.Decider, authority.Decision{Choice: authority.Choice(value)}); err != nil {
		if errors.Is(err, authority.ErrStale) {
			return Decision{}, ErrStale
		}
		return Decision{}, err
	}
	a, err := s.Service.Approval(ctx, ref.ID)
	if err != nil {
		return Decision{}, err
	}
	return approvalDecision(a), nil
}
func approvalDecision(a authority.Approval) Decision {
	return Decision{Ref: Ref{ID: a.ID, Revision: a.Epoch}, Person: a.AnsweredBy, Agent: a.Agent, Answer: string(a.Choice), At: a.AnsweredAt}
}
func (s *ApprovalSource) RecordedDecisions(ctx context.Context, tx *canon.Tx, person string) ([]Decision, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM authority_approvals WHERE json_extract(record,'$.AnsweredBy')=? ORDER BY approval_id", person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var decisions []Decision
	for rows.Next() {
		var data []byte
		var a authority.Approval
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &a); err != nil {
			return nil, err
		}
		decisions = append(decisions, approvalDecision(a))
	}
	return decisions, rows.Err()
}
