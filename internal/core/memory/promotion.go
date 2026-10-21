// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
	"strings"
)

// Promote is the separate person-confirmed write path. The host binds the
// confirmation to the reviewed cluster and persists its receipt in this same
// transaction. A verified external observation keeps its original precision
// and never gains person-speaker provenance from the confirmation.
func Promote(ctx context.Context, tx *canon.Tx, in Write, confirmation string, resolve ResolveEvidence) (Item, error) {
	if strings.TrimSpace(confirmation) == "" || in.Item.Basis != Imported || in.Item.Subject.Kind != Person || instructionLike(in.Item.Content) {
		return Item{}, ErrInvalid
	}
	return insert(ctx, tx, in, resolve, true)
}
