// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

type directoryInfo struct {
	os.FileInfo
	stat syscall.Stat_t
	mode os.FileMode
}

func (i directoryInfo) Sys() any          { return &i.stat }
func (i directoryInfo) Mode() os.FileMode { return i.mode }

func TestSocketDirectoryOwner(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "socket-parent")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		uid       uint32
		mode      os.FileMode
		wantError bool
	}{
		{"current user", uint32(os.Geteuid()), os.ModeDir | 0700, false},
		{"another user", uint32(os.Geteuid()) + 1, os.ModeDir | 0700, true},
		{"group writable", uint32(os.Geteuid()), os.ModeDir | 0770, true},
		{"other writable", uint32(os.Geteuid()), os.ModeDir | 0702, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stat := *info.Sys().(*syscall.Stat_t)
			stat.Uid = tc.uid
			err := validateSocketDirectory(directoryInfo{FileInfo: info, stat: stat, mode: tc.mode})
			if (err != nil) != tc.wantError {
				t.Fatalf("uid=%d mode=%s: error=%v; want rejection=%v", tc.uid, tc.mode, err, tc.wantError)
			}
		})
	}
}
