// SPDX-License-Identifier: Apache-2.0
package assets_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
)

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type callbackReader struct {
	once   sync.Once
	before func()
	io.Reader
}

func (r *callbackReader) Read(p []byte) (int, error) { r.once.Do(r.before); return r.Reader.Read(p) }

func TestStaleEditCannotRemoveNewerCommittedBytes(t *testing.T) {
	t.Parallel()
	s, _, root := setup(t)
	a := importAsset(t, s, "original")
	newPath := filepath.Join("materials/user", a.ID, "2/content")
	body := &callbackReader{Reader: strings.NewReader("stale edit"), before: func() {
		// A successor publishes revision 2 while the old caller is still reading
		// its input. The old writer must lose at the fence without harming it.
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(newPath)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, newPath), []byte("newer"), 0600); err != nil {
			t.Fatal(err)
		}
		db, err := canon.OpenSQLite(t.Context(), filepath.Join(root, "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.ExecContext(t.Context(), "INSERT INTO asset_revisions(asset_id,revision,path,hash,size,person_id,agent_id) VALUES (?,2,?,?,5,'person','agent')", a.ID, newPath, fmt.Sprintf("%x", sha256.Sum256([]byte("newer")))); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), "UPDATE assets SET revision=2 WHERE id=?", a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), "UPDATE canon_meta SET epoch='2'"); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := s.Edit(t.Context(), a.ID, 1, body, actor); !errors.Is(err, canon.ErrStaleEpoch) {
		t.Fatalf("stale edit: %v", err)
	}
	got, err := s.Read(t.Context(), a.ID, 2)
	if err != nil || string(got) != "newer" {
		t.Fatalf("successor bytes changed: %q %v", got, err)
	}
}

func TestFailedOrCancelledImportRemovesPartialBytes(t *testing.T) {
	t.Parallel()
	s, _, root := setup(t)
	if _, err := s.StageImport(t.Context(), io.MultiReader(strings.NewReader("partial"), brokenReader{})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read failure: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.StageImport(ctx, strings.NewReader("cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if got := files(t, filepath.Join(root, "staging")); len(got) != 0 {
		t.Fatalf("leftovers: %v", got)
	}
}

func TestFailedDeliveryIsRecordedAsUnknownAndNotRetried(t *testing.T) {
	t.Parallel()
	s, db, root := setup(t)
	a := importAsset(t, s, "delivery")
	expected := errors.New("delivery interrupted")
	record, err := s.Export(t.Context(), a.ID, 1, "destination", actor, func(ctx context.Context, body io.Reader) error {
		if _, err := s.Get(ctx, a.ID); err != nil {
			t.Fatal(err)
		} // no store lock over effects
		var one [1]byte
		if _, err := body.Read(one[:]); err != nil {
			t.Fatal(err)
		}
		return expected
	})
	if !errors.Is(err, expected) || record.State != assets.Unknown {
		t.Fatalf("failed export: %+v %v", record, err)
	}
	if got := files(t, filepath.Join(root, "staging/export")); len(got) != 0 {
		t.Fatalf("export leftovers: %v", got)
	}
	// Model a process exit before the cleanup following a delivery.
	if err := os.WriteFile(filepath.Join(root, "staging/export", record.ID), []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := files(t, filepath.Join(root, "staging/export")); len(got) != 0 {
		t.Fatalf("restart leftovers: %v", got)
	}
	records, err := reopened.Exports(t.Context(), a.ID)
	if err != nil || len(records) != 1 || records[0] != record {
		t.Fatalf("restart export records: %+v, %v", records, err)
	}
}

func TestFencedHandoverKeepsStageAndRollsBackMaterial(t *testing.T) {
	t.Parallel()
	s, db, root := setup(t)
	token, err := s.StageImport(t.Context(), strings.NewReader("not owned yet"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := canon.OpenSQLite(t.Context(), filepath.Join(root, "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.ExecContext(t.Context(), "UPDATE canon_meta SET epoch='2'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Handover(t.Context(), token, "refused", assets.User, actor); !errors.Is(err, canon.ErrStaleEpoch) {
		t.Fatalf("fence: %v", err)
	}
	if got := files(t, filepath.Join(root, "materials")); len(got) != 0 {
		t.Fatalf("uncommitted material: %v", got)
	}
	if got := files(t, filepath.Join(root, "staging/import")); len(got) != 1 || got[0] != token {
		t.Fatalf("lost import: %v", got)
	}
	var count int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM assets").Scan(&count)
	}); err != nil || count != 0 {
		t.Fatalf("fenced metadata: %d %v", count, err)
	}
}

func TestPathsCannotEscapeManagedAreas(t *testing.T) {
	t.Parallel()
	s, _, root := setup(t)
	if _, err := s.Handover(t.Context(), "../../outside", "name", assets.User, actor); !errors.Is(err, assets.ErrInvalid) {
		t.Fatalf("token traversal: %v", err)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "staging"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "staging/import")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StageImport(t.Context(), strings.NewReader("escape")); err == nil {
		t.Fatal("followed staging symlink")
	}
	if got := files(t, outside); len(got) != 0 {
		t.Fatalf("outside write: %v", got)
	}
}

func TestLinaMaterialsAndPendingImportsSurviveReopen(t *testing.T) {
	t.Parallel()
	s, db, root := setup(t)
	token, err := s.StageImport(t.Context(), strings.NewReader("generated"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	a, err := reopened.Handover(t.Context(), token, "output.bin", assets.Lina, actor)
	if err != nil || !strings.HasPrefix(a.Path, "materials/lina/") {
		t.Fatalf("produced material: %+v %v", a, err)
	}
}

func TestCorruptExportDoesNotDeliverOrLeaveStage(t *testing.T) {
	t.Parallel()
	s, _, root := setup(t)
	a := importAsset(t, s, "good")
	if err := os.WriteFile(filepath.Join(root, a.Path), []byte("evil"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if _, err := s.Export(t.Context(), a.ID, 1, "destination", actor, func(context.Context, io.Reader) error { called = true; return nil }); !errors.Is(err, assets.ErrHashMismatch) || called {
		t.Fatalf("corrupt export: %v called=%v", err, called)
	}
	if got := files(t, filepath.Join(root, "staging/export")); len(got) != 0 {
		t.Fatalf("corrupt leftovers: %v", got)
	}
}

func TestReopenPreservesEpisodeExportDirectories(t *testing.T) {
	t.Parallel()
	store, db, root := setup(t)
	for _, name := range []string{"episode-SYNTHETIC", ".episode-INCOMPLETE"} {
		path := filepath.Join(root, "staging/export", name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "episodes.jsonl"), []byte("synthetic episode\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	abandoned := filepath.Join(root, "staging/export", "0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(abandoned, []byte("interrupted asset export"), 0600); err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatalf("episode export prevents Core restart: %v", err)
	}
	defer reopened.Close()
	if _, err := os.Stat(abandoned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned asset export retained: %v", err)
	}
	for _, name := range []string{"episode-SYNTHETIC", ".episode-INCOMPLETE"} {
		data, err := os.ReadFile(filepath.Join(root, "staging/export", name, "episodes.jsonl"))
		if err != nil || string(data) != "synthetic episode\n" {
			t.Fatalf("episode bytes changed during asset cleanup: %s %v", data, err)
		}
	}
}

func TestExportCleanupRefusesUnexpectedEntries(t *testing.T) {
	t.Parallel()
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			store, db, root := setup(t)
			staging := filepath.Join(root, "staging/export")
			if err := os.MkdirAll(staging, 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			if symlink {
				err = os.Symlink(t.TempDir(), filepath.Join(staging, "episode-SYMLINK"))
			} else {
				err = os.Mkdir(filepath.Join(staging, "unexpected"), 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			reopened, err := assets.Open(t.Context(), root, db)
			if reopened != nil {
				reopened.Close()
			}
			if !errors.Is(err, assets.ErrInvalid) {
				t.Fatalf("unexpected export staging accepted: %v", err)
			}
		})
	}
}
