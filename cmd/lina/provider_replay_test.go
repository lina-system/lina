// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProviderReplayOutputRefusesRepositoryAndSymlinks(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: somewhere"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(repo, "out"), filepath.Join(link, "out")} {
		if _, err := providerOutput(path); err == nil {
			t.Fatal("accepted repository output")
		}
	}
	dir, err := providerOutput(filepath.Join(root, "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	info, err := os.Stat(filepath.Join(root, "private"))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal(info, err)
	}
	if _, err := providerOutput(filepath.Join(root, "private")); err == nil {
		t.Fatal("overwrote output")
	}
}
func TestProviderReplayRequiresExplicitOptIn(t *testing.T) {
	t.Setenv("LINA_QA_REAL_MODEL", "")
	if code := runProviderReplay([]string{"--provider", "chatgpt", "--socket", "missing", "--out", filepath.Join(t.TempDir(), "out"), "--fixture", "missing"}); code != 64 {
		t.Fatal(code)
	}
}
