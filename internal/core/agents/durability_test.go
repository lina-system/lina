// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func durabilityStore(t *testing.T, options ...Option) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
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
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations(), NoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	s, err := Open(root, db, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}

// Core opens the store without options, so saves must reach os.File.Sync and
// report its failure.
func TestDurableStoreSyncsPersonaFiles(t *testing.T) {
	s, root := durabilityStore(t)
	file, err := os.Create(filepath.Join(root, "probe"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.sync(file); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("default sync did not reach os.File.Sync: %v", err)
	}
	synced := errors.New("sync failed")
	calls := 0
	s.syncFile = func(f *os.File) error {
		calls++
		return synced
	}
	if _, err := s.Create(t.Context(), Create{Name: "writer", PersonID: "person"}); !errors.Is(err, synced) {
		t.Fatalf("save ignored the sync failure: %v", err)
	}
	if calls == 0 {
		t.Fatal("saving a revision did not sync its files")
	}
}

// A throwaway root records the same rows without paying for each fsync.
func TestNoSyncStoreSkipsPersonaFileSync(t *testing.T) {
	s, _ := durabilityStore(t, NoSync())
	calls := 0
	s.syncFile = func(*os.File) error {
		calls++
		return nil
	}
	created, err := s.Create(t.Context(), Create{Name: "writer", PersonID: "person"})
	if err != nil {
		t.Fatal(err)
	}
	if a, err := s.Get(t.Context(), created.ID); err != nil || a.Name != "writer" {
		t.Fatalf("revision written without sync: %v", err)
	}
	if calls != 0 {
		t.Fatalf("store opened for a throwaway root synced files %d times", calls)
	}
}
