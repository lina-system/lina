// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func openGeneration(t *testing.T, root string) (*canon.Store, func()) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.Acquire(); err != nil {
		controls.Close()
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 2, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		controls.Close()
		t.Fatal(err)
	}
	var once sync.Once
	close := func() { once.Do(func() { db.Close(context.Background()); controls.Close() }) }
	t.Cleanup(close)
	return db, close
}

func backupCanon(t *testing.T, source, destination string) {
	t.Helper()
	reader, err := canon.OpenSQLiteReader(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := canon.BackupSQLite(t.Context(), reader, destination); err != nil {
		t.Fatal(err)
	}
}

func TestOldGenerationReplaysLatestRemovalListBeforeRead(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"forget", "erase", "recover"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "state")
			db, closeDB := openGeneration(t, root)
			seedProtocol(t, db)
			s := protocolStore(t, db, root)
			generation := filepath.Join(t.TempDir(), "canon.sqlite")
			backupCanon(t, filepath.Join(root, "canon.sqlite"), generation)
			r := Request{Person: "person-1", Targets: []Ref{{Kind: Memory, ID: "memory-1"}}}
			if err := s.Forget(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "erase":
				if err := s.Delete(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			case "recover":
				if err := s.Restore(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			}
			removalList, err := os.ReadFile(filepath.Join(root, "backups", "removals"))
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			closeDB()
			// The only surviving current state is the out-of-generation removal list.
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "backups"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "backups", "removals"), removalList, 0600); err != nil {
				t.Fatal(err)
			}
			backupCanon(t, generation, filepath.Join(root, "canon.sqlite"))
			db, _ = openGeneration(t, root)
			s = protocolStore(t, db, root) // Recovery completes before this store is returned.
			v, err := s.Snapshot(t.Context(), "person-1", "agent-1")
			if err != nil {
				t.Fatal(err)
			}
			if v.Visible(r.Targets[0]) != (operation == "recover") {
				t.Fatal("old generation resurrected forgotten item or lost recovery")
			}
			if operation != "recover" && visibleProtocol(t, db) != 0 {
				t.Fatal("old generation exposed removed content")
			}
			var state string
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT state FROM retract_target WHERE id='memory-1'").Scan(&state)
			}); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"forget": "trashed", "erase": "erased", "recover": "active"}[operation]
			if state != want {
				t.Fatalf("replay state %q, want %q", state, want)
			}
		})
	}
}
