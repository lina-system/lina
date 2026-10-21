// SPDX-License-Identifier: Apache-2.0
package skills

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func document(body string) string {
	return "---\nname: example\ndescription: Use for synthetic procedures.\n---\n" + body
}
func fixture(t *testing.T, builtins fs.FS) (*Store, *canon.Store, string) {
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
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: Schema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	var source BuiltinFS
	if builtins != nil {
		if versioned, ok := builtins.(BuiltinFS); ok {
			source = versioned
		} else {
			source = versionedFS{FS: builtins, revision: "fixture"}
		}
	}
	s, err := Open(root, db, source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, db, root
}
func apply(t *testing.T, s *Store, rev int64, body string) Applied {
	t.Helper()
	out, err := s.Patch(t.Context(), Patch{ID: "example", Expected: rev, Files: Bundle{"SKILL.md": {Text: document(body)}}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestAdoptUndoReopenAndPin(t *testing.T) {
	s, db, root := fixture(t, nil)
	a := apply(t, s, 0, "Check the input.\n")
	if a.Version.Revision != 1 || a.Notice == "" || strings.ContainsAny(a.Notice, "\r\n") {
		t.Fatal(a)
	}
	pinned, err := s.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b := apply(t, s, 1, "Check the input and output.\n")
	old, err := s.Read(t.Context(), pinned[0])
	if err != nil || old.Files["SKILL.md"].Text != document("Check the input.\n") {
		t.Fatalf("pin: %+v %v", old, err)
	}
	u, err := s.Undo(t.Context(), b.Undo)
	if err != nil {
		t.Fatal(err)
	}
	if u.Version.Revision != 3 || u.Version.Commit == a.Version.Commit {
		t.Fatal(u)
	}
	restored, err := s.Read(t.Context(), u.Version)
	if err != nil || restored.Files["SKILL.md"].Text != old.Files["SKILL.md"].Text {
		t.Fatalf("undo: %+v %v", restored, err)
	}
	reopened, err := Open(root, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, err = reopened.Patch(t.Context(), Patch{ID: "example", Expected: 3, Files: Bundle{"SKILL.md": {Text: document("Check the input and output.\n")}}})
	if !errors.Is(err, ErrUndone) {
		t.Fatalf("undone content accepted: %v", err)
	}
	if _, err = s.Undo(t.Context(), b.Undo); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale undo: %v", err)
	}
	cmd := exec.Command("git", "--git-dir", filepath.Join(root, "versions/skills"), "fsck", "--full")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fsck: %s %v", out, err)
	}
	cmd = exec.Command("git", "--git-dir", filepath.Join(root, "versions/skills"), "show", u.Version.Commit+":example/SKILL.md")
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != document("Check the input.\n") {
		t.Fatalf("git content: %s %v", out, err)
	}
}
func TestUndoCreationHidesSkillAndPreservesVersion(t *testing.T) {
	s, _, _ := fixture(t, nil)
	a := apply(t, s, 0, "Procedure.\n")
	u, err := s.Undo(t.Context(), a.Undo)
	if err != nil || u.Version.Revision != 2 {
		t.Fatalf("undo: %+v %v", u, err)
	}
	entries, err := s.Catalog(t.Context())
	if err != nil || len(entries) != 0 {
		t.Fatalf("catalog: %+v %v", entries, err)
	}
	if _, err = s.Read(t.Context(), a.Version); err != nil {
		t.Fatal(err)
	}
}
func TestExecutableAndPermissionSurfacesNeverAdopt(t *testing.T) {
	for name, files := range map[string]Bundle{
		"script":     {"SKILL.md": {Text: document("Run helper.")}, "run.sh": {Text: "echo hi"}},
		"mode":       {"SKILL.md": {Text: document("Run helper."), Executable: true}},
		"permission": {"SKILL.md": {Text: strings.Replace(document("Step."), "description:", "permissions: network\ndescription:", 1)}},
		"tool":       {"SKILL.md": {Text: strings.Replace(document("Step."), "description:", "tools: shell\ndescription:", 1)}},
		"connection": {"SKILL.md": {Text: strings.Replace(document("Step."), "description:", "connection: external\ndescription:", 1)}},
		"traversal":  {"../SKILL.md": {Text: document("Step.")}},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := fixture(t, nil)
			out, err := s.Patch(t.Context(), Patch{ID: "example", Files: files})
			if err == nil || out.Notice != "" {
				t.Fatalf("adopted: %+v %v", out, err)
			}
			entries, _ := s.Catalog(t.Context())
			if len(entries) != 0 {
				t.Fatal(entries)
			}
		})
	}
	builtin := fstest.MapFS{"example/SKILL.md": {Data: []byte(document("Old."))}, "example/helper.py": {Data: []byte("print('example')")}}
	s, _, _ := fixture(t, builtin)
	out, err := s.Patch(t.Context(), Patch{ID: "example", Files: Bundle{"SKILL.md": {Text: document("New.")}}})
	if !errors.Is(err, ErrAcceptance) || out.Notice != "" {
		t.Fatalf("existing code adopted: %+v %v", out, err)
	}
}
func TestIndexDoesNotReadVersionBodies(t *testing.T) {
	s, _, root := fixture(t, nil)
	a := apply(t, s, 0, "Only load on demand.")
	// Index still works when immutable objects are unavailable; an actual read fails.
	if err := os.Rename(filepath.Join(root, "versions/skills/objects"), filepath.Join(root, "objects-away")); err != nil {
		t.Fatal(err)
	}
	entries, err := s.Catalog(t.Context())
	if err != nil || len(entries) != 1 || entries[0].Name != "example" || entries[0].Description == "" {
		t.Fatalf("index: %+v %v", entries, err)
	}
	if _, err = s.Read(t.Context(), a.Version); err == nil {
		t.Fatal("body read ignored missing objects")
	}
}
func TestFailedCommitPublishesNoNoticeOrVersion(t *testing.T) {
	s, db, _ := fixture(t, nil)
	a := apply(t, s, 0, "Original.")
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER fail_skill BEFORE INSERT ON skill_versions WHEN NEW.revision > 1 BEGIN SELECT RAISE(ABORT,'injected'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Patch(t.Context(), Patch{ID: "example", Expected: 1, Files: Bundle{"SKILL.md": {Text: document("Changed.")}}})
	if err == nil || out.Notice != "" {
		t.Fatalf("failed commit: %+v %v", out, err)
	}
	entries, _ := s.Catalog(t.Context())
	if len(entries) != 1 || entries[0] != a.Version {
		t.Fatal(entries)
	}
}

func TestUndoCannotReintroduceAnUndoneHash(t *testing.T) {
	s, _, _ := fixture(t, nil)
	apply(t, s, 0, "First.")
	second := apply(t, s, 1, "Second.")
	restored, err := s.Undo(t.Context(), second.Undo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Undo(t.Context(), restored.Undo); !errors.Is(err, ErrUndone) {
		t.Fatalf("undone content reintroduced: %v", err)
	}
}
func TestPathCannotBeBothFileAndDirectory(t *testing.T) {
	s, _, _ := fixture(t, nil)
	out, err := s.Patch(t.Context(), Patch{ID: "example", Files: Bundle{"SKILL.md": {Text: document("Instructions.")}, "notes.md": {Text: "Text"}, "notes.md/more.md": {Text: "More"}}})
	if !errors.Is(err, ErrInvalid) || out.Notice != "" {
		t.Fatalf("invalid Git tree adopted: %+v %v", out, err)
	}
}
func TestConcurrentPatchesHaveOneWinner(t *testing.T) {
	s, db, root := fixture(t, nil)
	apply(t, s, 0, "Original.")
	other, err := Open(root, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	done := make(chan error, 2)
	for i, store := range []*Store{s, other} {
		go func() {
			<-start
			_, err := store.Patch(t.Context(), Patch{ID: "example", Expected: 1, Files: Bundle{"SKILL.md": {Text: document(strings.Repeat("Changed.", i+1))}}})
			done <- err
		}()
	}
	close(start)
	first, second := <-done, <-done
	if !(first == nil && errors.Is(second, ErrConflict) || second == nil && errors.Is(first, ErrConflict)) {
		t.Fatalf("writers: %v %v", first, second)
	}
}
func TestFailedFileWriteDoesNotPublishAndCanRecover(t *testing.T) {
	s, _, _ := fixture(t, nil)
	a := apply(t, s, 0, "Original.")
	s.writeFile = func(*os.File, []byte) (int, error) { return 0, errors.New("injected file failure") }
	out, err := s.Patch(t.Context(), Patch{ID: "example", Expected: 1, Files: Bundle{"SKILL.md": {Text: document("Changed.")}}})
	if err == nil || out.Notice != "" {
		t.Fatalf("file failure adopted: %+v %v", out, err)
	}
	entries, _ := s.Catalog(t.Context())
	if len(entries) != 1 || entries[0] != a.Version {
		t.Fatal(entries)
	}
	s.writeFile = nil
	apply(t, s, 1, "Changed.")
}
func TestGitSnapshotPreservesOtherSkillAndUndoBuiltin(t *testing.T) {
	builtin := fstest.MapFS{"example/SKILL.md": {Data: []byte(document("Built in."))}}
	s, _, root := fixture(t, builtin)
	a := apply(t, s, 0, "Updated.")
	_, err := s.Patch(t.Context(), Patch{ID: "second", Files: Bundle{"SKILL.md": {Text: strings.Replace(document("Independent."), "name: example", "name: second", 1)}}})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := s.Undo(t.Context(), a.Undo)
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.Read(t.Context(), restored.Version)
	if err != nil || body.Files["SKILL.md"].Text != document("Built in.") {
		t.Fatalf("builtin undo: %+v %v", body, err)
	}
	cmd := exec.Command("git", "--git-dir", filepath.Join(root, "versions/skills"), "ls-tree", restored.Version.Commit)
	data, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(data), "example") || !strings.Contains(string(data), "second") {
		t.Fatalf("lost other skill: %s %v", data, err)
	}
}

func TestCommittedVersionAndSuppressionSurviveDatabaseReopen(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: Schema})
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: migrations}
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(root, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, s, 0, "Before.")
	second := apply(t, s, 1, "After.")
	restored, err := s.Undo(t.Context(), second.Undo)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	db, err = canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	s, err = Open(root, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	body, err := s.Read(t.Context(), restored.Version)
	if err != nil || body.Files["SKILL.md"].Text != document("Before.") {
		t.Fatalf("reopen: %+v %v", body, err)
	}
	_, err = s.Patch(t.Context(), Patch{ID: "example", Expected: 3, Files: Bundle{"SKILL.md": {Text: document("After.")}}})
	if !errors.Is(err, ErrUndone) {
		t.Fatalf("suppression lost: %v", err)
	}
}

type headerOnlyFS struct {
	fs.FS
	boundary int
}

func (f headerOnlyFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || !strings.HasSuffix(name, "/SKILL.md") {
		return file, err
	}
	return &headerOnlyFile{File: file, remaining: f.boundary}, nil
}

type headerOnlyFile struct {
	fs.File
	remaining int
}

func (f *headerOnlyFile) Read(p []byte) (int, error) {
	if len(p) > f.remaining {
		return 0, errors.New("body loaded during index construction")
	}
	n, err := f.File.Read(p)
	f.remaining -= n
	return n, err
}
func TestBuiltinIndexReadsOnlyFrontMatter(t *testing.T) {
	text := document("Never load this body during indexing.")
	boundary := len(text) - len("Never load this body during indexing.")
	s, _, _ := fixture(t, headerOnlyFS{FS: fstest.MapFS{"example/SKILL.md": {Data: []byte(text)}}, boundary: boundary})
	entries, err := s.Catalog(t.Context())
	if err != nil || len(entries) != 1 {
		t.Fatalf("catalog: %+v %v", entries, err)
	}
}

func TestNameMustIdentifyItsFolder(t *testing.T) {
	s, _, _ := fixture(t, nil)
	_, err := s.Patch(t.Context(), Patch{ID: "different", Files: Bundle{"SKILL.md": {Text: document("Instructions.")}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("name alias accepted: %v", err)
	}
}

// Each compiled generation supplies an immutable identity without loading bodies.
type versionedFS struct {
	fs.FS
	revision string
}

func (f versionedFS) SkillRevision() string { return f.revision }
func TestBuiltinHandleCannotReadAnotherGeneration(t *testing.T) {
	original := versionedFS{FS: fstest.MapFS{"example/SKILL.md": {Data: []byte(document("Release A."))}}, revision: "release-a"}
	s, db, root := fixture(t, original)
	entries, err := s.Catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	updated := versionedFS{FS: fstest.MapFS{"example/SKILL.md": {Data: []byte(document("Release B."))}}, revision: "release-b"}
	next, err := Open(root, db, updated)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if body, err := next.Read(t.Context(), entries[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old handle resolved to new text: %+v %v", body, err)
	}
}

func TestNULPathCannotPublishAnUnreadableVersion(t *testing.T) {
	s, _, _ := fixture(t, nil)
	out, err := s.Patch(t.Context(), Patch{ID: "example", Files: Bundle{"SKILL.md": {Text: document("Instructions.")}, "notes\x00.md": {Text: "Notes."}}})
	if !errors.Is(err, ErrInvalid) || out.Notice != "" {
		t.Fatalf("invalid Git path adopted: %+v %v", out, err)
	}
}
func TestBuiltinNoOpCannotPoisonUndo(t *testing.T) {
	s, _, _ := fixture(t, fstest.MapFS{"example/SKILL.md": {Data: []byte(document("Original."))}})
	out, err := s.Patch(t.Context(), Patch{ID: "example", Files: Bundle{"SKILL.md": {Text: document("Original.")}}})
	if !errors.Is(err, ErrConflict) || out.Notice != "" {
		t.Fatalf("no-op published: %+v %v", out, err)
	}
	changed := apply(t, s, 0, "Changed.")
	restored, err := s.Undo(t.Context(), changed.Undo)
	if err != nil {
		t.Fatal(err)
	}
	body, err := s.Read(t.Context(), restored.Version)
	if err != nil || body.Files["SKILL.md"].Text != document("Original.") {
		t.Fatalf("undo: %+v %v", body, err)
	}
}
