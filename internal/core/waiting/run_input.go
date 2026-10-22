// SPDX-License-Identifier: Apache-2.0
package waiting

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting/view"
	"strings"
	"time"
)

// RunInputOwner is implemented by runs.Service. The composition root supplies
// it without making either L4 flow import the other.
type RunInputOwner interface {
	AnswerInput(context.Context, string, string, uint64, string) error
}
type RunInputSource struct {
	DB        Canon
	Owner     RunInputOwner
	AgentName func(string) string
}

// This read projection contains only fields needed by the waiting producer;
// the run owner alone writes the run and validates its current generation.
type runInputRecord struct {
	ID, State       string
	Origin          struct{ Person, Agent, Conversation string }
	Brief           struct{ Goal string }
	Created         int64
	EffectsQuestion *struct{ Answer *json.RawMessage }
	NoticeVersion   uint64
	NoticeAt        int64
	InputAnswers    []struct {
		Revision              uint64
		Person, Agent, Answer string
		At                    time.Time
	}
}

func readRunInput(ctx context.Context, tx *canon.Tx, id string) (runInputRecord, error) {
	var data []byte
	var record runInputRecord
	err := tx.QueryRowContext(ctx, "SELECT record FROM background_runs WHERE id=?", id).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &record)
	}
	return record, err
}
func (s *RunInputSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM background_runs WHERE json_extract(record,'$.Origin.Person')=? AND json_extract(record,'$.State')='needs_input' ORDER BY seq", person)
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
func (s *RunInputSource) Read(ctx context.Context, tx *canon.Tx, id string) (Item, error) {
	r, err := readRunInput(ctx, tx, id)
	if err != nil {
		return Item{}, err
	}
	v := view.Waiting{Kind: view.Reply, Sentence: r.Brief.Goal, FreeText: true, Effort: view.FiveMinutes}
	if s.AgentName != nil {
		v.AgentName = s.AgentName(r.Origin.Agent)
	}
	return Item{Person: r.Origin.Person, Agent: r.Origin.Agent, Revision: r.NoticeVersion, View: v, Reason: MissingInformation, Blocking: true, Finished: r.State != "needs_input", Requested: time.Unix(0, r.NoticeAt)}, nil
}
func (s *RunInputSource) Done(context.Context, *canon.Tx, string) error { return nil }
func (s *RunInputSource) AnswerRecorded(ctx context.Context, person string, ref Ref, value string) (Decision, error) {
	if s.DB == nil || s.Owner == nil || strings.TrimSpace(value) == "" {
		return Decision{}, ErrInvalid
	}
	// The run owner rechecks owner, state and generation under its own lock and
	// records the decision atomically with the follow-up. A scheduling error
	// after commit must not turn that durable answer into a second attempt.
	answerErr := s.Owner.AnswerInput(ctx, person, ref.ID, ref.Revision, value)
	var decision Decision
	err := s.DB.Read(ctx, func(tx *canon.Tx) error {
		r, err := readRunInput(ctx, tx, ref.ID)
		if err != nil {
			return err
		}
		if r.Origin.Person != person {
			return ErrPerson
		}
		for _, a := range r.InputAnswers {
			if a.Revision == ref.Revision && a.Person == person && (answerErr == nil || a.Answer == value) {
				decision = Decision{Ref: ref, Person: a.Person, Agent: a.Agent, Answer: a.Answer, At: a.At}
				return nil
			}
		}
		if answerErr != nil {
			return answerErr
		}
		return ErrStale
	})
	return decision, err
}
func (s *RunInputSource) RecordedDecisions(ctx context.Context, tx *canon.Tx, person string) ([]Decision, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM background_runs WHERE json_extract(record,'$.Origin.Person')=? ORDER BY seq", person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var decisions []Decision
	for rows.Next() {
		var data []byte
		var r runInputRecord
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		for _, a := range r.InputAnswers {
			if a.Person == person {
				decisions = append(decisions, Decision{Ref: Ref{ID: r.ID, Revision: a.Revision}, Person: a.Person, Agent: a.Agent, Answer: a.Answer, At: a.At})
			}
		}
	}
	return decisions, rows.Err()
}
