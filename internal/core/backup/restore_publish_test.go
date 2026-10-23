// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRestorePublicationResumesAtEveryBoundary(t *testing.T) {
	t.Parallel()
	// Inject after each durable rename, including after canon publication.
	for stop := 0; stop < 15; stop++ {
		rootPath := t.TempDir()
		for _, area := range []string{"versions", "materials", "derived", "workspace", "canon"} {
			for _, prefix := range []string{"", "staging/prepared/new"} {
				dir := filepath.Join(rootPath, prefix, area)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "value"), []byte(prefix), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, prefix := range []string{"", "staging/prepared/new"} {
			dir := filepath.Join(rootPath, prefix, "backups")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "removals"), []byte(prefix), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Join(rootPath, "staging/prepared/previous/backups"), 0700); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		injected := errors.New("interrupted")
		after := func(string) error {
			n := count
			count++
			if n == stop {
				return injected
			}
			return nil
		}
		err = commitRestore(root, "staging/prepared", Manifest{}, after)
		if errors.Is(err, injected) {
			if err := CheckRestore(rootPath); !errors.Is(err, ErrRestorePending) {
				t.Fatalf("boundary %d admitted Core: %v", stop, err)
			}
			if err := resumeRestore(root, Manifest{}, nil); err != nil {
				t.Fatalf("boundary %d: %v", stop, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if err := CheckRestore(rootPath); err != nil {
			t.Fatal(err)
		}
		for _, area := range []string{"versions/value", "materials/value", "derived/value", "workspace/value", "canon/value", "backups/removals"} {
			value, err := root.ReadFile(area)
			if err != nil || string(value) != "staging/prepared/new" {
				t.Fatalf("boundary %d mixed %s: %s %v", stop, area, value, err)
			}
		}
		root.Close()
	}
}

func TestRestorePublicationRefusesSymlink(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(rootPath, "staging")); err != nil {
		t.Fatal(err)
	}
	if err := CheckRestore(rootPath); err == nil {
		t.Fatal("linked restore staging admitted")
	}
}
