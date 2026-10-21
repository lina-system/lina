// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

const TrashRetention = 30 * 24 * time.Hour

type Config struct {
	Canon *canon.Store
	Root  string
	Now   func() time.Time
}

type Ref struct {
	Kind Kind
	ID   string
}
type Request struct {
	Person  string
	Targets []Ref
}

type projection struct {
	resolve func(context.Context, *canon.Tx, Ref, string) (Target, error)
	apply   func(context.Context, *canon.Tx, entry, Target) error
}

type Store struct {
	mu         sync.Mutex
	db         *canon.Store
	root       string
	journal    *journal
	projection projection
	now        func() time.Time
	closed     bool
	pending    bool
	fault      func(string) error // deterministic crash boundaries in package tests
}

func openStore(ctx context.Context, cfg Config, p projection) (*Store, error) {
	if cfg.Canon == nil || cfg.Root == "" || p.resolve == nil || p.apply == nil {
		return nil, ErrInvalid
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Store{db: cfg.Canon, root: cfg.Root, projection: p, now: cfg.Now}
	if err := s.Recover(ctx); err != nil {
		if s.journal != nil {
			s.journal.Close()
		}
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.journal != nil {
		return s.journal.Close()
	}
	return nil
}

// Recover replays the complete removal list before reopening canon reads. The
// same entry is applied at most once; a lost/changed acknowledged line fails
// closed. It also applies removals absent from an older restored generation.
func (s *Store) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return canon.ErrClosed
	}
	err := s.db.Reconcile(func(write canon.RecoveryWrite) error {
		if s.journal != nil {
			old := s.journal
			s.journal = nil
			if err := old.Close(); err != nil {
				return err
			}
		}
		j, err := openJournal(s.root)
		if err != nil {
			return err
		}
		s.journal = j
		entries, err := j.read()
		if err != nil {
			return err
		}
		return write(ctx, func(tx *canon.Tx) error {
			rows, err := tx.QueryContext(ctx, "SELECT sequence,id,digest FROM removal_applied ORDER BY sequence")
			if err != nil {
				return err
			}
			type applied struct {
				sequence   int
				id, digest string
			}
			var existing []applied
			for rows.Next() {
				var a applied
				if err := rows.Scan(&a.sequence, &a.id, &a.digest); err != nil {
					rows.Close()
					return err
				}
				existing = append(existing, a)
			}
			err = errors.Join(rows.Err(), rows.Close())
			if err != nil {
				return err
			}
			if len(existing) > len(entries) {
				return ErrJournal
			}
			previous := ""
			for i, e := range entries {
				line := journalLine{Sequence: i + 1, Previous: previous, Entry: e}
				line.Digest = lineDigest(line)
				previous = line.Digest
				if i < len(existing) {
					a := existing[i]
					if a.sequence != line.Sequence || a.id != e.ID || a.digest != line.Digest {
						return ErrJournal
					}
					continue
				}
				if err := s.apply(ctx, tx, e, line.Sequence, line.Digest); err != nil {
					return err
				}
			}
			return nil
		})
	})
	s.pending = err != nil
	return err
}

func (s *Store) Epoch(ctx context.Context) (uint64, error) {
	var epoch uint64
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&epoch)
	})
	return epoch, err
}

func (s *Store) Forget(ctx context.Context, r Request) error  { return s.change(ctx, r, forget) }
func (s *Store) Restore(ctx context.Context, r Request) error { return s.change(ctx, r, restore) }

// Delete skips recoverable trash. Its caller must establish the person's
// explicit permanent-deletion choice before invoking it.
func (s *Store) Delete(ctx context.Context, r Request) error { return s.change(ctx, r, erase) }

func (s *Store) change(ctx context.Context, r Request, a action) error {
	return s.changeMode(ctx, r, a, false)
}

// Expire permanently deletes this person's trash at its 30-day deadline.
// Selection and deletion share the writer transaction. An empty pass writes
// neither an epoch nor a removal-list line.
func (s *Store) Expire(ctx context.Context, person string) error {
	return s.changeMode(ctx, Request{Person: person}, erase, true)
}

