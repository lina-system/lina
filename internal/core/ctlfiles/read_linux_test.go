// SPDX-License-Identifier: Apache-2.0

package ctlfiles

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A link inside system/core is refused even when its target is a private
// record in the same directory, which os.Root resolves without escaping.
func TestReadRefusesInternalSymlink(t *testing.T) {
	root := privateRoot(t)
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	core := filepath.Join(root, "system", "core")
	if err := os.WriteFile(filepath.Join(core, "target"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(core, "update.json")); err != nil {
		t.Fatal(err)
	}
	data, err := store.Read("update.json")
	if err == nil || len(data) != 0 {
		t.Fatalf("internal symlink accepted: data=%q err=%v", data, err)
	}
}

// TestReadDuringAtomicReplacement publishes the record before the reader
// checks each layer, so every case is deterministic. The replacement is the
// one Write's rename performs; a reader must retry a bounded number of times.
func TestReadDuringAtomicReplacement(t *testing.T) {
	const name = "update.json"
	cases := []struct {
		name string
		// want is the returned record; empty means the read must fail.
		want string
		// publish replaces the record after the reader opens it and before it
		// checks the opened inode. attempt counts opens from one.
		publish func(t *testing.T, root *os.Root, path, name string, attempt int)
		// entry replaces the record before the reader checks the directory
		// entry and returns what the reader sees for that entry. attempt counts
		// entry checks from one.
		entry func(t *testing.T, root *os.Root, path, name string, old os.FileInfo, attempt int) (os.FileInfo, error)
	}{
		{
			name: "replaced-before-stat",
			want: "new",
			publish: func(t *testing.T, root *os.Root, path, name string, attempt int) {
				if attempt == 1 {
					publishPrivate(t, root, name)
				}
			},
		},
		{
			name: "replaced-after-stat",
			want: "new",
			entry: func(t *testing.T, root *os.Root, path, name string, _ os.FileInfo, attempt int) (os.FileInfo, error) {
				if attempt == 1 {
					publishPrivate(t, root, name)
				}
				return root.Lstat(name)
			},
		},
		{
			name: "entry-unlinked-before-stat",
			want: "new",
			entry: func(t *testing.T, root *os.Root, path, name string, old os.FileInfo, attempt int) (os.FileInfo, error) {
				if attempt == 1 {
					publishPrivate(t, root, name)
					return old, nil
				}
				return root.Lstat(name)
			},
		},
		{
			name: "entry-observed-unlinked",
			want: "new",
			entry: func(t *testing.T, root *os.Root, path, name string, _ os.FileInfo, attempt int) (os.FileInfo, error) {
				if attempt == 1 {
					file, err := root.Open(name)
					if err != nil {
						t.Fatal(err)
					}
					defer file.Close()
					publishPrivate(t, root, name)
					// Lstat resolved the entry, then the rename unlinked it
					// before the metadata copy.
					return file.Stat()
				}
				return root.Lstat(name)
			},
		},
		{
			name: "replaced-by-public",
			publish: func(t *testing.T, root *os.Root, path, name string, attempt int) {
				if attempt == 1 {
					if err := root.WriteFile("next", []byte("new"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := root.Chmod("next", 0644); err != nil {
						t.Fatal(err)
					}
					if err := root.Rename("next", name); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "replaced-by-hardlink",
			publish: func(t *testing.T, root *os.Root, path, name string, attempt int) {
				if attempt == 1 {
					if err := root.WriteFile("next", []byte("new"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Link(filepath.Join(path, "next"), filepath.Join(path, "alias")); err != nil {
						t.Fatal(err)
					}
					if err := root.Rename("next", name); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "replaced-by-symlink",
			publish: func(t *testing.T, root *os.Root, path, name string, attempt int) {
				if attempt == 1 {
					if err := os.Symlink("target", filepath.Join(path, "next")); err != nil {
						t.Fatal(err)
					}
					if err := root.Rename("next", name); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "replaced-on-every-attempt",
			publish: func(t *testing.T, root *os.Root, path, name string, attempt int) {
				publishPrivate(t, root, name)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			root, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := root.WriteFile(name, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := root.WriteFile("target", []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			var opened []*os.File
			open := func(entry string, flags int, mode os.FileMode) (*os.File, error) {
				file, err := root.OpenFile(entry, flags, mode)
				if err != nil {
					return nil, err
				}
				opened = append(opened, file)
				t.Cleanup(func() { file.Close() })
				if tc.publish != nil {
					tc.publish(t, root, path, entry, len(opened))
				}
				return file, nil
			}
			checks := 0
			lstat := func(entry string) (os.FileInfo, error) {
				if tc.entry == nil {
					return root.Lstat(entry)
				}
				checks++
				old, err := root.Lstat(entry)
				if err != nil {
					t.Fatal(err)
				}
				return tc.entry(t, root, path, entry, old, checks)
			}
			data, err := readRecord(name, open, lstat)
			if tc.want == "" {
				if err == nil || len(data) != 0 {
					t.Errorf("unsafe or continuously replaced record accepted: data=%q err=%v", data, err)
				}
			} else if err != nil || string(data) != tc.want {
				t.Errorf("atomic replacement refused: data=%q err=%v", data, err)
			}
			if len(opened) > 8 {
				t.Errorf("read opened the record %d times, retry is unbounded", len(opened))
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

func publishPrivate(t *testing.T, root *os.Root, name string) {
	t.Helper()
	if err := root.WriteFile("next", []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("next", name); err != nil {
		t.Fatal(err)
	}
}
