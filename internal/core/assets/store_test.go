// SPDX-License-Identifier: Apache-2.0
package assets_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

var actor = assets.Actor{PersonID: "person-test", AgentID: "agent-test"}

func setup(t *testing.T) (*assets.Store, *canon.Store, string) {
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
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	s, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, db, root
}

func importAsset(t *testing.T, s *assets.Store, body string) assets.Asset {
	t.Helper()
	token, err := s.StageImport(t.Context(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Handover(t.Context(), token, "picture.bin", assets.User, actor)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func files(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestRenameKeepsIdentityHashAndRevision(t *testing.T) {
	s, _, _ := setup(t)
	a := importAsset(t, s, "original")
	renamed, err := s.Rename(t.Context(), a.ID, "renamed/picture.bin", actor)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != a.ID || renamed.Hash != a.Hash || renamed.Revision != a.Revision || renamed.ObservedPath != "renamed/picture.bin" {
		t.Fatalf("rename: %+v -> %+v", a, renamed)
	}
	got, err := s.Get(t.Context(), a.ID)
	if err != nil || got != renamed {
		t.Fatalf("persisted rename: %+v, %v", got, err)
	}
	data, err := s.Read(t.Context(), a.ID, a.Revision)
	if err != nil || string(data) != "original" {
		t.Fatalf("read renamed: %q, %v", data, err)
	}
}

func TestEditKeepsIDAndRetainsOldRevision(t *testing.T) {
	s, _, _ := setup(t)
	a := importAsset(t, s, "before")
	edited, err := s.Edit(t.Context(), a.ID, a.Revision, strings.NewReader("after"), actor)
	if err != nil {
		t.Fatal(err)
	}
	if edited.ID != a.ID || edited.Hash == a.Hash || edited.Revision != a.Revision+1 {
		t.Fatalf("edit: %+v -> %+v", a, edited)
	}
	for rev, want := range map[int64]string{a.Revision: "before", edited.Revision: "after"} {
		data, err := s.Read(t.Context(), a.ID, rev)
		if err != nil || string(data) != want {
			t.Fatalf("revision %d: %q, %v", rev, data, err)
		}
	}
	if _, err := s.Edit(t.Context(), a.ID, a.Revision, strings.NewReader("stale"), actor); !errors.Is(err, assets.ErrStaleRevision) {
		t.Fatalf("stale edit: %v", err)
	}
	same, err := s.Edit(t.Context(), a.ID, edited.Revision, strings.NewReader("after"), actor)
	if err != nil || same != edited {
		t.Fatalf("no-op edit: %+v, %v", same, err)
	}
}

func TestIdenticalContentGetsIndependentIDs(t *testing.T) {
	s, _, _ := setup(t)
	a, b := importAsset(t, s, "same"), importAsset(t, s, "same")
	if a.ID == b.ID || a.Hash != b.Hash || a.Path == b.Path {
		t.Fatalf("identity: %+v, %+v", a, b)
	}
}

func TestImportStaysInStagingUntilHandover(t *testing.T) {
	s, db, root := setup(t)
	before := files(t, root)
	token, err := s.StageImport(t.Context(), strings.NewReader("incoming"))
	if err != nil {
		t.Fatal(err)
	}
	after := files(t, root)
	for _, path := range after {
		found := false
		for _, old := range before {
			if old == path {
				found = true
			}
		}
		if !found && !strings.HasPrefix(path, "staging/import/") {
			t.Fatalf("premature write: %s", path)
		}
	}
	var count int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM assets").Scan(&count)
	}); err != nil || count != 0 {
		t.Fatalf("premature canon record: %d, %v", count, err)
	}
	a, err := s.Handover(t.Context(), token, "incoming.bin", assets.User, actor)
	if err != nil {
		t.Fatal(err)
	}
	if a.Actor != actor || a.Revision != 1 || !strings.HasPrefix(a.Path, "materials/user/"+a.ID+"/1/") {
		t.Fatalf("handover: %+v", a)
	}
	if got := files(t, filepath.Join(root, "staging/import")); len(got) != 0 {
		t.Fatalf("staged copies: %v", got)
	}
	if _, err := s.Handover(t.Context(), token, "again", assets.User, actor); err == nil {
		t.Fatal("reused consumed import")
	}
}

func TestExportRecordsDeliveryAndRemovesStaging(t *testing.T) {
	s, db, root := setup(t)
	a := importAsset(t, s, "outgoing")
	exported, err := s.Export(t.Context(), a.ID, a.Revision, "chosen-destination", actor, func(ctx context.Context, body io.Reader) error {
		if got := files(t, filepath.Join(root, "staging/export")); len(got) != 1 {
			t.Fatalf("export staging: %v", got)
		}
		var count int
		if err := db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT count(*) FROM asset_exports").Scan(&count)
		}); err != nil || count != 1 {
			t.Fatalf("delivery not recorded first: %d %v", count, err)
		}
		data, err := io.ReadAll(body)
		if err != nil || string(data) != "outgoing" {
			t.Fatalf("export bytes: %q, %v", data, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if exported.AssetID != a.ID || exported.Revision != a.Revision || exported.Hash != a.Hash || exported.State != assets.Delivered || exported.Destination != "chosen-destination" || exported.Actor != actor {
		t.Fatalf("export: %+v", exported)
	}
	if got := files(t, filepath.Join(root, "staging/export")); len(got) != 0 {
		t.Fatalf("export leftovers: %v", got)
	}
	records, err := s.Exports(t.Context(), a.ID)
	if err != nil || len(records) != 1 || records[0] != exported {
		t.Fatalf("export record: %+v, %v", records, err)
	}
}

func TestBackupDetectsHashMismatchInCurrentAndSavedBytes(t *testing.T) {
	for _, corruptOld := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "saved"}[corruptOld], func(t *testing.T) {
			s, _, root := setup(t)
			a := importAsset(t, s, "first")
			edited, err := s.Edit(t.Context(), a.ID, 1, strings.NewReader("second"), actor)
			if err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			manifest, err := s.Backup(t.Context(), destination)
			if err != nil || len(manifest) != 2 {
				t.Fatalf("backup: %+v, %v", manifest, err)
			}
			if err := assets.VerifyBackup(t.Context(), destination, manifest); err != nil {
				t.Fatal(err)
			}
			path := edited.Path
			if corruptOld {
				path = a.Path
			}
			original, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				t.Fatal(err)
			}
			bad := strings.Repeat("!", len(original)) // same size: the digest must be checked
			if err := os.WriteFile(filepath.Join(destination, path), []byte(bad), 0600); err != nil {
				t.Fatal(err)
			}
			if err := assets.VerifyBackup(t.Context(), destination, manifest); !errors.Is(err, assets.ErrHashMismatch) {
				t.Fatalf("backup corruption: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, path), []byte(bad), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Backup(t.Context(), t.TempDir()); !errors.Is(err, assets.ErrHashMismatch) {
				t.Fatalf("source corruption: %v", err)
			}
		})
	}
}

func TestTrashHidesAllRevisionsAndRestoresIdentity(t *testing.T) {
	s, _, _ := setup(t)
	a := importAsset(t, s, "recoverable")
	if err := s.Trash(t.Context(), a.ID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(t.Context(), a.ID); !errors.Is(err, assets.ErrTrashed) {
		t.Fatalf("get trash: %v", err)
	}
	if _, err := s.Read(t.Context(), a.ID, 1); !errors.Is(err, assets.ErrTrashed) {
		t.Fatalf("read trash: %v", err)
	}
	called := false
	if _, err := s.Export(t.Context(), a.ID, 1, "destination", actor, func(context.Context, io.Reader) error { called = true; return nil }); !errors.Is(err, assets.ErrTrashed) || called {
		t.Fatalf("export trash: %v, called=%v", err, called)
	}
	trash, err := s.ListTrash(t.Context())
	if err != nil || len(trash) != 1 || trash[0].ID != a.ID || trash[0].TrashedAt == 0 {
		t.Fatalf("trash: %+v, %v", trash, err)
	}
	manifest, err := s.Backup(t.Context(), t.TempDir())
	if err != nil || len(manifest) != 1 {
		t.Fatalf("trash backup: %+v, %v", manifest, err)
	}
	restored, err := s.Restore(t.Context(), a.ID, actor)
	if err != nil || restored != a {
		t.Fatalf("restore: %+v, %v", restored, err)
	}
}

func TestMetadataSurvivesReopen(t *testing.T) {
	s, db, root := setup(t)
	a := importAsset(t, s, "durable")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(t.Context(), a.ID)
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("reopen: %+v, %v", got, err)
	}
}
