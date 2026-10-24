// SPDX-License-Identifier: Apache-2.0

package ctlfiles

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRecordRejectsInternalSymlink(t *testing.T) {
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("target", []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(path, "update.json")); err != nil {
		t.Fatal(err)
	}
	store := &Store{dir: root}
	if data, err := store.Read("update.json"); err == nil || len(data) != 0 {
		t.Fatalf("internal symlink accepted: data=%q err=%v", data, err)
	}
}

func TestReadRecordDuringAtomicReplacement(t *testing.T) {
	for _, replacement := range []string{"private", "after-stat", "path-unlinked", "public", "hardlink", "symlink", "continuous"} {
		t.Run(replacement, func(t *testing.T) {
			path := t.TempDir()
			root, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := root.WriteFile("update.json", []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := root.WriteFile("target", []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			var opened []*os.File
			open := func(name string, flags int, mode os.FileMode) (*os.File, error) {
				file, err := root.OpenFile(name, flags, mode)
				if err != nil {
					return nil, err
				}
				opened = append(opened, file)
				t.Cleanup(func() { file.Close() })
				if len(opened) > 8 {
					t.Fatal("record replacement retry was not bounded")
				}
				if replacement == "after-stat" || replacement == "path-unlinked" || len(opened) > 1 && replacement != "continuous" {
					return file, nil
				}
				// Exactly the writer's rename, after open and before the reader's
				// fstat. No scheduler race or sleep is needed to unlink this fd.
				if err := root.WriteFile("next", []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
				switch replacement {
				case "public":
					err = root.Chmod("next", 0644)
				case "hardlink":
					err = os.Link(filepath.Join(path, "next"), filepath.Join(path, "alias"))
				case "symlink":
					if err = root.Remove("next"); err == nil {
						err = os.Symlink("target", filepath.Join(path, "next"))
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := root.Rename("next", name); err != nil {
					t.Fatal(err)
				}
				return file, nil
			}
			lstat := func(name string) (os.FileInfo, error) {
				if (replacement == "after-stat" || replacement == "path-unlinked") && len(opened) == 1 {
					old, err := root.Open(name)
					if err != nil {
						t.Fatal(err)
					}
					defer old.Close()
					if err := root.WriteFile("next", []byte("new"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := root.Rename("next", name); err != nil {
						t.Fatal(err)
					}
					if replacement == "path-unlinked" {
						return old.Stat() // Replacement during the entry's metadata lookup.
					}
				}
				return root.Lstat(name)
			}
			data, err := readRecord("update.json", open, lstat)
			if replacement == "private" || replacement == "after-stat" || replacement == "path-unlinked" {
				if err != nil || string(data) != "new" {
					t.Errorf("atomic replacement rejected: data=%q err=%v", data, err)
				}
			} else if err == nil || len(data) != 0 {
				t.Errorf("unsafe or continuously replaced record accepted: data=%q err=%v", data, err)
			}
			if len(opened) > 8 {
				t.Error("record replacement retry was not bounded")
			}
			for _, file := range opened {
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					file.Close()
					t.Error("read retained a file descriptor")
				}
			}
		})
	}
}
