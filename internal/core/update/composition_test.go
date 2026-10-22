// SPDX-License-Identifier: Apache-2.0
package update

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestRollbackRemovesNewAdapterWhenPreviousHadNone(t *testing.T) {
	root := t.TempDir()
	previous := filepath.Join(root, "system/updates/previous-attempt")
	for _, dir := range []string{"system/bin", "system/updates/previous-attempt-next/bin", "system/updates/previous-attempt-next/adapters"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "system/bin/lina"), []byte("old"), 0700)
	os.WriteFile(filepath.Join(previous+"-next/bin/lina"), []byte("new"), 0700)
	os.WriteFile(filepath.Join(previous+"-next/adapters/package"), []byte("new adapter"), 0600)
	r := ctlfiles.UpdateRecord{Root: root, Previous: previous}
	if err := Swap(r, nil); err != nil {
		t.Fatal(err)
	}
	if err := RestoreComposition(r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "system/adapters")); !os.IsNotExist(err) {
		t.Fatal("rollback kept the new adapter beside the old binary")
	}
	if err := RestoreComposition(r); err != nil {
		t.Fatal("rollback is not resumable", err)
	}
}
