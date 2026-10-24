// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// ReassignSubject moves one third-party item to another people record inside
// the caller's transaction. Sender identity uses it when two records merge,
// when a record splits, and when a link moves to the right record, so an item
// follows the identity of its evidence. It never changes the person, the
// holder, the scope or the evidence, and it writes a new revision like any
// other change.
func ReassignSubject(ctx context.Context, tx *canon.Tx, person, itemID, from, to string) (Item, error) {
	if !present(person) || !present(itemID) || !present(from) || !present(to) || from == to {
		return Item{}, ErrInvalid
	}
	item, err := readItem(ctx, tx, itemID)
	if err != nil {
		return Item{}, err
	}
	if item.Person != person || item.Subject.Kind != ThirdParty || item.Subject.ID != from || item.State != Active {
		return Item{}, ErrInvalid
	}
	item.Subject.ID = to
	item.Revision++
	raw, err := marshalRecord(item)
	if err != nil {
		return Item{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE memory_item SET subject_id=?,record=? WHERE id=?", to, raw, item.ID); err != nil {
		return Item{}, err
	}
	if err = revision(ctx, tx, item, raw); err != nil {
		return Item{}, err
	}
	return item, nil
}
