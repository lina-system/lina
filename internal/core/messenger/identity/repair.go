// SPDX-License-Identifier: Apache-2.0
package identity

import (
	"context"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/retract"
)

// confirmationWindow retains every recorded restriction when the person's
// answer creates a new destination link. No evidence means unbounded validity.
func confirmationWindow(links []Link) (from, through time.Time) {
	for _, link := range links {
		if link.ValidFrom.After(from) {
			from = link.ValidFrom
		}
		if !link.ValidThrough.IsZero() && (through.IsZero() || link.ValidThrough.Before(through)) {
			through = link.ValidThrough
		}
	}
	return from, through
}

func (s *Store) queueRepairTx(ctx context.Context, tx *canon.Tx, link string, items []string) error {
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO identity_repair(person,link,item) VALUES(?,?,?)", s.person, link, item); err != nil {
			return err
		}
	}
	return nil
}

// recoverRepairs delivers committed intent before matching resumes. A crash
// after Forget but before acknowledgment is safe: an item that is already
// retracted, or whose row is gone, is acknowledged without a second Forget.
// An item that is still active is forgotten even when an ancestor hides it.
func (s *Store) recoverRepairs(ctx context.Context) error {
	s.repairMu.Lock()
	defer s.repairMu.Unlock()
	type repair struct{ link, item string }
	var pending []repair
	if err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT link,item FROM identity_repair WHERE person=? ORDER BY link,item", s.person)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r repair
			if err := rows.Scan(&r.link, &r.item); err != nil {
				return err
			}
			pending = append(pending, r)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, r := range pending {
		var repairable bool
		if err := s.db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM memory_item m
WHERE m.id=? AND m.person=? AND m.state='active'
AND NOT EXISTS(SELECT 1 FROM retract_target r WHERE r.kind='memory' AND r.id=m.id AND r.state<>'active'))`,
				r.item, s.person).Scan(&repairable)
		}); err != nil {
			return err
		}
		if repairable {
			if s.removals == nil {
				return ErrInvalid
			}
			if err := s.removals.Forget(ctx, retract.Request{Person: s.person, Targets: []retract.Ref{{Kind: retract.Memory, ID: r.item}}}); err != nil {
				return err
			}
		}
		if err := s.db.Write(ctx, func(tx *canon.Tx) error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM identity_artifact WHERE link=? AND kind='item' AND id=?", r.link, r.item); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM identity_repair WHERE person=? AND link=? AND item=?", s.person, r.link, r.item)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