func (s *Store) changeMode(ctx context.Context, r Request, a action, expiry bool) error {
	if r.Person == "" || (!expiry && len(r.Targets) == 0) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return canon.ErrClosed
	}
	if s.journal == nil || s.journal.failed || s.pending {
		return ErrRecovery
	}
	var requestErr error
	err := s.db.Reconcile(func(write canon.RecoveryWrite) error {
		durable := false
		requestErr = write(ctx, func(tx *canon.Tx) error {
			// Resolve every target and validate all transitions before journaling any.
			e := entry{Version: 1, Action: a, At: s.now().UnixNano(), Person: r.Person}
			if expiry {
				rows, err := tx.QueryContext(ctx, "SELECT kind,id FROM retract_target WHERE person=? AND state='trashed' AND at<=? ORDER BY kind,id", r.Person, e.At-int64(TrashRetention))
				if err != nil {
					return err
				}
				for rows.Next() {
					var ref Ref
					if err := rows.Scan(&ref.Kind, &ref.ID); err != nil {
						rows.Close()
						return err
					}
					r.Targets = append(r.Targets, ref)
				}
				if err := errors.Join(rows.Err(), rows.Close()); err != nil {
					return err
				}
				if len(r.Targets) == 0 {
					return nil
				}
			}
			var nonce [16]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return err
			}
			e.ID = hex.EncodeToString(nonce[:])
			for _, ref := range r.Targets {
				target, err := s.projection.resolve(ctx, tx, ref, r.Person)
				if err != nil {
					return err
				}
				if err := transition(ctx, tx, target, e); err != nil {
					return err
				}
				e.Targets = append(e.Targets, target)
			}
			if !e.valid() {
				return ErrInvalid
			}
			// A failed write/sync has an uncertain disk outcome, so close reads even
			// when no complete line was acknowledged. Recovery validates the bytes.
			durable = true
			if err := s.journal.append(e); err != nil {
				return err
			}
			if err := s.fail("after_sync"); err != nil {
				return err
			}
			return s.apply(ctx, tx, e, s.journal.sequence, s.journal.digest)
		})
		if durable {
			s.pending = requestErr != nil
			return requestErr
		}
		return nil
	})
	if err != nil {
		return errors.Join(ErrRecovery, err)
	}
	return requestErr
}

func transition(ctx context.Context, tx *canon.Tx, t Target, e entry) error {
	var state, person string
	var at int64
	err := tx.QueryRowContext(ctx, "SELECT state,at,person FROM retract_target WHERE kind=? AND id=?", t.Kind, t.ID).Scan(&state, &at, &person)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if person != "" && person != e.Person {
		return ErrInvalid
	}
	if state == "erased" || state == "disconnected" {
		return ErrInvalid
	}
	switch e.Action {
	case restore:
		if state != "trashed" || e.At < at || e.At-at >= int64(TrashRetention) {
			return ErrInvalid
		}
	case forget:
		if state == "trashed" {
			return ErrInvalid
		}
	case erase, disconnect:
	}
	return nil
}

func (s *Store) apply(ctx context.Context, tx *canon.Tx, e entry, sequence int, digest string) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO removal_applied(sequence,id,digest) VALUES(?,?,?)", sequence, e.ID, digest); err != nil {
		return err
	}
	for _, target := range e.Targets {
		state := ""
		switch e.Action {
		case forget:
			state = "trashed"
		case restore:
			state = "active"
		case erase:
			state = "erased"
		case disconnect:
			state = "disconnected"
		}
		provenance, err := json.Marshal(target)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO retract_target(kind,id,person,holder,agent,state,at,provenance,operation_id) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(kind,id) DO UPDATE SET person=excluded.person,holder=excluded.holder,agent=excluded.agent,state=excluded.state,at=excluded.at,provenance=excluded.provenance,operation_id=excluded.operation_id`, target.Kind, target.ID, e.Person, target.Holder, target.Agent, state, e.At, provenance, e.ID)
		if err != nil {
			return err
		}
		if target.Kind == Memory {
			if _, err := tx.ExecContext(ctx, "DELETE FROM retract_block WHERE item_id=?", target.ID); err != nil {
				return err
			}
			if e.Action != restore {
				for _, event := range target.Events {
					if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO retract_block(item_id,person,holder,event_id) VALUES(?,?,?,?)", target.ID, e.Person, target.Holder, event); err != nil {
						return err
					}
				}
				for _, conversation := range target.Conversations {
					if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO retract_block(item_id,person,holder,conversation_id) VALUES(?,?,?,?)", target.ID, e.Person, target.Holder, conversation); err != nil {
						return err
					}
				}
			}
		}
		if err := s.projection.apply(ctx, tx, e, target); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, "UPDATE retract_meta SET epoch=epoch+1 WHERE singleton=1 AND epoch<?", int64(math.MaxInt64))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalid
	}
	return s.fail("after_apply")
}
func (s *Store) fail(stage string) error {
	if s.fault != nil {
		return s.fault(stage)
	}
	return nil
}
