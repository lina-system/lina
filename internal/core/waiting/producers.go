// SPDX-License-Identifier: Apache-2.0
package waiting

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
	"github.com/thisisjun786/lina/internal/core/work"
)

// RoundSource projects each unanswered question from the existing work rounds.
// Answers continue through the grouped question form owned by Rounds.
type RoundSource struct{ AgentName func(string) string }

func (s RoundSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,record FROM work_waiting_rounds WHERE person=? ORDER BY id", person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		var raw []byte
		var r Round
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		for i := range r.Questions {
			ids = append(ids, id+":"+strconv.Itoa(i))
		}
	}
	return ids, rows.Err()
}
func (s RoundSource) Read(ctx context.Context, tx *canon.Tx, id string) (Item, error) {
	split := strings.LastIndexByte(id, ':')
	if split < 1 {
		return Item{}, ErrInvalid
	}
	index, err := strconv.Atoi(id[split+1:])
	if err != nil {
		return Item{}, err
	}
	var raw []byte
	var r Round
	var done int
	err = tx.QueryRowContext(ctx, "SELECT record,(SELECT count(*) FROM work_waiting_decisions WHERE round_id=r.id) FROM work_waiting_rounds r WHERE id=?", id[:split]).Scan(&raw, &done)
	if err != nil {
		return Item{}, err
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return Item{}, err
	}
	if index < 0 || index >= len(r.Questions) {
		return Item{}, sql.ErrNoRows
	}
	q := r.Questions[index]
	v := view.Waiting{Kind: view.Decide, Sentence: q.Prompt, Recommendation: q.Recommendation, Answers: q.Choices, Effort: view.FiveMinutes, Due: r.Deadline}
	if s.AgentName != nil {
		v.AgentName = s.AgentName(r.Agent)
	}
	return Item{Person: r.Person, Agent: r.Agent, Revision: 1, View: v, Reason: q.Reason, Blocking: true, Irreversible: q.Irreversible, Finished: done > 0}, nil
}
func (RoundSource) Done(context.Context, *canon.Tx, string) error { return nil }

// ExchangeSource uses the explicit owner decision flag, never timeout or model
// prose. The exchange's live outcome wins over a stale waiting candidate.
type ExchangeSource struct{ AgentName func(string) string }

func (ExchangeSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT exchange_id FROM exch_waiting WHERE person_id=? ORDER BY exchange_id", person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s ExchangeSource) Read(ctx context.Context, tx *canon.Tx, id string) (Item, error) {
	var item Item
	var question, outcome string
	var needs bool
	err := tx.QueryRowContext(ctx, "SELECT w.person_id,e.to_agent,w.question,e.outcome,e.needs_person,e.round FROM exch_waiting w JOIN exchanges e ON e.id=w.exchange_id WHERE e.id=?", id).Scan(&item.Person, &item.Agent, &question, &outcome, &needs, &item.Revision)
	if err != nil {
		return item, err
	}
	item.View = view.Waiting{Kind: view.Reply, Sentence: question, FreeText: true, Effort: view.FiveMinutes}
	if s.AgentName != nil {
		item.View.AgentName = s.AgentName(item.Agent)
	}
	item.Reason = SignificantChoice
	item.Blocking = true
	item.Finished = outcome != "pending" || !needs
	return item, nil
}
func (ExchangeSource) Done(ctx context.Context, tx *canon.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM exch_waiting WHERE exchange_id=?", id)
	return err
}

// BlockedWorkSource admits explicit input-needed receipts, not retry, check or
// quota states. Question-round holds already have their own actionable rows.
type BlockedWorkSource struct{ AgentName func(string) string }

func (BlockedWorkSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT task_id FROM work_cards WHERE json_extract(record,'$.task.person_id')=? AND json_extract(record,'$.state')='input_needed' ORDER BY task_id`, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s BlockedWorkSource) Read(ctx context.Context, tx *canon.Tx, id string) (Item, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", id).Scan(&raw); err != nil {
		return Item{}, err
	}
	// Decode the work owner's schema rather than interpreting worker prose.
	var card work.Card
	if err := json.Unmarshal(raw, &card); err != nil {
		return Item{}, err
	}
	finished := card.State != "input_needed"
	for _, hold := range card.Waits {
		if hold.Held {
			finished = true
		}
	}
	v := view.Waiting{Kind: view.Reply, Sentence: card.Task.Title, FreeText: true, Effort: view.Longer}
	if s.AgentName != nil {
		v.AgentName = s.AgentName(card.Task.AgentID)
	}
	return Item{Person: card.Task.PersonID, Agent: card.Task.AgentID, Revision: card.Revision, View: v, Reason: MissingInformation, Blocking: true, Finished: finished}, nil
}
func (BlockedWorkSource) Done(context.Context, *canon.Tx, string) error { return nil }
