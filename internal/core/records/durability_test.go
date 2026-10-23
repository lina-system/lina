// SPDX-License-Identifier: Apache-2.0

package records

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func durabilityStore(t *testing.T, options ...Option) *Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations(), NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	s, err := Open(root, db, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Core opens records without options, so a repository write must reach the
// file sync and report its failure.
func TestDurableStoreSyncsRepositoryWrites(t *testing.T) {
	s := durabilityStore(t)
	failure := errors.New("sync failed")
	calls := 0
	s.syncFile = func(*os.File) error {
		calls++
		return failure
	}
	_, err := s.Commit(t.Context(), Change{ID: "memo", Path: "projects/memo.md", Data: doc("memo", "Probe")}, actor)
	if !errors.Is(err, failure) {
		t.Fatalf("commit ignored the sync failure: %v", err)
	}
	if calls == 0 {
		t.Fatal("committing a record did not sync its files")
	}
}

// A throwaway root records the same content without paying for each fsync.
func TestNoSyncStoreSkipsRepositoryWrites(t *testing.T) {
	s := durabilityStore(t, NoSync())
	calls := 0
	s.syncFile = func(*os.File) error {
		calls++
		return nil
	}
	rev, err := s.Commit(t.Context(), Change{ID: "memo", Path: "projects/memo.md", Data: doc("memo", "Probe")}, actor)
	if err != nil {
		t.Fatal(err)
	}
	record, got, err := s.Read(t.Context(), "memo")
	if err != nil {
		t.Fatal(err)
	}
	if got != rev || record.ID != "memo" {
		t.Fatalf("record written without sync: %v/%v", record.ID, got)
	}
	if calls != 0 {
		t.Fatalf("store opened for a throwaway root synced files %d times", calls)
	}
}
