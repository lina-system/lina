// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestListenRecoversKilledCore(t *testing.T) {
	if root := os.Getenv("MOIRAI_KILLED_CORE_ROOT"); root != "" {
		controls, err := ctlfiles.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer controls.Close()
		if err := controls.Acquire(); err != nil {
			t.Fatal(err)
		}
		listener, err := Listen(filepath.Join(root, "jobs.sock"), controls)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		fmt.Fprintln(os.Stdout, "bound")
		io.Copy(io.Discard, os.Stdin)
		return
	}
	root, err := os.MkdirTemp("", "jobs-kill-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestListenRecoversKilledCore$")
	child.Env = []string{"MOIRAI_KILLED_CORE_ROOT=" + root}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "bound" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child did not bind its job listener")
		}
	case <-ctx.Done():
		t.Fatal("child did not acknowledge binding")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("killed Core exited successfully")
	}
	path := filepath.Join(root, "jobs.sock")
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Type() != os.ModeSocket || info.Mode().Perm() != 0600 {
		t.Fatalf("missing private stale socket: %v %v", info, err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	listener, err := Listen(path, controls)
	if err != nil {
		t.Fatalf("restart with stale job socket: %v", err)
	}
	defer listener.Close()
	if live, err := Listen(path, controls); err == nil {
		live.Close()
		t.Fatal("replaced a live listener")
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("refusal disturbed live listener: %v", err)
	}
	conn.Close()
}

func TestListenPreservesUnsafePathsAndRequiresCoreLock(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "socket-mode", "directory-mode", "unlocked", "nil-lock"} {
		t.Run(kind, func(t *testing.T) {
			root, err := os.MkdirTemp("", "jobs-preserve-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(root, 0700); os.RemoveAll(root) })
			controls, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer controls.Close()
			if kind != "unlocked" {
				if err := controls.Acquire(); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "jobs.sock")
			switch kind {
			case "regular":
				if err := os.WriteFile(path, []byte("unrelated"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing-target", path); err != nil {
					t.Fatal(err)
				}
			default:
				listener, err := listenPrivate(path)
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "socket-mode" {
				if err := os.Chmod(path, 0660); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "directory-mode" {
				if err := os.Chmod(root, 0770); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			lock := controls
			if kind == "nil-lock" {
				lock = nil
			}
			if listener, err := Listen(path, lock); err == nil {
				listener.Close()
				t.Fatal("unsafe recovery succeeded")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatalf("refusal disturbed existing path: %v", err)
			}
		})
	}
}
