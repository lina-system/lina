// SPDX-License-Identifier: Apache-2.0
//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOrdinaryProtectedTreeIsAccepted(t *testing.T) {
	root := t.TempDir()
	work, hidden := filepath.Join(root, "work"), filepath.Join(root, "hidden")
	for _, path := range []string{work, hidden} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(hidden, "canary"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := normalize(Config{WorkDir: work, ReadDenied: []string{hidden}}); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedHardlinkAliasFailsClosed(t *testing.T) {
	root := t.TempDir()
	work, hidden := filepath.Join(root, "work"), filepath.Join(root, "hidden")
	for _, path := range []string{work, hidden} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(hidden, "canary")
	if err := os.WriteFile(secret, []byte("synthetic protected bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(work, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := normalize(Config{WorkDir: work, ReadDenied: []string{hidden}}); err == nil {
		t.Fatal("protected hardlink alias accepted")
	}
}

func TestProtectedMountAliases(t *testing.T) {
	root := "1 0 8:1 / / rw - ext4 root rw\n"
	for _, test := range []struct {
		name, mounts string
		paths        []string
		reject       bool
	}{
		{"ordinary", root, []string{"/private"}, false},
		{"namespace handle", root + "2 1 0:5 mnt:[123] /run/namespace rw - nsfs nsfs rw\n", []string{"/private"}, false},
		{"bind directory", root + "2 1 8:1 /private /alias rw - ext4 root rw\n", []string{"/private"}, true},
		{"bind file", root + "2 1 8:1 /private/secret /alias rw - ext4 root rw\n", []string{"/private"}, true},
		{"covered alias", root + "2 1 8:1 /private /alias rw - ext4 root rw\n", []string{"/private", "/alias"}, false},
		{"nested mount", root + "2 1 8:2 / /private/volume rw - ext4 disk rw\n3 1 8:2 / /alias rw - ext4 disk rw\n", []string{"/private"}, true},
		{"different tree", root + "2 1 8:1 /public /alias rw - ext4 root rw\n", []string{"/private"}, false},
		{"escaped path", root + "2 1 8:1 /private\\040data /alias rw - ext4 root rw\n", []string{"/private data"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := checkMountAliases(test.paths, test.mounts); (err != nil) != test.reject {
				t.Fatalf("mount policy: %v, reject=%v", err, test.reject)
			}
		})
	}
}
