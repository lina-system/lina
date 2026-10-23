// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// CurrentRelationship is an observer's current view of one person. Missing
// views stay absent. Apply the same removal, validity and endpoint-scope filters
// as recall, selecting relationship kind and the exact holder before the limit.
func (s *Store) CurrentRelationship(ctx context.Context, scope RecallScope, at int64) ([]Hit, error) {
	if scope.Projection {
		return nil, nil
	}
	return s.recall(ctx, scope, Query{At: at, Limit: 1, PersonOnly: true}, false, true)
}

// endRelationshipPredecessor closes the previous validity interval in Insert's
// transaction for every writer. The end survives deletion of the replacement;
// historical recall can still distinguish the earlier view from a current one.
// Use the existing validity/revision format, without retaining erased content.
func endRelationshipPredecessor(ctx context.Context, tx *canon.Tx, replacement Item) error {
	if replacement.Supersedes == "" {
		return nil
	}
	if replacement.Kind != Relationship || replacement.ValidFrom <= 0 {
		return ErrInvalid
	}
	previous, err := readItem(ctx, tx, replacement.Supersedes)
	if err != nil {
		return err
	}
	if previous.Kind != Relationship || previous.Person != replacement.Person || previous.Holder != replacement.Holder || previous.Subject != replacement.Subject || previous.Scope != replacement.Scope || replacement.ValidFrom < previous.ValidFrom {
		return ErrSupersedes
	}
	previous.ValidUntil = replacement.ValidFrom
	previous.Revision++
	raw, err := marshalRecord(previous)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE memory_item SET valid_until=?,record=? WHERE id=?", previous.ValidUntil, raw, previous.ID); err != nil {
		return err
	}
	return revision(ctx, tx, previous, raw)
}
