// SPDX-License-Identifier: Apache-2.0
package canon_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestWorkLedgerSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(ctx, canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	err = db.Read(ctx, func(tx *canon.Tx) error {
		for _, table := range []string{"work_ledger", "work_cards", "work_active_runs"} {
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?", table).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Errorf("missing work ledger table %s", table)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
