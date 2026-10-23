// SPDX-License-Identifier: Apache-2.0
package ctlfiles

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func openLocked(t *testing.T, root string) *Store {
	t.Helper()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Acquire(); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestLockKeepsOneInode(t *testing.T) {
	root := privateRoot(t)
	first := openLocked(t, root)
	lock := filepath.Join(root, "system/core/core.lock")
	before, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if !first.Locked() || second.Locked() {
		t.Fatal("lock ownership report is incorrect")
	}
	if err := second.Acquire(); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock = %v", err)
	}
	if err := second.Write("halt.json", []byte(`{}`)); err == nil {
		t.Fatal("write without lock succeeded")
	}
	if err := first.Write("core.lock", nil); err == nil {
		t.Fatal("lock inode can be replaced")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if first.Locked() {
		t.Fatal("closed store reports a held lock")
	}
	if err := second.Acquire(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(lock)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock inode changed: %v", err)
	}
}

func TestEpochUsesAllHighWaterMarks(t *testing.T) {
	store := openLocked(t, privateRoot(t))
	for _, tc := range []struct{ canon, peer, want uint64 }{{0, 0, 1}, {0, 0, 2}, {50, 4, 51}, {2, 90, 91}} {
		got, err := store.MintEpoch(tc.canon, tc.peer)
		if err != nil || got != tc.want {
			t.Fatalf("mint = %d, %v; want %d", got, err, tc.want)
		}
		saved, err := store.Epoch()
		if err != nil || saved != got {
			t.Fatalf("saved = %d, %v", saved, err)
		}
	}
	if _, err := store.MintEpoch(math.MaxUint64, 0); err == nil {
		t.Fatal("overflow accepted")
	}
	saved, _ := store.Epoch()
	if saved != 91 {
		t.Fatalf("overflow changed record to %d", saved)
	}
}

func TestMalformedEpochNeverResets(t *testing.T) {
	for _, input := range []string{"", "01", "-1", "+1", "1\n", " 1", "18446744073709551616", "null"} {
		t.Run(input, func(t *testing.T) {
			root := privateRoot(t)
			store := openLocked(t, root)
			path := filepath.Join(root, "system/core/epoch")
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.MintEpoch(9, 10); err == nil {
				t.Fatal("malformed epoch accepted")
			}
			after, _ := os.ReadFile(path)
			if string(after) != input {
				t.Fatal("malformed epoch overwritten")
			}
		})
	}
}

func TestPrivateAtomicReplacement(t *testing.T) {
	root := privateRoot(t)
	store := openLocked(t, root)
	if err := store.Write("halt.json", []byte(`{"reason":"io"}`)); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(filepath.Join(root, "system/core/halt.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := store.Write("halt.json", []byte(`{"reason":"corrupt"}`)); err != nil {
		t.Fatal(err)
	}
	oldInfo, _ := old.Stat()
	newInfo, _ := os.Stat(filepath.Join(root, "system/core/halt.json"))
	if os.SameFile(oldInfo, newInfo) {
		t.Fatal("rewrote destination in place")
	}
	data, err := store.Read("halt.json")
	if err != nil || string(data) != `{"reason":"corrupt"}` {
		t.Fatalf("read = %s, %v", data, err)
	}
	for _, name := range []string{"system", "system/core", "system/core/core.lock", "system/core/halt.json"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %s", name, info.Mode())
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, "system/core"))
	if len(entries) != 2 {
		t.Fatalf("left temporary files: %v", entries)
	}
	for _, name := range []string{"../escape", "unknown", "core.lock"} {
		if err := store.Write(name, nil); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}

func TestUnsafePathsAreRefused(t *testing.T) {
	for _, kind := range []string{"public root", "root symlink", "core symlink", "epoch symlink", "epoch hardlink", "public epoch"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "state")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, "outside")
			if err := os.WriteFile(target, []byte("77"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "public root":
				os.Chmod(root, 0755)
			case "root symlink":
				os.Remove(root)
				os.Symlink(parent, root)
			case "core symlink":
				os.Mkdir(filepath.Join(root, "system"), 0700)
				os.Symlink(parent, filepath.Join(root, "system/core"))
			}
			store, err := Open(root)
			if kind == "public root" || kind == "root symlink" || kind == "core symlink" {
				if err == nil {
					store.Close()
					t.Fatal("unsafe root accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.Acquire(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "system/core/epoch")
			switch kind {
			case "epoch symlink":
				err = os.Symlink(target, path)
			case "epoch hardlink":
				err = os.Link(target, path)
			case "public epoch":
				err = os.WriteFile(path, []byte("77"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			// WriteFile applies the process umask. The public epoch must stay
			// mode 0644 or a restrictive mask turns the fixture private.
			if kind == "public epoch" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(path)
				if err != nil || info.Mode().Perm() != 0644 {
					t.Fatalf("public epoch mode: %v %v", info, err)
				}
			}
			if _, err := store.MintEpoch(0, 0); err == nil {
				t.Fatal("unsafe epoch accepted")
			}
			data, _ := os.ReadFile(target)
			if string(data) != "77" {
				t.Fatal("external file changed")
			}
		})
	}
}

func TestFilesystemClassification(t *testing.T) {
	for _, kind := range []int64{0x6969, 0xff534d42, 0x517b, 0x65735546, 0x01021997, 0x9fa0, 0x12345678} {
		if localFilesystem(kind) {
			t.Fatalf("accepted remote, FUSE, pseudo or unknown filesystem %#x", kind)
		}
	}
	for _, kind := range []int64{0xef53, 0x58465342, 0x9123683e, 0x01021994, 0x794c7630} {
		if !localFilesystem(kind) {
			t.Fatalf("refused local filesystem %#x", kind)
		}
	}
}

func privateRoot(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
