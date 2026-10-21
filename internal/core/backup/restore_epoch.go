// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ncruces/go-sqlite3"
	"math"
	"os"
	"strconv"

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
	halt, err := restoreHaltEpoch(controls)
	if err != nil {
		return 0, err
	}
	_, previous, err := canon.Inspect(ctx, currentPath)
	if err != nil {
		// Every admitted writer first durably minted its control epoch. A corrupt
		// database can be replaced using that independent fence; absent/invalid
		// controls and non-corruption errors never authorize a reset.
		if current == 0 || !damagedRestoreCanon(err) {
			return 0, err
		}
		previous = 0
	}
	epoch := max(current, previous, captured, halt)
	if epoch == math.MaxUint64 {
		return 0, errors.New("epoch exhausted")
	}
	return epoch, nil
}

func damagedRestoreCanon(err error) bool {
	return errors.Is(err, sqlite3.CORRUPT) || errors.Is(err, sqlite3.NOTADB)
}

func restoreHaltEpoch(controls *ctlfiles.Store) (uint64, error) {
	data, err := controls.Read("halt.json")
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var marker struct {
		Schema int    `json:"schema"`
		Epoch  string `json:"epoch"`
		Cause  string `json:"cause"`
	}
	if json.Unmarshal(data, &marker) != nil {
		return 0, ErrInvalid
	}
	epoch, err := strconv.ParseUint(marker.Epoch, 10, 64)
	if err != nil || epoch == 0 || strconv.FormatUint(epoch, 10) != marker.Epoch || marker.Schema != 1 {
		return 0, ErrInvalid
	}
	switch marker.Cause {
	case "sqlite_corrupt", "sqlite_notadb", "sqlite_io":
		return epoch, nil
	}
	return 0, ErrInvalid
}
