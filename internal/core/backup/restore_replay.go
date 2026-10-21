// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/retract"
)

// prepareRestoredCanon operates only on a private staging root. No store is
// returned until removal replay and authority invalidation have both finished.
func prepareRestoredCanon(ctx context.Context, root string, epoch uint64, migrations []canon.Migration) (result error) {
	controls, err := ctlfiles.Open(root)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, controls.Close()) }()
	if err := controls.Acquire(); err != nil {
		return err
	}
	if err := controls.Write("epoch", []byte(strconv.FormatUint(epoch, 10))); err != nil {
		return err
	}
	db, err := canon.Open(ctx, canon.Config{Path: filepath.Join(root, "canon/canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: migrations})
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, db.Close(context.Background())) }()
	removals, err := retract.Open(ctx, retract.Config{Canon: db, Root: root})
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, removals.Close()) }()
	return invalidateAuthority(ctx, db)
}

// The removal list is one append-only history. Different roots may hold a
// longer prefix of that same history; divergent histories cannot be ordered
// safely, so restore refuses them. Replay validates every selected entry's
// sequence and digest against canon before publication.
func mergeRestoreRemovals(source, target []byte) ([]byte, error) {
	for _, list := range [][]byte{source, target} {
		if len(list) > 0 && list[len(list)-1] != '\n' {
			return nil, retract.ErrJournal
		}
	}
	if bytes.HasPrefix(source, target) {
		return bytes.Clone(source), nil
	}
	if bytes.HasPrefix(target, source) {
		return bytes.Clone(target), nil
	}
	return nil, retract.ErrJournal
}
