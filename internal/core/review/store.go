// SPDX-License-Identifier: Apache-2.0
package review

import (
	"context"
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
	ReturnOf   string    `json:"return_of,omitempty"`
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
// One review is requested per pull request. The only second request accepted is
// the head produced by that review's single work-definition return: it carries
// ReturnOf, the returned receipt, and the next generation of the same task.
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
		prior, err := reviewsFor(ctx, tx, r.Key)
		if err != nil {
			return err
		}
		for _, p := range prior {
			if p.Head == r.Head {
				return ErrDuplicate
			}
		}
		if len(prior) > 0 && !continuationOf(prior, r) {
			return ErrDuplicate
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO work_reviews(repository,pr_number,head,record) VALUES(?,?,?,?)", r.Repository, r.Number, r.Head, raw)
		return err
	})
}

// reviewsFor returns every review already claimed for the pull request. The
// primary key's leading columns cover this read.
func reviewsFor(ctx context.Context, tx *canon.Tx, k Key) ([]Record, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM work_reviews WHERE repository=? AND pr_number=?", k.Repository, k.Number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// continuationOf accepts exactly the one review of the head the return
// produced: the same task's next generation, naming the returned receipt, after
// a needs_changes verdict. A third request finds no such allowance.
func continuationOf(prior []Record, r Record) bool {
	if len(prior) != 1 || r.ReturnOf == "" || r.ReturnOf == r.Receipt {
		return false
	}
	p := prior[0]
	return p.Receipt == r.ReturnOf && p.Task == r.Task && p.Verdict == "needs_changes" && r.Generation == p.Generation+1
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
	if prior.State != "checking" || prior.Task != r.Task || prior.Receipt != r.Receipt || prior.ReturnOf != r.ReturnOf || prior.Generation != r.Generation {
		return ErrState
	}
	switch r.State {
	case "complete":
		if (r.Verdict != "verified" && r.Verdict != "needs_changes") || strings.TrimSpace(r.Evidence) == "" {
			return ErrState
		}
	case "blocked":
		// A blocked review runs no model call, so its findings are Core's own
		// evidence and carry no verdict. Unverified keeps none.
		if r.Verdict != "" || r.Cause == "" {
			return ErrState
		}
	case "unverified":
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
