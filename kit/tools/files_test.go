// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fileStore(t *testing.T) (*Files, string, string) {
	t.Helper()
	root, trash := t.TempDir(), t.TempDir()
	if err := os.Chmod(trash, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFiles(root, trash)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, root, trash
}

func TestFilesRevisionAndRecoverableReplacement(t *testing.T) {
	f, root, _ := fileStore(t)
	path := filepath.Join(root, "note")
	if err := os.WriteFile(path, []byte("original"), 0640); err != nil {
		t.Fatal(err)
	}
	read, err := f.Execute(t.Context(), FileRequest{Operation: "read", Path: "note"})
	if err != nil || string(read.Content) != "original" || read.Revision == "" {
		t.Fatalf("read: %+v %v", read, err)
	}
	if err := os.WriteFile(path, []byte("external"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Execute(t.Context(), FileRequest{Operation: "write", Path: "note", Revision: read.Revision, Content: []byte("overwrite")}); !errors.Is(err, ErrFileRevision) {
		t.Fatalf("stale write: %v", err)
	}
	read, err = f.Execute(t.Context(), FileRequest{Operation: "read", Path: "note"})
	if err != nil {
		t.Fatal(err)
	}
	written, err := f.Execute(t.Context(), FileRequest{Operation: "write", Path: "note", Revision: read.Revision, Content: []byte("edited")})
	if err != nil || len(written.Changes) != 1 || written.Changes[0].Version == "" {
		t.Fatalf("write: %+v %v", written, err)
	}
	restored, err := f.Execute(t.Context(), FileRequest{Operation: "restore", Path: "note", Revision: written.Revision, Version: written.Changes[0].Version})
	if err != nil || restored.Revision != read.Revision {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "external" {
		t.Fatalf("restored: %q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode: %v %v", info, err)
	}
}

func TestTrashRestoresIdenticalBytesAfterReopenAndRefusesOverwrite(t *testing.T) {
	f, root, trash := fileStore(t)
	data := []byte{0, 255, 1, '\r', '\n', 0, 128}
	if err := os.WriteFile(filepath.Join(root, "binary"), data, 0600); err != nil {
		t.Fatal(err)
	}
	read, err := f.Execute(t.Context(), FileRequest{Operation: "read", Path: "binary"})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := f.Execute(t.Context(), FileRequest{Operation: "delete", Path: "binary", Revision: read.Revision})
	if err != nil || len(deleted.Changes) != 1 || deleted.Revision != AbsentRevision {
		t.Fatalf("delete: %+v %v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(root, "binary")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	f.Close()
	f, err = OpenFiles(root, trash)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	request := FileRequest{Operation: "restore", Path: "binary", Revision: AbsentRevision, Version: deleted.Changes[0].Version}
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte("new owner bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Execute(t.Context(), request); !errors.Is(err, ErrFileRevision) {
		t.Fatalf("restore overwrote replacement: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "binary")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "binary"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("bytes changed: %v %v", got, err)
	}
}

func TestFilePatchStaleLaterFileLeavesAllFilesUnchanged(t *testing.T) {
	f, root, _ := fileStore(t)
	for name, content := range map[string]string{"a": "old\n", "b": "current\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	patch := "*** Begin Patch\n*** Update File: a\n@@\n-old\n+new\n*** Update File: b\n@@\n-stale\n+new\n*** End Patch"
	if _, err := f.Execute(t.Context(), FileRequest{Operation: "patch", Patch: patch}); !errors.Is(err, ErrPatchContext) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "a"))
	if err != nil || string(data) != "old\n" {
		t.Fatalf("partial stale patch: %q %v", data, err)
	}
}

func TestFileBoundaryRefusesTraversalSymlinksAndSpecialFiles(t *testing.T) {
	f, root, _ := fileStore(t)
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../private", outside, "link", "directory", ".git/config"} {
		if _, err := f.Execute(t.Context(), FileRequest{Operation: "read", Path: name}); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}

func TestFileCreationRequiresAbsenceAndPatchReceiptsCanUndo(t *testing.T) {
	f, root, _ := fileStore(t)
	if _, err := f.Execute(t.Context(), FileRequest{Operation: "write", Path: "created", Content: []byte("x")}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	created, err := f.Execute(t.Context(), FileRequest{Operation: "write", Path: "created", Revision: AbsentRevision, Content: []byte("old\n")})
	if err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: created\n*** Move to: moved\n@@\n-old\n+new\n*** Add File: nested/new\n+added\n*** End Patch"
	result, err := f.Execute(t.Context(), FileRequest{Operation: "patch", Patch: patch})
	if err != nil || len(result.Changes) != 3 {
		t.Fatalf("patch: %+v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "moved"))
	if err != nil || string(data) != "new\n" {
		t.Fatalf("move: %q %v", data, err)
	}
	for _, change := range result.Changes {
		if change.Path == "created" {
			if _, err := f.Execute(t.Context(), FileRequest{Operation: "restore", Path: change.Path, Revision: change.After, Version: change.Version}); err != nil {
				t.Fatal(err)
			}
		}
	}
	read, err := f.Execute(t.Context(), FileRequest{Operation: "read", Path: "created"})
	if err != nil || read.Revision != created.Revision {
		t.Fatalf("restored move: %+v %v", read, err)
	}
}

func TestCorruptRetainedVersionCannotReplaceCurrentBytes(t *testing.T) {
	f, root, trash := fileStore(t)
	if err := os.WriteFile(filepath.Join(root, "note"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := f.Execute(t.Context(), FileRequest{Operation: "read", Path: "note"})
	if err != nil {
		t.Fatal(err)
	}
	written, err := f.Execute(t.Context(), FileRequest{Operation: "write", Path: "note", Revision: read.Revision, Content: []byte("new")})
	if err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(trash, written.Changes[0].Version)
	data, err := os.ReadFile(version)
	if err != nil {
		t.Fatal(err)
	}
	// Both strings are valid base64: corruption must not silently change a
	// retained revision into different bytes with a newly computed digest.
	data = bytes.ReplaceAll(data, []byte("b2xk"), []byte("YmFk"))
	if err := os.WriteFile(version, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Execute(t.Context(), FileRequest{Operation: "restore", Path: "note", Revision: written.Revision, Version: written.Changes[0].Version}); err == nil {
		t.Fatal("corrupt snapshot restored")
	}
	data, err = os.ReadFile(filepath.Join(root, "note"))
	if err != nil || string(data) != "new" {
		t.Fatalf("current bytes changed: %q %v", data, err)
	}
}

func TestVersionFailureLeavesOriginalFile(t *testing.T) {
	f, root, trash := fileStore(t)
	if err := os.WriteFile(filepath.Join(root, "note"), []byte("only copy"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := f.Execute(t.Context(), FileRequest{Operation: "read", Path: "note"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(trash); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Execute(t.Context(), FileRequest{Operation: "delete", Path: "note", Revision: read.Revision}); err == nil {
		t.Fatal("delete succeeded without retained copy")
	}
	data, err := os.ReadFile(filepath.Join(root, "note"))
	if err != nil || string(data) != "only copy" {
		t.Fatalf("original lost: %q %v", data, err)
	}
}
