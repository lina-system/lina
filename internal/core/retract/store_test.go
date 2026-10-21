// SPDX-License-Identifier: Apache-2.0
package retract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func fixture(t *testing.T) (*canon.Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	return db, root
}

// The small projection isolates the disk/SQLite crash protocol from the memory
// schema. Separate integration scenarios use the real ledger and memory stores.
func protocolStore(t *testing.T, db *canon.Store, root string) *Store {
	t.Helper()
	s, err := openStore(t.Context(), Config{Canon: db, Root: root, Now: func() time.Time { return time.Unix(0, 123) }}, projection{
		resolve: func(ctx context.Context, tx *canon.Tx, ref Ref, person string) (Target, error) {
			var state string
			if err := tx.QueryRowContext(ctx, "SELECT state FROM removal_test_content WHERE id=?", ref.ID).Scan(&state); err != nil {
				return Target{}, err
			}
			return Target{Kind: Memory, ID: ref.ID, Holder: "all", Agent: "agent-1", Events: []string{"event-1"}, Conversations: []string{"conversation-1"}}, nil
		},
		apply: func(ctx context.Context, tx *canon.Tx, e entry, target Target) error {
			_, err := tx.ExecContext(ctx, "UPDATE removal_test_content SET state=? WHERE id=?", string(e.Action), target.ID)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedProtocol(t *testing.T, db *canon.Store) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "CREATE TABLE removal_test_content(id TEXT PRIMARY KEY,body TEXT,state TEXT); INSERT INTO removal_test_content VALUES('memory-1','synthetic violet','active')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func visibleProtocol(t *testing.T, db *canon.Store) int {
	t.Helper()
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM removal_test_content WHERE state='active'").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDurableIntentReplaysBeforeReads(t *testing.T) {
	for _, stage := range []string{"after_sync", "after_apply"} {
		t.Run(stage, func(t *testing.T) {
			db, root := fixture(t)
			seedProtocol(t, db)
			s := protocolStore(t, db, root)
			failure := errors.New("injected crash boundary")
			s.fault = func(at string) error {
				if at == stage {
					return failure
				}
				return nil
			}
			if err := s.Forget(t.Context(), Request{Person: "person-1", Targets: []Ref{{Kind: Memory, ID: "memory-1"}}}); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if err := db.Read(t.Context(), func(*canon.Tx) error { return nil }); !errors.Is(err, canon.ErrRecoveryRequired) {
				t.Fatalf("partial state was readable: %v", err)
			}
			// No tombstone or epoch update escaped the rolled-back transaction.
			if err := db.Write(t.Context(), func(tx *canon.Tx) error {
				var n int
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM retract_target").Scan(&n); err != nil {
					return err
				}
				if n != 0 {
					t.Fatal("target escaped rollback")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			s.fault = nil
			if err := s.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			if visibleProtocol(t, db) != 0 {
				t.Fatal("forgotten content exposed after replay")
			}
			before, err := s.Epoch(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			after, err := s.Epoch(t.Context())
			if err != nil || after != before || after != 1 {
				t.Fatalf("non-idempotent replay: %d %d %v", before, after, err)
			}
		})
	}
}

func TestMissingOrTruncatedRemovalListRefusesReads(t *testing.T) {
	db, root := fixture(t)
	seedProtocol(t, db)
	s := protocolStore(t, db, root)
	if err := s.Forget(t.Context(), Request{Person: "person-1", Targets: []Ref{{Kind: Memory, ID: "memory-1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(root, "backups", "removals"), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Recover(t.Context()); !errors.Is(err, ErrJournal) {
		t.Fatalf("missing acknowledged removal: %v", err)
	}
	if err := db.Read(t.Context(), func(*canon.Tx) error { return nil }); !errors.Is(err, canon.ErrRecoveryRequired) {
		t.Fatal(err)
	}
}

func TestInvalidRemovalDoesNotCloseReads(t *testing.T) {
	db, root := fixture(t)
	seedProtocol(t, db)
	s := protocolStore(t, db, root)
	if err := s.Forget(t.Context(), Request{Person: "person-1", Targets: []Ref{{Kind: Memory, ID: "absent"}}}); err == nil {
		t.Fatal("accepted missing item")
	}
	if visibleProtocol(t, db) != 1 {
		t.Fatal("invalid request changed visibility")
	}
	data, err := os.ReadFile(filepath.Join(root, "backups", "removals"))
	if err != nil || len(data) != 0 {
		t.Fatalf("invalid intent journaled: %s %v", data, err)
	}
}
