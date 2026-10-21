// SPDX-License-Identifier: Apache-2.0
package clientsrv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestListenPrivateAtBind(t *testing.T) {
	maskText := os.Getenv("LINA_LISTEN_TEST_UMASK")
	if maskText == "" {
		for _, mask := range []string{"0022", "0777"} {
			t.Run(mask, func(t *testing.T) {
				child := exec.Command(os.Args[0], "-test.run=^TestListenPrivateAtBind$")
				child.Env = append(os.Environ(), "LINA_LISTEN_TEST_UMASK="+mask)
				if output, err := child.CombinedOutput(); err != nil {
					t.Fatalf("umask %s: %v\n%s", mask, err, output)
				}
			})
		}
		return
	}
	mask, err := strconv.ParseInt(maskText, 8, 32)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "lina-bind-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	previous := syscall.Umask(int(mask))
	defer syscall.Umask(previous)
	path := filepath.Join(dir, "core.sock")
	observed := false
	listener, err := (ListenConfig{AfterBind: func() error {
		observed = true
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0600 {
			return fmt.Errorf("socket not private at bind: %s", info.Mode())
		}
		return nil
	}}).Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if !observed {
		t.Fatal("bind observer not called")
	}
	if got := syscall.Umask(int(mask)); got != int(mask) {
		t.Fatalf("bind changed caller umask: %04o", got)
	}
	// Fail in bind itself, after preparing an already-existing directory.
	if failed, err := Listen(filepath.Join(dir, strings.Repeat("s", 108))); err == nil {
		failed.Close()
		t.Fatal("overlong socket address accepted")
	}
	if got := syscall.Umask(int(mask)); got != int(mask) {
		t.Fatalf("failed bind changed caller umask: %04o", got)
	}
}
