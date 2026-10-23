// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

type CandidateInput struct {
	Source         string  `json:"source"`
	SourceRevision string  `json:"source_revision"`
	Class          string  `json:"class"`
	Title          string  `json:"title"`
	Note           string  `json:"note"`
	Confidence     float64 `json:"confidence"`
}

// Facts are supplied by Core's source resolver, never a model argument.
type CandidateFacts struct{ AddressedToPerson, FromPerson, RequestOrPromise bool }
type Candidate struct {
	CandidateInput
	Number   int64  `json:"number"`
	Status   string `json:"status"`
	RecordID string `json:"record_id,omitempty"`
	Undo     string `json:"undo,omitempty"`
	Notice   string `json:"notice,omitempty"`
}

func equivalentTitle(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

func (s *Store) ProposeCandidate(ctx context.Context, a Actor, input CandidateInput, facts CandidateFacts) (Candidate, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	if a.Person == "" || a.Agent == "" || input.Source == "" || len(input.Source) > 512 || len(input.SourceRevision) > 512 || input.Class == "" || len(input.Class) > 64 || strings.TrimSpace(input.Title) == "" || len(input.Title) > 512 || strings.ContainsAny(input.Title, "\r\n") || len(input.Note) > 1<<20 || math.IsNaN(input.Confidence) || input.Confidence < 0 || input.Confidence > 1 {
		return Candidate{}, ErrInvalid
	}
	if _, _, err := s.snapshot(ctx); err != nil {
		return Candidate{}, err
	}
	encoded, _ := json.Marshal(input)
	key := equivalentTitle(input.Title)
	var number int64
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO record_candidates(person,source,source_revision,class,equivalence,proposal) VALUES(?,?,?,?,?,?) ON CONFLICT(person,source,equivalence) DO NOTHING", a.Person, input.Source, input.SourceRevision, input.Class, key, encoded)
		if err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, "SELECT number FROM record_candidates WHERE person=? AND source=? AND equivalence=?", a.Person, input.Source, key).Scan(&number)
	})
	if err != nil {
		return Candidate{}, err
	}
	c, err := s.candidate(ctx, a.Person, number)
	if err != nil || c.Status != "pending" {
		return c, err
	}
	// A retry cannot replace the source snapshot, class or wording that was
	// admitted originally, and confidence never substitutes for source facts.
	if c.CandidateInput != input || input.SourceRevision == "" || !facts.AddressedToPerson || !facts.FromPerson || !facts.RequestOrPromise {
		return c, nil
	}
	duplicate := false
	rejected := 0
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM record_candidates WHERE person=? AND class=? AND status='rejected'", a.Person, input.Class).Scan(&rejected); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT title FROM records_text WHERE person=? AND kind='todo' AND state NOT IN ('completed','canceled')", a.Person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var title string
			if err = rows.Scan(&title); err != nil {
				return err
			}
			if equivalentTitle(title) == key {
				duplicate = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return c, err
	}
	if duplicate || rejected >= 3 {
		return c, nil
	}
	id := newID()
	front := fmt.Sprintf("---\nid: %q\nkind: todo\nperson_id: %q\nagent_id: %q", id, a.Person, a.Agent)
	front, err = WithTodo(front, Todo{State: "open", Source: input.Source})
	if err != nil {
		return c, err
	}
	data := []byte(front + "\n---\n\n# " + input.Title + "\n\n" + input.Note + "\n")
	op := &recordOperation{Handle: newID(), Person: a.Person, Agent: a.Agent, Candidate: number, Notice: "To-do added. Undo: "}
	op.Notice += op.Handle
	_, err = s.commitLocked(ctx, Change{ID: id, Path: "inbox/candidate-" + strconv.FormatInt(number, 10) + ".md", Data: data}, a, op)
	if err != nil {
		return c, err
	}
	return s.candidate(ctx, a.Person, number)
}

func (s *Store) candidate(ctx context.Context, person string, number int64) (Candidate, error) {
	var c Candidate
	var raw []byte
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT c.number,c.proposal,c.status,c.record_id,c.undo_handle,coalesce(o.notice,'') FROM record_candidates c LEFT JOIN record_operations o ON o.handle=c.undo_handle WHERE c.person=? AND c.number=?`, person, number).Scan(&c.Number, &raw, &c.Status, &c.RecordID, &c.Undo, &c.Notice)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(raw, &c.CandidateInput)
	}
	return c, err
}

// Candidates retains unanswered proposals and provides a numbered, explicit
// page. It never interprets a vague "all" as permission to adopt unseen rows.
func (s *Store) Candidates(ctx context.Context, person string, after int64) ([]Candidate, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	if person == "" || after < 0 {
		return nil, ErrInvalid
	}
	if _, _, err := s.snapshot(ctx); err != nil {
		return nil, err
	}
	var numbers []int64
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT number FROM record_candidates WHERE person=? AND number>? ORDER BY number LIMIT 100", person, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n int64
			if err = rows.Scan(&n); err != nil {
				return err
			}
			numbers = append(numbers, n)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := []Candidate{}
	for _, n := range numbers {
		c, err := s.candidate(ctx, person, n)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
