// SPDX-License-Identifier: Apache-2.0

package ledger_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestBatchCompletionWithLargeMaterialRollbackAndRetry(t *testing.T) {
	db, cfg := database(t)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "CREATE TABLE completion (id TEXT PRIMARY KEY)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a, err := assets.Open(t.Context(), filepath.Dir(cfg.Path), db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := ledger.New(db, materials(t, a))
	large := input("large")
	large.Body = strings.Repeat("한", ledger.InlineBytes)
	batch := []ledger.Admission{{Entry: guard.EntryOwner, Input: large}, {Entry: guard.EntryOwner, Input: large}}
	complete := func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO completion(id) VALUES('turn')")
		return err
	}
	// The second event collides after completion and the first material insert.
	if events, err := s.AdmitBatchWith(t.Context(), batch, complete); err == nil || len(events) != 0 {
		t.Fatalf("failed batch acknowledged: %v %v", events, err)
	}
	var completions int
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM completion").Scan(&completions)
	}); err != nil || completions != 0 {
		t.Fatalf("completion survived rollback: %d %v", completions, err)
	}
	manifest, err := a.Backup(t.Context(), t.TempDir())
	if err != nil || len(manifest) != 0 {
		t.Fatalf("material survived rollback: %v %v", manifest, err)
	}
	batch[1].Input = input("second")
	events, err := s.AdmitBatchWith(t.Context(), batch, complete)
	if err != nil || len(events) != 2 {
		t.Fatalf("retry: %v %v", events, err)
	}
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM completion").Scan(&completions)
	}); err != nil || completions != 1 {
		t.Fatalf("completion missing: %d %v", completions, err)
	}
	got, err := s.Get(t.Context(), large.ID)
	if err != nil || got.Material.ID == "" || got.Body.Text() != large.Body {
		t.Fatalf("large body missing: %v", err)
	}
	manifest, err = a.Backup(t.Context(), t.TempDir())
	if err != nil || len(manifest) != 1 {
		t.Fatalf("retained materials: %v %v", manifest, err)
	}
}
