// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"context"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var ErrStaleSnapshot = errors.New("stale_retraction_snapshot")

// Snapshot is a content-free, person/agent-scoped retraction view for one
// preparation. Its private sets cannot be mutated by callers. The composition
// root filters packet references and ledger history through Visible, uses
// DerivedValid for summaries, and Validate before dispatch. It never persists
// a ContextPacket. This checks revocation, not ownership or permission: callers
// must first obtain authorized candidates through their store's scoped reads.
// The zero value refuses all input.
type Snapshot struct {
	Epoch         uint64
	ready         bool
	denied        map[Ref]bool
	conversations map[string]bool
}

func (v Snapshot) Visible(ref Ref) bool {
	if !v.ready || ref.ID == "" {
		return false
	}
	switch ref.Kind {
	case Memory, Event, Source:
		return !v.denied[ref]
	default:
		return false
	}
}
func (v Snapshot) DerivedValid(epoch uint64, refs []Ref) bool {
	if !v.ready || epoch != v.Epoch {
		return false
	}
	for _, ref := range refs {
		if !v.Visible(ref) {
			return false
		}
	}
	return true
}
func (v Snapshot) CanExtract(events, conversations []string) bool {
	if !v.ready {
		return false
	}
	for _, id := range events {
		if !v.Visible(Ref{Kind: Event, ID: id}) {
			return false
		}
	}
	for _, id := range conversations {
		if id == "" || v.conversations[id] {
			return false
		}
	}
	return true
}

func (s *Store) Snapshot(ctx context.Context, person, agent string) (Snapshot, error) {
	if person == "" || agent == "" {
		return Snapshot{}, ErrInvalid
	}
	v := Snapshot{denied: map[Ref]bool{}, conversations: map[string]bool{}}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&v.Epoch); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT kind,id FROM retract_target WHERE person=? AND state<>'active'
AND (holder='all' OR holder=? OR (holder='' AND (agent='' OR agent=?)))`, person, agent, agent)
		if err != nil {
			return err
		}
		for rows.Next() {
			var ref Ref
			if err := rows.Scan(&ref.Kind, &ref.ID); err != nil {
				rows.Close()
				return err
			}
			v.denied[ref] = true
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT m.id FROM memory_item m JOIN retract_memory_exclusion x ON x.id=m.id
WHERE m.person=? AND (m.holder='all' OR m.holder=?)`, person, agent)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			v.denied[Ref{Kind: Memory, ID: id}] = true
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, `SELECT x.event_id FROM retract_excluded_event x
JOIN retract_target r ON r.kind='memory' AND r.id=x.item_id
WHERE r.person=? AND (r.holder='all' OR r.holder=?)`, person, agent)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			v.denied[Ref{Kind: Event, ID: id}] = true
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		rows, err = tx.QueryContext(ctx, "SELECT event_id,conversation_id FROM retract_block WHERE person=? AND (holder='all' OR holder=?)", person, agent)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event, conversation string
			if err := rows.Scan(&event, &conversation); err != nil {
				return err
			}
			if event != "" {
				v.denied[Ref{Kind: Event, ID: event}] = true
			}
			if conversation != "" {
				v.conversations[conversation] = true
			}
		}
		return rows.Err()
	})
	if err != nil {
		return Snapshot{}, err
	}
	v.ready = true
	return v, nil
}

// Validate rejects a request assembled against an earlier revocation epoch.
// An already dispatched request is not recalled. Each new preparation takes
// a fresh Snapshot and rebuilds stale derived inputs before using them.
func (s *Store) Validate(ctx context.Context, epoch uint64) error {
	current, err := s.Epoch(ctx)
	if err != nil {
		return err
	}
	if current != epoch {
		return ErrStaleSnapshot
	}
	return nil
}
