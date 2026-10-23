// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestClosesDescriptorsBetweenEntries(t *testing.T) {
	f, root, _ := fileStore(t)
	for i := 0; i < 900; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprint(i)), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.Execute(t.Context(), FileRequest{Operation: "manifest"})
	if err != nil || len(result.Manifest) != 900 {
		t.Fatalf("manifest: entries=%d error=%v", len(result.Manifest), err)
	}
}

func TestManifestRecordsCreatedChangedAndDeletedPaths(t *testing.T) {
	f, root, _ := fileStore(t)
	for _, name := range []string{"changed", "removed"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := f.Execute(t.Context(), FileRequest{Operation: "manifest"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "changed"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "removed")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "created"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := f.Execute(t.Context(), FileRequest{Operation: "manifest"})
	if err != nil {
		t.Fatal(err)
	}
	changes := ManifestDiff(before.Manifest, after.Manifest)
	if len(changes) != 3 || before.Revision == after.Revision {
		t.Fatalf("changes: %+v", changes)
	}
	for _, change := range changes {
		if change.Before == change.After {
			t.Fatalf("unchanged receipt: %+v", change)
		}
		if change.Path == "removed" && change.After != AbsentRevision {
			t.Fatal(change)
		}
		if change.Path == "created" && change.Before != AbsentRevision {
			t.Fatal(change)
		}
	}
}

func TestManifestIncludesUserDirectoriesWithRecoveryPrefix(t *testing.T) {
	f, root, _ := fileStore(t)
	name := "notes/.lina-recovery-drafts/plan.txt"
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := f.Execute(t.Context(), FileRequest{Operation: "manifest"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := f.Execute(t.Context(), FileRequest{Operation: "manifest"})
	if err != nil {
		t.Fatal(err)
	}
	changes := ManifestDiff(before.Manifest, after.Manifest)
	if len(changes) != 1 || changes[0].Path != name || changes[0].Before == changes[0].After || before.Revision == after.Revision {
		t.Fatalf("user writes were omitted from receipt: %+v", changes)
	}
}

func TestManifestRetainedCapturesDoNotExhaustEntryLimit(t *testing.T) {
	f, root, versions := fileStore(t)
	created, err := f.Execute(t.Context(), FileRequest{Operation: "write", Path: "note", Revision: AbsentRevision, Content: []byte("v1")})
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := f.Execute(t.Context(), FileRequest{Operation: "write", Path: "note", Revision: created.Revision, Content: []byte("v2")})
	if err != nil || len(replaced.Changes) != 1 || replaced.Changes[0].Recovery == "" {
		t.Fatalf("replace: %+v %v", replaced, err)
	}
	var lastRecovery string
	for i := 0; i < 5005; i++ {
		slot := fmt.Sprintf("%sbulk-%d", recoveryPrefix, i)
		recovery := slot + "/file"
		if err := os.Mkdir(filepath.Join(root, slot), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, slot, "file"), []byte("captured"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(versions, recoveryMarker(slot)), nil, 0600); err != nil {
			t.Fatal(err)
		}
		lastRecovery = recovery
	}
	f.Close()
	reopened, err := OpenFiles(root, versions)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	before, err := reopened.Execute(t.Context(), FileRequest{Operation: "manifest"})
	if err != nil {
		t.Fatalf("manifest failed with retained captures: %v", err)
	}
	if _, ok := before.Manifest["note"]; !ok {
		t.Fatalf("missing current file from manifest: %+v", before.Manifest)
	}
	if _, ok := before.Manifest[replaced.Changes[0].Recovery]; !ok {
		t.Fatalf("missing replaced capture from manifest: %+v", before.Manifest)
	}
	if _, ok := before.Manifest[lastRecovery]; !ok {
		t.Fatalf("missing retained capture from manifest: %+v", before.Manifest)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(lastRecovery)), []byte("open-fd-write"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := reopened.Execute(t.Context(), FileRequest{Operation: "manifest"})
	if err != nil {
		t.Fatal(err)
	}
	changes := ManifestDiff(before.Manifest, after.Manifest)
	if len(changes) != 1 || changes[0].Path != lastRecovery || changes[0].Before == changes[0].After {
		t.Fatalf("capture modification omitted from diff: %+v", changes)
	}
	userDir := filepath.Join(root, "notes", ".lina-recovery-user")
	if err := os.MkdirAll(userDir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9996; i++ {
		if err := os.WriteFile(filepath.Join(userDir, fmt.Sprint(i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reopened.Execute(t.Context(), FileRequest{Operation: "manifest"}); err != nil {
		t.Fatalf("manifest at exact 10,000 non-capture entry boundary failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "overflow"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Execute(t.Context(), FileRequest{Operation: "manifest"}); err != ErrFileSize {
		t.Fatalf("expected ErrFileSize for unrecorded recovery-like user entries, got %v", err)
	}
}
