// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
)

func TestRestoringOldCanonCannotResurrectRestart(t *testing.T) {
	root := restartRoot(t)
	s, epoch := restartStore(t, root)
	x := restarter(t, s, epoch, lifecycle.NewRegistry(nil))
	request, err := x.Request(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	// A display projection from the time a request was owed is deliberately in
	// this generation. It must never be interpreted as lifecycle authority.
	path := filepath.Join(root, "canon.sqlite")
	db, err := canon.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE old_status(request_id TEXT, owed INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO old_status VALUES(?, 1)", request.ID); err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(t.TempDir(), "generation.sqlite")
	if err := canon.BackupSQLite(t.Context(), db, generation); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := x.Ready(); err != nil {
		t.Fatal(err)
	}
	if exit, _, err := x.Step(t.Context()); !exit || err != nil {
		t.Fatalf("commit: %v %v", exit, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, epoch = restartStore(t, root)
	x = restarter(t, s, epoch, lifecycle.NewRegistry(nil))
	if err := x.Ready(); err != nil {
		t.Fatal(err)
	}
	terminal := x.Status()
	if terminal.Owed() {
		t.Fatal("request not discharged")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{root, restartRoot(t)} {
		// CP-03 restores canonical data with Core stopped and leaves system/core
		// alone. Use a real SQLite online-backup image, including an old owed row.
		image, err := os.ReadFile(generation)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "canon.sqlite"), image, 0600); err != nil {
			t.Fatal(err)
		}
		reader, err := canon.OpenSQLiteReader(t.Context(), filepath.Join(target, "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		var oldID string
		var oldOwed int
		err = reader.QueryRowContext(t.Context(), "SELECT request_id, owed FROM old_status").Scan(&oldID, &oldOwed)
		reader.Close()
		if err != nil || oldID != request.ID || oldOwed != 1 {
			t.Fatalf("old generation not restored: %s %d %v", oldID, oldOwed, err)
		}
		restored, epoch := restartStore(t, target)
		x := restarter(t, restored, epoch, lifecycle.NewRegistry(nil))
		if err := x.Ready(); err != nil {
			t.Fatal(err)
		}
		if x.Status().Owed() {
			t.Fatal("old generation resurrected request")
		}
		if target == root && x.Status() != terminal {
			t.Fatal("restore changed lifecycle receipt")
		}
		if exit, _, err := x.Step(t.Context()); exit || err != nil {
			t.Fatalf("restore restarted Core: %v %v", exit, err)
		}
		if err := restored.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
