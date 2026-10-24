// SPDX-License-Identifier: Apache-2.0
package waiting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

var ErrQuestionLimit = errors.New("question_limit")

type QuestionDraft struct {
	YesNo          bool     `json:"yes_no,omitempty"`
	Question       string   `json:"question"`
	Recommendation string   `json:"recommendation"`
	Choices        []string `json:"choices,omitempty"`
	Default        string   `json:"default"`
}
type QuestionResolution struct {
	Kind  string    `json:"kind"`
	Input string    `json:"input"`
	Value string    `json:"value"`
	At    time.Time `json:"at"`
}
type ConversationQuestion struct {
	Binding      *binding.Choice `json:"binding,omitempty"`
	ID           string          `json:"id"`
	Person       string          `json:"person"`
	Agent        string          `json:"agent"`
	Conversation string          `json:"conversation"`
	Turn         string          `json:"turn"`
	Input        string          `json:"input"`
	AskedAt      time.Time       `json:"asked_at"`
	QuestionDraft
	Resolution *QuestionResolution `json:"resolution,omitempty"`
}
type ConversationQuestions struct{ db Canon }

func NewConversationQuestions(db Canon) *ConversationQuestions { return &ConversationQuestions{db} }

func (q QuestionDraft) Valid() bool {
	// Five is the question card limit. Target ties use the same cap.
	if len(q.Choices) > 5 {
		return false
	}
	for _, v := range append([]string{q.Question, q.Recommendation, q.Default}, q.Choices...) {
		if strings.TrimSpace(v) == "" || len(v) > 4096 || !utf8.ValidString(v) {
			return false
		}
	}
	seen := map[string]bool{}
	for _, choice := range q.Choices {
		if seen[choice] {
			return false
		}
		seen[choice] = true
	}
	return len(q.Choices) == 0 || len(q.Choices) >= 2 && slices.Contains(q.Choices, q.Default)
}
func readConversationQuestion(ctx context.Context, tx *canon.Tx, id string) (q ConversationQuestion, err error) {
	var raw, resolution []byte
	err = tx.QueryRowContext(ctx, `SELECT q.record,r.record FROM conversation_questions q LEFT JOIN conversation_question_resolutions r ON r.question=q.id WHERE q.id=?`, id).Scan(&raw, &resolution)
	if err != nil {
		return
	}
	if err = json.Unmarshal(raw, &q); err != nil {
		return
	}
	if len(resolution) > 0 {
		err = json.Unmarshal(resolution, &q.Resolution)
	}
	return
}
func (s *ConversationQuestions) Get(ctx context.Context, id string) (q ConversationQuestion, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error { q, err = readConversationQuestion(ctx, tx, id); return err })
	return
}

// The prepared limit and immutable call identity come from Core, never model arguments.
// Count every admitted question, including closed ones; a retry consumes no slot.
func (s *ConversationQuestions) Ask(ctx context.Context, q ConversationQuestion, limit int) error {
	if !q.QuestionDraft.Valid() || q.ID == "" || q.Person == "" || q.Agent == "" || q.Conversation == "" || q.Turn == "" || q.Input == "" || q.AskedAt.IsZero() || q.Resolution != nil || limit < 1 {
		return ErrInvalid
	}
	// Preserve the stated recommendation as masked data, never as authority.
	raw, err := json.Marshal(q.QuestionDraft)
	if err != nil {
		return err
	}
	masked, err := secrets.Mask(string(raw))
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(masked.Text()), &q.QuestionDraft); err != nil {
		return err
	}
	if !q.QuestionDraft.Valid() {
		return ErrInvalid
	}
	raw, err = json.Marshal(q)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		old, err := readConversationQuestion(ctx, tx, q.ID)
		if err == nil {
			// The original admission time, unlike the payload, is not a retry input.
			old.AskedAt = q.AskedAt
			old.Resolution = nil
			if reflect.DeepEqual(old, q) {
				return nil
			}
			return ErrStale
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM conversation_questions WHERE person=? AND agent=? AND turn=?`, q.Person, q.Agent, q.Turn).Scan(&count); err != nil {
			return err
		}
		if count >= limit {
			return ErrQuestionLimit
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO conversation_questions(id,person,agent,turn,record) VALUES(?,?,?,?,?)`, q.ID, q.Person, q.Agent, q.Turn, raw)
		return err
	})
}
func (s *ConversationQuestions) Pending(ctx context.Context, person, agent string) (questions []ConversationQuestion, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		ids, err := roundIDs(ctx, tx, `SELECT q.id FROM conversation_questions q WHERE q.person=? AND (?='' OR q.agent=?) AND NOT EXISTS(SELECT 1 FROM conversation_question_resolutions r WHERE r.question=q.id) ORDER BY q.rowid`, person, agent, agent)
		if err != nil {
			return err
		}
		for _, id := range ids {
			q, err := readConversationQuestion(ctx, tx, id)
			if err != nil {
				return err
			}
			questions = append(questions, q)
		}
		return nil
	})
	return
}

// Resolve only records a conversational decision. It never runs the default or
// grants permission for an effect; the owning conversation continues that work.
func (s *ConversationQuestions) Resolve(ctx context.Context, person, agent, id string, d QuestionResolution) error {
	return s.ResolveWith(ctx, person, agent, id, d, nil)
}

// ResolveWith commits the conversation notice with the decision. The hook may
// only write through this transaction; it cannot publish or perform external IO.
func (s *ConversationQuestions) ResolveWith(ctx context.Context, person, agent, id string, d QuestionResolution, notice func(context.Context, *canon.Tx, ConversationQuestion, QuestionResolution) error) error {
	if d.Input == "" || d.At.IsZero() || len(d.Value) > 4096 || !utf8.ValidString(d.Value) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		q, err := readConversationQuestion(ctx, tx, id)
		if err != nil {
			return err
		}
		if q.Person != person || q.Agent != agent {
			return ErrPerson
		}
		switch d.Kind {
		case "default_applied":
			if q.Binding != nil {
				return ErrInvalid
			}
			if d.Value != q.Default || d.Input == q.Input {
				return ErrInvalid
			}
		case "answered":
			if strings.TrimSpace(d.Value) == "" || len(q.Choices) > 0 && !slices.Contains(q.Choices, d.Value) {
				return ErrInvalid
			}
		case "declined", "cancelled":
			if d.Value != "" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		if q.Resolution != nil {
			old := *q.Resolution
			old.At = d.At
			if old == d {
				return nil
			}
			return ErrAnswered
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return err
		}
		masked, err := secrets.Mask(string(raw))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO conversation_question_resolutions(question,record) VALUES(?,?)`, id, []byte(masked.Text()))
		if err == nil && notice != nil {
			err = notice(ctx, tx, q, d)
		}
		return err
	})
}
