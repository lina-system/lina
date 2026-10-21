// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"os"
	"path/filepath"
	"testing"
)

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
