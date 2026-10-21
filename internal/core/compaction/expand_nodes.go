// SPDX-License-Identifier: Apache-2.0
package compaction

import (
	"context"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func treeCoverage(ctx context.Context, tx *canon.Tx, id string) ([]Coverage, error) {
	rows, err := tx.QueryContext(ctx, `WITH RECURSIVE tree(id) AS (SELECT ? UNION ALL SELECT e.child_id FROM sum_edge e JOIN tree t ON e.parent_id=t.id)
 SELECT c.event_id,c.reason FROM sum_cover c JOIN tree t ON c.node_id=t.id JOIN conv_event e ON e.id=c.event_id ORDER BY e.rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var coverage []Coverage
	for rows.Next() {
		var c Coverage
		if err := rows.Scan(&c.EventID, &c.Reason); err != nil {
			return nil, err
		}
		coverage = append(coverage, c)
	}
	return coverage, rows.Err()
}

// ExpandEvents pages the exact visible source events in ledger order, including
// descendants. Deliberately skipped events are not returned as summary evidence.
func (s *Store) ExpandEvents(ctx context.Context, agent, id string, offset, limit int) ([]string, int, error) {
	if agent == "" || id == "" || offset < 0 || limit < 1 || limit > 1000 {
		return nil, 0, ErrExpand
	}
	var ids []string
	next := 0
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		node, err := readNode(ctx, tx, id)
		if err != nil {
			return err
		}
		var epoch uint64
		if err = tx.QueryRowContext(ctx, `SELECT epoch FROM retract_meta WHERE singleton=1`).Scan(&epoch); err != nil {
			return err
		}
		if node.Status != "ready" || node.Epoch != epoch {
			return ErrUnavailable
		}
		coverage, err := treeCoverage(ctx, tx, id)
		if err != nil {
			return err
		}
		var all []string
		for _, c := range coverage {
			// Check ownership even for skipped rows: an empty page cannot reveal a
			// foreign node's existence, validity or extent.
			var owner string
			if err = tx.QueryRowContext(ctx, `SELECT agent_id FROM conv_event WHERE id=?`, c.EventID).Scan(&owner); err != nil {
				return err
			}
			if owner != agent {
				return ErrExpand
			}
			if c.Reason == "" {
				all = append(all, c.EventID)
			}
		}
		if offset >= len(all) {
			return nil
		}
		end := offset + min(limit, len(all)-offset)
		ids = append(ids, all[offset:end]...)
		if end < len(all) {
			next = end
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return ids, next, nil
}
