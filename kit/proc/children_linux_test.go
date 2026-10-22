// SPDX-License-Identifier: Apache-2.0

package proc

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestChildrenWithoutCheckpointRestoreInterface(t *testing.T) {
	root := t.TempDir()
	// Ordinary /proc/<pid>/stat is available without CONFIG_CHECKPOINT_RESTORE.
	// Threads exist, but the optional per-thread children files do not.
	if err := os.MkdirAll(filepath.Join(root, "self/task/100"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ pid, stat string }{
		{"100", "100 (supervisor) S 1 100 100 0"},
		{"200", "200 (worker (with) spaces) S 100 200 200 0"},
		{"300", "300 (adopted orphan) Z 100 300 300 0"},
		{"400", "400 (other owner) S 1 400 400 0"},
		{"500", "500 (not yet adopted) S 200 500 500 0"},
	} {
		path := filepath.Join(root, entry.pid)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "stat"), []byte(entry.stat), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "999"), 0700); err != nil {
		t.Fatal(err)
	} // exited during scan
	got, err := childrenIn(root, 100)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{200, 300}; !reflect.DeepEqual(got, want) {
		t.Fatalf("children = %v, want %v", got, want)
	}
}
