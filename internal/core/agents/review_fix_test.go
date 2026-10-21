// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func reviewStore(t *testing.T) (*Store, *canon.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: canon.GrowthSchema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	s, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, db, root
}

func TestReviewInterruptedObjectAllowsSameRevisionRetry(t *testing.T) {
	s, _, _ := reviewStore(t)
	a, err := s.Create(t.Context(), Create{Name: "Synthetic", PersonID: "person", Layers: Layers{Core: "initial"}})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected partial write")
	var interrupted string
	s.writeFile = func(f *os.File, data []byte) (int, error) {
		if !strings.Contains(filepath.ToSlash(f.Name()), "/objects/") {
			return f.Write(data)
		}
		interrupted = f.Name()
		n, err := f.Write(data[:len(data)/2])
		if err != nil {
			return n, err
		}
		return n, failure
	}
	desired := Layers{Core: "initial", UserVoice: "new voice"}
	if _, err := s.Revise(t.Context(), a.ID, a.Revision, desired, "person"); !errors.Is(err, failure) {
		t.Fatalf("injected write: %v", err)
	}
	if interrupted == "" {
		t.Fatal("fault did not reach an object write")
	}
	if partial, err := os.ReadFile(interrupted); err == nil {
		t.Logf("interrupted write left %d bytes at its final object path", len(partial))
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	s.writeFile = nil
	retried, err := s.Revise(t.Context(), a.ID, a.Revision, desired, "person")
	if err != nil {
		t.Fatalf("same Revise retry: %v (ErrCorrupt=%v)", err, errors.Is(err, ErrCorrupt))
	}
	if retried.Revision != 2 {
		t.Fatal(retried)
	}
	prepared, err := s.Prepare(t.Context(), "after-retry", a.ID)
	if err != nil || prepared.Layers != desired {
		t.Fatalf("prepared retry: %+v %v", prepared, err)
	}
}

func TestReviewConcurrentSameObjectNeverReadsPartialBytes(t *testing.T) {
	s, _, _ := reviewStore(t)
	path := filepath.Join("versions", "agents", newID(), "objects", "ab", "object")
	data := []byte("same complete object bytes")
	partial := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	s.writeFile = func(f *os.File, data []byte) (int, error) {
		n, err := f.Write(data[:len(data)/2])
		close(partial)
		if err != nil {
			return n, err
		}
		<-resume
		rest, err := f.Write(data[n:])
		return n + rest, err
	}
	go func() { done <- s.immutable(path, data) }()
	<-partial
	other := &Store{root: s.root}
	secondErr := other.immutable(path, data)
	close(resume)
	firstErr := <-done
	if firstErr != nil || secondErr != nil {
		t.Fatalf("same-object writers: first=%v second=%v", firstErr, secondErr)
	}
	got, err := s.root.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("published bytes: %q %v", got, err)
	}
	// Published content is immutable even when another writer supplies bad bytes.
	if err := other.immutable(path, []byte("different")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("existing-content check: %v", err)
	}
	got, err = s.root.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("overwritten bytes: %q %v", got, err)
	}
}

func TestReviewOpenRemovesInterruptedTemporaryFiles(t *testing.T) {
	s, db, root := reviewStore(t)
	a, err := s.Create(t.Context(), Create{Name: "Synthetic", PersonID: "person", Layers: Layers{Core: "kept"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repoPath(a.ID), "objects", "ab")
	if err := privateDirs(s.root, dir); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, ".persona-tmp-"+newID())
	unrelated := filepath.Join(dir, "unrelated.tmp")
	for _, path := range []string{stale, unrelated} {
		if err := s.root.WriteFile(path, []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	recovered, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, err := s.root.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale temporary file remains: %v", err)
	}
	if _, err := s.root.Stat(unrelated); err != nil {
		t.Fatal("removed unrelated file", err)
	}
	prepared, err := recovered.Prepare(t.Context(), "after-recovery", a.ID)
	if err != nil || prepared.Layers.Core != "kept" {
		t.Fatalf("recovery: %+v %v", prepared, err)
	}
}

func TestReviewExpiryRemovesActionsAtomically(t *testing.T) {
	s, db, _ := reviewStore(t)
	a, err := s.Create(t.Context(), Create{Name: "Deleted", PersonID: "person"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(t.Context(), Create{Name: "Kept", PersonID: "person"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.Delete(t.Context(), a.ID, "person", now); err != nil {
		t.Fatal(err)
	}
	count := func(table, id string) int {
		t.Helper()
		var n int
		err := db.Read(t.Context(), func(tx *canon.Tx) error {
			return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table+" WHERE agent_id=?", id).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count("agent_actions", a.ID)
	if before == 0 {
		t.Fatal("fixture has no actions")
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "CREATE TRIGGER fail_expiry BEFORE DELETE ON agents BEGIN SELECT RAISE(ABORT,'injected'); END;")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Expire(t.Context(), now.Add(TrashRetention)); err == nil {
		t.Fatal("expiry should fail")
	}
	if got := count("agent_actions", a.ID); got != before {
		t.Fatal("actions escaped rollback", got)
	}
	if got := count("agent_tombstones", a.ID); got != 0 {
		t.Fatal("tombstone escaped rollback", got)
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DROP TRIGGER fail_expiry")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Expire(t.Context(), now.Add(TrashRetention)); err != nil {
		t.Fatal(err)
	}
	if got := count("agent_actions", a.ID); got != 0 {
		t.Errorf("expired agent retains %d action rows", got)
	}
	if got := count("agent_actions", b.ID); got != 1 {
		t.Errorf("other agent action rows: %d", got)
	}
	tomb, err := s.Tombstone(t.Context(), a.ID)
	if err != nil || !tomb.DeletedAt.Equal(now.Add(TrashRetention)) {
		t.Fatalf("tombstone: %+v %v", tomb, err)
	}
}
