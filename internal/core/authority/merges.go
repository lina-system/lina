// SPDX-License-Identifier: Apache-2.0
package authority

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var ErrProtectedRef = errors.New("protected_ref_changed")
var ErrMergeState = errors.New("merge_state_conflict")

// MergeBinding is authority bookkeeping, not a work-ledger receipt or verdict.
// Protected refs are captured by the trusted host at assignment, never supplied
// as expected hashes by a worker or by a merge request.
type MergeBinding struct {
	ID, Person, Agent, Repository, Base, Clone, Head, State string
	Number                                                  int
	Protected                                               map[string]string
	Result                                                  json.RawMessage
}
type Merges struct {
	store *canon.Store
	epoch uint64
}

func NewMerges(store *canon.Store, epoch uint64) *Merges { return &Merges{store, epoch} }
func (m *Merges) write(ctx context.Context, f func(*canon.Tx) error) error {
	return m.store.Write(ctx, func(tx *canon.Tx) error {
		var epoch string
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM canon_meta WHERE singleton=1").Scan(&epoch); err != nil {
			return err
		}
		if epoch != fmt.Sprint(m.epoch) {
			return ErrMergeState
		}
		return f(tx)
	})
}
func readMerge(ctx context.Context, tx *canon.Tx, id, person string) (MergeBinding, error) {
	var raw []byte
	var b MergeBinding
	err := tx.QueryRowContext(ctx, "SELECT record FROM authority_merges WHERE id=? AND person=?", id, person).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &b)
	}
	return b, err
}
func saveMerge(ctx context.Context, tx *canon.Tx, b MergeBinding) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE authority_merges SET state=?,record=? WHERE id=? AND person=?", b.State, raw, b.ID, b.Person)
	return err
}
func (m *Merges) Get(ctx context.Context, id, person string) (b MergeBinding, err error) {
	err = m.store.Read(ctx, func(tx *canon.Tx) error { b, err = readMerge(ctx, tx, id, person); return err })
	return
}
func (m *Merges) Bind(ctx context.Context, b MergeBinding) error {
	if b.ID == "" || b.Person == "" || b.Agent == "" || b.Repository == "" || b.Number <= 0 || b.Base == "" || b.Clone == "" || len(b.Protected) == 0 || b.Protected["refs/heads/"+b.Base] == "" {
		return ErrMergeState
	}
	b.State = "bound"
	b.Head = ""
	b.Result = nil
	return m.write(ctx, func(tx *canon.Tx) error {
		old, err := readMerge(ctx, tx, b.ID, b.Person)
		if err == nil {
			if old.Repository != b.Repository || old.Number != b.Number || old.Agent != b.Agent || old.Base != b.Base || old.Clone != b.Clone || !maps.Equal(old.Protected, b.Protected) {
				return ErrMergeState
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO authority_merges(id,person,repository,base,state,record) VALUES(?,?,?,?,?,?)", b.ID, b.Person, b.Repository, b.Base, b.State, raw)
		return err
	})
}
func (m *Merges) Accept(ctx context.Context, id, person, head string) error {
	if head == "" {
		return ErrMergeState
	}
	return m.write(ctx, func(tx *canon.Tx) error {
		b, err := readMerge(ctx, tx, id, person)
		if err != nil {
			return err
		}
		if b.State != "bound" && b.State != "ready" && b.State != "blocked" {
			return ErrMergeState
		}
		b.Head = head
		b.State = "ready"
		b.Result = nil
		return saveMerge(ctx, tx, b)
	})
}

// Begin reserves the shared base durably before the external call. A crash at
// any later point leaves unknown; opening Core never clears or replays it.
func (m *Merges) Begin(ctx context.Context, id, person, head string, current map[string]string) error {
	return m.write(ctx, func(tx *canon.Tx) error {
		b, err := readMerge(ctx, tx, id, person)
		if err != nil {
			return err
		}
		if b.State != "ready" || b.Head == "" || b.Head != head {
			return ErrMergeState
		}
		if !maps.Equal(b.Protected, current) {
			return ErrProtectedRef
		}
		b.State = "unknown"
		return saveMerge(ctx, tx, b)
	})
}

// Record stores only evidence returned by the trusted GitHub adapter. It never
// promotes a client's boolean or a worker receipt to a verified merge.
func (m *Merges) Record(ctx context.Context, id, person, state string, result json.RawMessage) error {
	if state != "merged" && state != "unknown" && state != "queued" && state != "blocked" {
		return ErrMergeState
	}
	return m.write(ctx, func(tx *canon.Tx) error {
		b, err := readMerge(ctx, tx, id, person)
		if err != nil {
			return err
		}
		if b.State == "merged" {
			return nil
		}
		// Observation failures cannot revive permission invalidated by restore.
		if b.State == "restored" && state != "merged" {
			state = b.State
		}
		if (b.State == "unknown" || b.State == "queued") && state == "blocked" {
			state = b.State
		}
		b.State = state
		b.Result = append(json.RawMessage{}, result...)
		return saveMerge(ctx, tx, b)
	})
}
