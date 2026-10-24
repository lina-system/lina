// SPDX-License-Identifier: Apache-2.0

package localmodel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestModelServedLedgerOmitsTheTask(t *testing.T) {
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
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(t.Context())
	row := Row{ID: "0123456789abcdef0123456789abcdef", Kind: Intent, Provider: provider.ProviderID("apple"), Model: "apple-foundation-on-device", Route: RouteLocal, Reason: ReasonFit, Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Created: "2026-10-23T00:00:00Z"}
	if err = Write(t.Context(), db, row); err != nil {
		t.Fatal(err)
	}
	providerName, model, err := ReadModel(t.Context(), db, row.ID)
	if err != nil || providerName != "apple" || model != row.Model {
		t.Fatalf("provider=%s model=%s err=%v", providerName, model, err)
	}
	err = db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `DELETE FROM model_served WHERE id=?`, row.ID)
		return err
	})
	if err == nil {
		t.Fatal("ledger row was deleted")
	}
}
