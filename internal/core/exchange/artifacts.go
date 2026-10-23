// SPDX-License-Identifier: Apache-2.0
package exchange

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

type Change struct {
	Person   string   `json:"-"`
	Artifact Artifact `json:"artifact"`
	Owner    string   `json:"owner"`
	WorkID   string   `json:"work_id"`
	Done     string   `json:"done"`
}
type ChangeResult struct {
	Change  *Change  `json:"change,omitempty"`
	Handoff *Receipt `json:"handoff,omitempty"`
}
type transactionCanon struct{ tx *canon.Tx }

func (t transactionCanon) Read(_ context.Context, f func(*canon.Tx) error) error  { return f(t.tx) }
func (t transactionCanon) Write(_ context.Context, f func(*canon.Tx) error) error { return f(t.tx) }

func change(ctx context.Context, tx *canon.Tx, person, id string) (Change, error) {
	var c Change
	c.Person = person
	c.Artifact.ID = id
	err := tx.QueryRowContext(ctx, `SELECT kind,owner,work_id,done FROM artifact_changes WHERE person=? AND artifact=?`, person, id).Scan(&c.Artifact.Kind, &c.Owner, &c.WorkID, &c.Done)
	return c, err
}
func (s *Store) Change(ctx context.Context, person, id string) (Change, error) {
	var c Change
	err := s.db.Read(ctx, func(tx *canon.Tx) error { var err error; c, err = change(ctx, tx, person, id); return err })
	return c, err
}

// BeginChange atomically chooses the existing open owner or the trusted preset
// owner. A conflicting agent gets a queued exchange and cannot get a lease.
// Requests are bounded inline so no filesystem work runs under the canon lock.
func (s *Store) BeginChange(ctx context.Context, c Change, request Request, at time.Time) (ChangeResult, error) {
	if !identifier(c.Person) || !identifier(c.Artifact.Kind) || !identifier(c.Artifact.ID) || !identifier(c.Owner) || !identifier(c.WorkID) || !present(c.Done) || request.Origin == nil || request.Origin.Person != c.Person {
		return ChangeResult{}, ErrInvalid
	}
	if len(encode(request.HandOff)) > ledger.InlineBytes {
		return ChangeResult{}, ErrInvalid
	}
	var err error
	c.Done, err = clean(c.Done)
	if err != nil {
		return ChangeResult{}, ErrInvalid
	}
	var out ChangeResult
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		current, err := change(ctx, tx, c.Person, c.Artifact.ID)
		if err == nil {
			if current.Artifact.Kind != c.Artifact.Kind {
				return ErrConflict
			}
			c = current
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if c.Owner == request.From {
			if err == nil {
				out.Change = &c
				return nil
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_changes(person,kind,artifact,owner,work_id,done) VALUES(?,?,?,?,?,?)`, c.Person, c.Artifact.Kind, c.Artifact.ID, c.Owner, c.WorkID, c.Done); err != nil {
				return err
			}
			out.Change = &c
			return nil
		}
		request.To = c.Owner
		origin := *request.Origin
		origin.Artifact = c.Artifact
		request.Origin = &origin
		receipt, err := New(transactionCanon{tx}).Start(ctx, request, at)
		if err != nil {
			return err
		}
		out.Handoff = &receipt
		return nil
	})
	if err != nil {
		return ChangeResult{}, err
	}
	return out, nil
}
func (s *Store) EndChange(ctx context.Context, person, agent, workID string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		var owner string
		err := tx.QueryRowContext(ctx, `SELECT owner FROM artifact_changes WHERE person=? AND work_id=?`, person, workID).Scan(&owner)
		if err != nil {
			return err
		}
		if owner != agent {
			return ErrInvalid
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM artifact_changes WHERE person=? AND work_id=?`, person, workID)
		return err
	})
}
