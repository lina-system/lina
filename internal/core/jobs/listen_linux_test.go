// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestPrivateBindPreservesProcessUmask(t *testing.T) {
	text := os.Getenv("MOIRAI_TEST_MASK")
	if text == "" {
		for _, mask := range []string{"0000", "0777"} {
			t.Run(mask, func(t *testing.T) {
				child := exec.Command(os.Args[0], "-test.run=^TestPrivateBindPreservesProcessUmask$")
				child.Env = []string{"MOIRAI_TEST_MASK=" + mask}
				if output, err := child.CombinedOutput(); err != nil {
					t.Fatalf("bind: %v\n%s", err, output)
				}
			})
		}
		return
	}
	mask, err := strconv.ParseInt(text, 8, 32)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "moirai-bind-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	previous := syscall.Umask(int(mask))
	defer syscall.Umask(previous)
	path := filepath.Join(dir, "jobs.sock")
	listener, err := listenPrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private bind: %v %v", info, err)
	}
	if got := syscall.Umask(int(mask)); got != int(mask) {
		t.Fatalf("process umask changed to %04o", got)
	}
	if failed, err := listenPrivate(filepath.Join(dir, strings.Repeat("s", 108))); err == nil {
		failed.Close()
		t.Fatal("invalid socket path accepted")
	}
	if got := syscall.Umask(int(mask)); got != int(mask) {
		t.Fatalf("failed bind changed process umask to %04o", got)
	}
}
