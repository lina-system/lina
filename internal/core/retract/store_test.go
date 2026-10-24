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

func TestForgetDuringCommitsTheCallerWriteWithTheRemoval(t *testing.T) {
	t.Parallel()
	db, root := fixture(t)
	seedProtocol(t, db)
	s := protocolStore(t, db, root)
	err := s.ForgetDuring(t.Context(), "person-1", func(tx *canon.Tx) ([]Ref, error) {
		if _, err := tx.ExecContext(t.Context(), "UPDATE removal_test_content SET body='moved' WHERE id='memory-1'"); err != nil {
			return nil, err
		}
		return []Ref{{Kind: Memory, ID: "memory-1"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body, state := protocolRow(t, db)
	if body != "moved" || state != "forget" {
		t.Fatalf("body=%q state=%q", body, state)
	}
}

func TestForgetDuringRollsBackTheCallerWrite(t *testing.T) {
	t.Parallel()
	db, root := fixture(t)
	seedProtocol(t, db)
	s := protocolStore(t, db, root)
	stop := errors.New("removal stopped")
	err := s.ForgetDuring(t.Context(), "person-1", func(tx *canon.Tx) ([]Ref, error) {
		if _, err := tx.ExecContext(t.Context(), "UPDATE removal_test_content SET body='moved' WHERE id='memory-1'"); err != nil {
			return nil, err
		}
		return nil, stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	body, state := protocolRow(t, db)
	if body != "synthetic violet" || state != "active" {
		t.Fatalf("partial write stayed: body=%q state=%q", body, state)
	}
}

func TestForgetDuringEmptyListCommitsWithoutAJournalLine(t *testing.T) {
	t.Parallel()
	db, root := fixture(t)
	seedProtocol(t, db)
	s := protocolStore(t, db, root)
	err := s.ForgetDuring(t.Context(), "person-1", func(tx *canon.Tx) ([]Ref, error) {
		if _, err := tx.ExecContext(t.Context(), "UPDATE removal_test_content SET body='moved' WHERE id='memory-1'"); err != nil {
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	body, state := protocolRow(t, db)
	if body != "moved" || state != "active" {
		t.Fatalf("body=%q state=%q", body, state)
	}
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM removal_applied").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("removal lines = %d", n)
	}
}

func TestForgetDuringCrashRollsBackTheCallerWrite(t *testing.T) {
	t.Parallel()
	db, root := fixture(t)
	seedProtocol(t, db)
	s := protocolStore(t, db, root)
	failure := errors.New("injected crash boundary")
	s.fault = func(stage string) error {
		if stage == "after_sync" {
			return failure
		}
		return nil
	}
	err := s.ForgetDuring(t.Context(), "person-1", func(tx *canon.Tx) ([]Ref, error) {
		if _, err := tx.ExecContext(t.Context(), "UPDATE removal_test_content SET body='moved' WHERE id='memory-1'"); err != nil {
			return nil, err
		}
		return []Ref{{Kind: Memory, ID: "memory-1"}}, nil
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := db.Read(t.Context(), func(*canon.Tx) error { return nil }); !errors.Is(err, canon.ErrRecoveryRequired) {
		t.Fatalf("partial state was readable: %v", err)
	}
	inspection, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(root, "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var body, state string
	err = inspection.QueryRowContext(t.Context(), "SELECT body,state FROM removal_test_content WHERE id='memory-1'").Scan(&body, &state)
	inspection.Close()
	if err != nil {
		t.Fatal(err)
	}
	if body != "synthetic violet" || state != "active" {
		t.Fatalf("caller write survived the crash boundary: body=%q state=%q", body, state)
	}
	s.fault = nil
	if err := s.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	body, state = protocolRow(t, db)
	if body != "synthetic violet" || state != "forget" {
		t.Fatalf("after replay body=%q state=%q", body, state)
	}
}

func protocolRow(t *testing.T, db *canon.Store) (string, string) {
	t.Helper()
	var body, state string
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT body,state FROM removal_test_content WHERE id='memory-1'").Scan(&body, &state)
	}); err != nil {
		t.Fatal(err)
	}
	return body, state
}

func TestDurableIntentReplaysBeforeReads(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"after_sync", "after_apply"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
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
			if err := s.Forget(t.Context(), Request{Person: "person-1", Targets: []Ref{{Kind: Memory, ID: "memory-1"}}}); !errors.Is(err, ErrRecovery) {
				t.Fatalf("accepted another operation before replay: %v", err)
			}
			// No tombstone or epoch update escaped the rolled-back transaction.
			inspection, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(root, "canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			var n int
			err = inspection.QueryRowContext(t.Context(), "SELECT count(*) FROM retract_target").Scan(&n)
			inspection.Close()
			if err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatal("target escaped rollback")
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
	t.Parallel()
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
	t.Parallel()
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

func TestTrashRecoveryAndExpiryBoundaries(t *testing.T) {
	t.Parallel()
	for _, atBoundary := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_deadline", true: "at_deadline"}[atBoundary], func(t *testing.T) {
			t.Parallel()
			db, root := fixture(t)
			seedProtocol(t, db)
			s := protocolStore(t, db, root)
			r := Request{Person: "person-1", Targets: []Ref{{Kind: Memory, ID: "memory-1"}}}
			if err := s.Forget(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			delta := TrashRetention - time.Nanosecond
			if atBoundary {
				delta = TrashRetention
			}
			s.now = func() time.Time { return time.Unix(0, 123).Add(delta) }
			err := s.Restore(t.Context(), r)
			if atBoundary {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("restored expired trash: %v", err)
				}
				if err := s.Expire(t.Context(), "person-1"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
			}
			var state string
			var blocks int
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				if err := tx.QueryRowContext(t.Context(), "SELECT state FROM retract_target WHERE id='memory-1'").Scan(&state); err != nil {
					return err
				}
				return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM retract_block").Scan(&blocks)
			}); err != nil {
				t.Fatal(err)
			}
			if atBoundary {
				if state != "erased" || blocks != 2 {
					t.Fatalf("expiry state=%s blocks=%d", state, blocks)
				}
			} else if state != "active" || blocks != 0 {
				t.Fatalf("recovery state=%s blocks=%d", state, blocks)
			}
			before, err := s.Epoch(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Expire(t.Context(), "person-1"); err != nil {
				t.Fatal(err)
			}
			after, err := s.Epoch(t.Context())
			if err != nil || after != before {
				t.Fatalf("empty expiry advanced epoch: %d %d %v", before, after, err)
			}
		})
	}
}

func TestJournalSyncPrecedesTombstoneAndFailureClosesAccess(t *testing.T) {
	t.Parallel()
	for _, failSync := range []bool{false, true} {
		t.Run(map[bool]string{false: "synced", true: "sync_failed"}[failSync], func(t *testing.T) {
			t.Parallel()
			db, root := fixture(t)
			seedProtocol(t, db)
			s := protocolStore(t, db, root)
			syncFile := s.journal.sync
			failed := errors.New("fsync unavailable")
			s.journal.sync = func() error {
				inspection, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(root, "canon.sqlite"))
				if err != nil {
					return err
				}
				defer inspection.Close()
				var n int
				if err := inspection.QueryRowContext(t.Context(), "SELECT count(*) FROM retract_target WHERE state='erased'").Scan(&n); err != nil {
					return err
				}
				if n != 0 {
					t.Fatal("tombstone committed before journal sync")
				}
				if failSync {
					return failed
				}
				return syncFile()
			}
			err := s.Delete(t.Context(), Request{Person: "person-1", Targets: []Ref{{Kind: Memory, ID: "memory-1"}}})
			if failSync {
				if !errors.Is(err, failed) || !errors.Is(err, ErrRecovery) {
					t.Fatalf("uncertain removal result: %v", err)
				}
				if err := db.Write(t.Context(), func(*canon.Tx) error { return nil }); !errors.Is(err, canon.ErrRecoveryRequired) {
					t.Fatalf("pending removal permitted new write: %v", err)
				}
				if err := s.Recover(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			entries, err := s.journal.read()
			if err != nil || len(entries) != 1 {
				t.Fatalf("removal line: %+v %v", entries, err)
			}
			var operation string
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT operation_id FROM retract_target WHERE id='memory-1' AND state='erased'").Scan(&operation)
			}); err != nil {
				t.Fatal(err)
			}
			if operation != entries[0].ID {
				t.Fatal("tombstone has no matching durable removal line")
			}
		})
	}
}
