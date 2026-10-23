// SPDX-License-Identifier: Apache-2.0
package review

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var ErrDuplicate = errors.New("review_already_requested")
var ErrState = errors.New("invalid_review_state")
var repositoryPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*/[a-z0-9_.-]+$`)
var commitPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

type Key struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Head       string `json:"head"`
}
type Record struct {
	Key
	Task       string    `json:"task"`
	Receipt    string    `json:"receipt"`
	Generation int       `json:"generation"`
	State      string    `json:"state"`
	Cause      string    `json:"cause,omitempty"`
	Verdict    string    `json:"verdict,omitempty"`
	Evidence   string    `json:"evidence,omitempty"`
	Findings   []Finding `json:"findings,omitempty"`
}
type Store struct{ db *canon.Store }

func New(db *canon.Store) *Store { return &Store{db: db} }
func normalized(k Key) (Key, error) {
	k.Repository = strings.ToLower(k.Repository)
	if !repositoryPattern.MatchString(k.Repository) || k.Number <= 0 || !commitPattern.MatchString(k.Head) {
		return k, ErrState
	}
	return k, nil
}

// Claim commits before any manager call. An interrupted claim is never replayed.
func (s *Store) Claim(ctx context.Context, r Record) error {
	var err error
	r.Key, err = normalized(r.Key)
	if err != nil {
		return err
	}
	if r.Task == "" || r.Receipt == "" || r.Generation < 1 {
		return ErrState
	}
	r.State = "checking"
	r.Cause = ""
	r.Verdict = ""
	r.Evidence = ""
	r.Findings = nil
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := GetIn(ctx, tx, r.Key)
		if err == nil {
			return ErrDuplicate
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO work_reviews(repository,pr_number,head,record) VALUES(?,?,?,?)", r.Repository, r.Number, r.Head, raw)
		return err
	})
}
func GetIn(ctx context.Context, tx *canon.Tx, key Key) (Record, error) {
	k, err := normalized(key)
	if err != nil {
		return Record{}, err
	}
	var raw []byte
	var r Record
	err = tx.QueryRowContext(ctx, "SELECT record FROM work_reviews WHERE repository=? AND pr_number=? AND head=?", k.Repository, k.Number, k.Head).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	return r, err
}
func (s *Store) Get(ctx context.Context, k Key) (r Record, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error { r, err = GetIn(ctx, tx, k); return err })
	return
}

// FinishIn shares the task verdict/assignment transaction. It cannot turn a
// claimed head into a different receipt or overwrite an existing result.
func FinishIn(ctx context.Context, tx *canon.Tx, r Record) error {
	prior, err := GetIn(ctx, tx, r.Key)
	if err != nil {
		return err
	}
	if prior.State != "checking" || prior.Task != r.Task || prior.Receipt != r.Receipt || prior.Generation != r.Generation {
		return ErrState
	}
	switch r.State {
	case "complete":
		if (r.Verdict != "verified" && r.Verdict != "needs_changes") || strings.TrimSpace(r.Evidence) == "" {
			return ErrState
		}
	case "blocked", "unverified":
		if r.Verdict != "" || len(r.Findings) != 0 || r.Cause == "" {
			return ErrState
		}
	default:
		return ErrState
	}
	if len(r.Findings) > 0 {
		raw, _ := json.Marshal(map[string]any{"findings": r.Findings})
		if _, err := Parse(raw); err != nil {
			return err
		}
		for _, f := range r.Findings {
			if r.Verdict == "verified" && (f.Severity == "P0" || f.Severity == "P1") {
				return ErrState
			}
		}
	}
	r.Key = prior.Key
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE work_reviews SET record=? WHERE repository=? AND pr_number=? AND head=?", raw, r.Repository, r.Number, r.Head)
	return err
}
