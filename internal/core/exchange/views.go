// SPDX-License-Identifier: Apache-2.0
package exchange

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

// NeedPerson resolves ownership through Core's trusted decision registry. It
// cannot derive ownership from an agent's words or from a missed deadline.
func (s *Store) NeedPerson(ctx context.Context, id string, at time.Time, resolve ResolveOwner) error {
	if resolve == nil {
		return ErrNotOwner
	}
	if !timestamp(at) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		r, err := get(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Outcome != Pending {
			return ErrClosed
		}
		if r.HandOff.Decision == nil {
			return ErrInvalid
		}
		owner, err := resolve(ctx, tx, DecisionRef{ExchangeID: id, DeliveryID: r.DeliveryID, Decision: *r.HandOff.Decision})
		if err != nil {
			return err
		}
		if owner.Kind != PersonOwner || !identifier(owner.ID) {
			return ErrNotOwner
		}
		if r.NeedsPerson {
			return nil
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO exch_waiting(exchange_id,person_id,question) VALUES(?,?,?)", id, owner.ID, r.HandOff.Decision.Question); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE exchanges SET needs_person=1 WHERE id=?", id); err != nil {
			return err
		}
		return appendEvent(ctx, tx, r, r.To, "needs_person", "", at)
	})
}
func (s *Store) Waiting(ctx context.Context, person string) ([]Waiting, error) {
	if !identifier(person) {
		return nil, ErrInvalid
	}
	var result []Waiting
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT exchange_id,person_id,question FROM exch_waiting WHERE person_id=? ORDER BY exchange_id", person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w Waiting
			if err = rows.Scan(&w.ExchangeID, &w.PersonID, &w.Question); err != nil {
				return err
			}
			result = append(result, w)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Expire takes the contract's flag-and-stop option: it never replays an accepted
// model turn. Expiry is visible in the log but never creates a person decision.
func (s *Store) Expire(ctx context.Context, at time.Time) error {
	if !timestamp(at) {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM exchanges WHERE outcome=?", Pending)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			r, err := get(ctx, tx, id)
			if err != nil {
				return err
			}
			if r.State == Answered || at.Before(r.HandOff.Deadline) {
				continue
			}
			if _, err = tx.ExecContext(ctx, "UPDATE exchanges SET outcome=?,needs_person=0 WHERE id=?", Expired, id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM exch_waiting WHERE exchange_id=?", id); err != nil {
				return err
			}
			if err = appendEvent(ctx, tx, r, r.To, string(Expired), "", at); err != nil {
				return err
			}
		}
		return nil
	})
}

// Events expands only this exchange's immutable ledger. The trusted entry fixes
// every event as agent evidence; no caller can relabel one as a person turn.
func (s *Store) Events(ctx context.Context, id string) ([]Event, error) {
	var result []Event
	var eventIDs []string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		if _, err := get(ctx, tx, id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT id,delivery_id,kind,event_id FROM exch_event WHERE exchange_id=? ORDER BY id", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Event
			var eventID string
			if err := rows.Scan(&e.ID, &e.DeliveryID, &e.Kind, &eventID); err != nil {
				return err
			}
			result = append(result, e)
			eventIDs = append(eventIDs, eventID)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	l := ledger.New(s.db, s.materials)
	for i, eventID := range eventIDs {
		e, err := l.Get(ctx, eventID)
		if err != nil {
			return nil, err
		}
		result[i].Agent = e.AgentID
		result[i].At = e.RecordedAt
		result[i].Message, err = e.Message()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Log derives one compact row per exchange from its latest ledger event and
// current mailbox. Person-facing adapters enforce access before calling it.
func (s *Store) Log(ctx context.Context) ([]Line, error) {
	var result []Line
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM exchanges ORDER BY id")
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, id := range ids {
			r, err := get(ctx, tx, id)
			if err != nil {
				return err
			}
			result = append(result, Line{ID: id, From: r.From, To: r.To, Subject: r.HandOff.Subject, State: r.State, Outcome: r.Outcome, NeedsPerson: r.NeedsPerson, At: r.Updated})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
