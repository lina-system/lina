// SPDX-License-Identifier: Apache-2.0
package boot_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
)

func staleSocket(t *testing.T, path string) os.FileInfo {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// Cancel at READY so a regression which unexpectedly accepts the path cannot
// hang the test. Success is an explicit failure, independent of elapsed time.
func runToReady(t *testing.T, root, socket string) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := false
	err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: socket, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
		if step == boot.Ready {
			ready = true
			cancel()
		}
		return nil
	}})
	return ready, err
}

func TestUnrelatedStaleSocketSurvivesRepeatedStarts(t *testing.T) {
	root := stateRoot(t)
	socket := filepath.Join(root, "run/core.sock")
	before := staleSocket(t, socket)
	for attempt := 1; attempt <= 2; attempt++ {
		ready, err := runToReady(t, root, socket)
		if err == nil || ready {
			t.Fatalf("attempt %d claimed an unrelated stale socket: ready=%v error=%v", attempt, ready, err)
		}
		after, err := os.Lstat(socket)
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("attempt %d removed or replaced unrelated socket: %v", attempt, err)
		}
	}
}

func TestFailedSocketChangePreservesCrashRecovery(t *testing.T) {
	root := stateRoot(t)
	original := filepath.Join(root, "run/core.sock")
	crashAt(t, root, boot.Ready)
	before, err := os.Lstat(original)
	if err != nil {
		t.Fatal(err)
	}
	occupied := filepath.Join(root, "run/occupied.sock")
	if err := os.WriteFile(occupied, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	ready, err := runToReady(t, root, occupied)
	if err == nil || ready {
		t.Fatalf("occupied replacement accepted: ready=%v error=%v", ready, err)
	}
	after, err := os.Lstat(original)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("failed path change disturbed original socket: %v", err)
	}
	ready, err = runToReady(t, root, original)
	if err != nil || !ready {
		t.Fatalf("failed path change lost original socket ownership: ready=%v error=%v", ready, err)
	}
	data, err := os.ReadFile(occupied)
	if err != nil || string(data) != "unrelated" {
		t.Fatalf("occupied replacement changed: %s %v", data, err)
	}
}
