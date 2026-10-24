// SPDX-License-Identifier: Apache-2.0

//go:build unix

package proc

import (
	"os"
	"path/filepath"
	"testing"
)

// TestJoinOwnerDirectoryLeavesSymlinkTraversalToTheKernel proves a relative
// directory containing a symlink and ".." selects the symlink's parent. A
// lexical clean of that path selects the owner's directory instead.
func TestJoinOwnerDirectoryLeavesSymlinkTraversalToTheKernel(t *testing.T) {
	root := t.TempDir()
	owner := filepath.Join(root, "owner")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(owner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "child"), filepath.Join(owner, "link")); err != nil {
		t.Fatal(err)
	}

	dir := joinOwnerDirectory(owner, "link/..")
	t.Chdir(dir)
	got, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	wantInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, wantInfo) {
		t.Fatalf("working directory = %q, want the symlink parent %q", got, target)
	}
	if filepath.Clean(dir) == dir {
		t.Fatal("the directory was cleaned before the kernel followed the symlink")
	}
}
