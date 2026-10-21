// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"errors"
	"math"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

// Canon carries the maximum into the next boot, where MintEpoch durably
// advances it before any dispatch. Restore never replaces the target controls.
func chooseRestoreEpoch(ctx context.Context, controls *ctlfiles.Store, currentPath string, captured uint64) (uint64, error) {
	if controls == nil || !controls.Locked() || captured == 0 {
		return 0, errors.New("restore requires a locked target and captured epoch")
	}
	current, err := controls.Epoch()
	if err != nil {
		return 0, err
	}
	_, previous, err := canon.Inspect(ctx, currentPath)
	if err != nil {
		return 0, err
	}
	epoch := max(current, previous, captured)
	if epoch == math.MaxUint64 {
		return 0, errors.New("epoch exhausted")
	}
	return epoch, nil
}
