// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ncruces/go-sqlite3"
)

func TestCoreRejectsCorruptEngineBeforeBoot(t *testing.T) {
	original := sqlite3.Binary
	sqlite3.Binary = []byte("corrupt engine")
	t.Cleanup(func() { sqlite3.Binary = original })
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if code := runCore([]string{"--state-root", root, "--socket", filepath.Join(root, "client.sock")}); code != 78 {
		t.Fatalf("exit=%d, want configuration error", code)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("boot changed state before verifying the engine: %v %v", entries, err)
	}
}
