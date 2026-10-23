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
	Source     string  `json:"source"`
	Quotation  string  `json:"quotation"`
	Title      string  `json:"title"`
	Note       string  `json:"note"`
	Confidence float64 `json:"confidence"`
}

type Candidate struct {
	CandidateInput
	Class          string `json:"class"`
	SourceRevision string `json:"source_revision,omitempty"`
	Number         int64  `json:"number"`
	Status         string `json:"status"`
	RecordID       string `json:"record_id,omitempty"`
	Undo           string `json:"undo,omitempty"`
	Notice         string `json:"notice,omitempty"`
}

func equivalentTitle(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

func (s *Store) ProposeCandidate(ctx context.Context, a Actor, input CandidateInput) (Candidate, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	if a.Person == "" || a.Agent == "" || input.Source == "" || len(input.Source) > 512 || len(input.Quotation) > 1<<20 || strings.TrimSpace(input.Title) == "" || len(input.Title) > 512 || strings.ContainsAny(input.Title, "\r\n") || len(input.Note) > 1<<20 || math.IsNaN(input.Confidence) || input.Confidence < 0 || input.Confidence > 1 {
		return Candidate{}, ErrInvalid
	}
	if _, _, err := s.snapshot(ctx); err != nil {
		return Candidate{}, err
	}
	bodies := s.candidateBodies(ctx, a, input.Source)
	encoded, _ := json.Marshal(input)
	key := equivalentTitle(input.Title)
	var number int64
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO record_candidates(person,source,source_revision,class,equivalence,proposal) VALUES(?,?,?,?,?,?) ON CONFLICT(person,source,equivalence) DO NOTHING", a.Person, input.Source, "", "unknown", key, encoded)
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
	// A retry cannot replace the original proposal. Admission facts are never
	// caller arguments: resolve them while committing the publication intent.
	if c.CandidateInput != input {
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
	op.Validate = func(ctx context.Context, tx *canon.Tx) error {
		class, revision, err := verifyCandidateSource(ctx, tx, a, input, bodies)
		if err != nil {
			return err
		}
		var rejected int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM record_candidates WHERE person=? AND class=? AND status='rejected'", a.Person, class).Scan(&rejected); err != nil {
			return err
		}
		if rejected >= 3 {
			return errCandidatePending
		}
		// Adoption survives completion or cancellation of the record. Resolve
		// aliases to the source hash so replay cannot create the same work again.
		var adopted int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM record_candidates WHERE person=? AND source_revision=? AND equivalence=? AND status IN ('added','rejected')", a.Person, revision, key).Scan(&adopted); err != nil {
			return err
		}
		if adopted > 0 {
			return errCandidatePending
		}
		rows, err := tx.QueryContext(ctx, "SELECT title,coalesce(json_extract(v.fields,'$.source'),'') FROM records_text t LEFT JOIN records_todo v ON v.id=t.id WHERE t.person=? AND t.kind='todo' AND t.state NOT IN ('completed','canceled')", a.Person)
		if err != nil {
			return err
		}
		duplicate := false
		for rows.Next() {
			var title, source string
			if err = rows.Scan(&title, &source); err != nil {
				rows.Close()
				return err
			}
			if equivalentTitle(title) == key || source == input.Source {
				duplicate = true
			}
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		if duplicate {
			return errCandidatePending
		}
		_, err = tx.ExecContext(ctx, "UPDATE record_candidates SET class=?,source_revision=? WHERE number=? AND person=? AND status='pending'", class, revision, number, a.Person)
		return err
	}
	_, err = s.commitLocked(ctx, Change{ID: id, Path: "inbox/candidate-" + strconv.FormatInt(number, 10) + ".md", Data: data}, a, op)
	if errors.Is(err, errCandidatePending) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return s.candidate(ctx, a.Person, number)
}

func (s *Store) candidate(ctx context.Context, person string, number int64) (Candidate, error) {
	var c Candidate
	var raw []byte
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT c.number,c.proposal,c.status,c.record_id,c.undo_handle,coalesce(o.notice,''),c.class,c.source_revision FROM record_candidates c LEFT JOIN record_operations o ON o.handle=c.undo_handle WHERE c.person=? AND c.number=?`, person, number).Scan(&c.Number, &raw, &c.Status, &c.RecordID, &c.Undo, &c.Notice, &c.Class, &c.SourceRevision)
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
