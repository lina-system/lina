// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

// Resume rechecks the stopped source before opening the publication gate. A
// forgetting made at the source while restore was interrupted still wins.
// The target remains locked and unreadable throughout this reconciliation.
func finalizeRestore(ctx context.Context, root *os.Root, controls *ctlfiles.Store, target, generation string, m Manifest, migrations []canon.Migration) (result error) {
	source, err := os.OpenRoot(filepath.Dir(generation))
	if err != nil {
		return err
	}
	defer source.Close()
	if err := regularPath(source, "removals"); err != nil {
		return err
	}
	sourceList, err := source.ReadFile("removals")
	if err != nil {
		return err
	}
	if err := regularPath(root, "backups/removals"); err != nil {
		return err
	}
	targetList, err := root.ReadFile("backups/removals")
	if err != nil {
		return err
	}
	merged, err := mergeRestoreRemovals(sourceList, targetList)
	if err != nil {
		return err
	}
	path := filepath.Join(target, "canon/canon.sqlite")
	if err := checkRestoreHistory(ctx, merged, path, filepath.Dir(generation), m); err != nil {
		return err
	}
	next := restorePending + "/removals-" + rand.Text()
	if err := writeRestoreFile(root, next, merged); err != nil {
		return err
	}
	if err := root.Rename(next, "backups/removals"); err != nil {
		return err
	}
	if err := errors.Join(syncDir(root, "backups"), syncDir(root, restorePending)); err != nil {
		return err
	}
	captured, err := strconv.ParseUint(m.Epoch, 10, 64)
	if err != nil {
		return err
	}
	epoch, err := chooseRestoreEpoch(ctx, controls, path, captured)
	if err != nil {
		return err
	}
	// These temporary controls belong only to recovery's writer. The installed
	// epoch, main identity and restart receipt remain untouched.
	staging := restorePending + "/reconcile-" + rand.Text()
	if err := privateDir(root, staging); err != nil {
		return err
	}
	temporary, err := ctlfiles.Open(filepath.Join(target, staging))
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, temporary.Close()) }()
	if err := temporary.Acquire(); err != nil {
		return err
	}
	if err := temporary.Write("epoch", []byte(strconv.FormatUint(epoch, 10))); err != nil {
		return err
	}
	if err := reconcileRestoredCanon(ctx, path, target, epoch, temporary, migrations); err != nil {
		return err
	}
	if err := syncDir(root, "canon"); err != nil {
		return err
	}
	if _, err := restoreHaltEpoch(controls); err != nil {
		return err
	}
	return controls.ClearHalt()
}
