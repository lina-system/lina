// SPDX-License-Identifier: Apache-2.0
package boot_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
)

func readSentinel(t *testing.T, root string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "system/core/run.sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	var sentinel map[string]string
	if err := json.Unmarshal(data, &sentinel); err != nil {
		t.Fatal(err)
	}
	return sentinel
}

func TestSIGKILLBetweenBindAndSentinelPublication(t *testing.T) {
	for _, changePath := range []bool{false, true} {
		name := "fresh"
		if changePath {
			name = "path-change"
		}
		t.Run(name, func(t *testing.T) {
			root := stateRoot(t)
			socket := filepath.Join(root, "run/core.sock")
			previous := ""
			if changePath {
				crashAt(t, root, boot.Ready)
				previous = socket
				socket = filepath.Join(root, "run/new.sock")
			}
			killed := crashAtSocket(t, root, socket, boot.SocketBound)
			// Prove this was the bind/publication gap, not the later serving step.
			assertMode(t, socket, 0600, os.ModeSocket)
			if sentinel := readSentinel(t, root); sentinel["socket"] != previous {
				t.Fatalf("kill happened after final publication: %v", sentinel)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready := false
			err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: socket, AfterStep: func(step boot.Step, snapshot boot.Snapshot) error {
				if step == boot.Ready {
					ready = true
					if snapshot.Epoch <= killed.Epoch {
						t.Fatalf("restart epoch %d did not exceed %d", snapshot.Epoch, killed.Epoch)
					}
					if sentinel := readSentinel(t, root); sentinel["socket"] != socket || sentinel["pending_socket"] != "" {
						t.Fatalf("final ownership not published: %v", sentinel)
					}
					cancel()
				}
				return nil
			}})
			if err != nil || !ready {
				t.Fatalf("restart after bind/publication kill: ready=%v error=%v", ready, err)
			}
		})
	}
}

func TestPendingSocketSurvivesFailedStarts(t *testing.T) {
	root := stateRoot(t)
	socket := filepath.Join(root, "run/core.sock")
	crashAt(t, root, boot.SocketBound)
	// A second interruption must preserve the first bind's recovery identity.
	crashAt(t, root, boot.EpochMinted)
	occupied := filepath.Join(root, "run/unrelated.sock")
	before := staleSocket(t, occupied)
	for attempt := 1; attempt <= 2; attempt++ {
		ready, err := runToReady(t, root, occupied)
		if err == nil || ready {
			t.Fatalf("attempt %d claimed unrelated socket: ready=%v error=%v", attempt, ready, err)
		}
		after, err := os.Lstat(occupied)
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("attempt %d disturbed unrelated socket: %v", attempt, err)
		}
	}
	if ready, err := runToReady(t, root, socket); err != nil || !ready {
		t.Fatalf("lost pending socket after failed starts: ready=%v error=%v", ready, err)
	}
}

func TestSocketIntentPrecedesBindAndKeepsPreviousOwnership(t *testing.T) {
	root := stateRoot(t)
	original := filepath.Join(root, "run/core.sock")
	crashAt(t, root, boot.Ready)
	socket := filepath.Join(root, "run/new.sock")
	crashAtSocket(t, root, socket, boot.SocketPending)
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("pending boundary already bound: %v", err)
	}
	sentinel := readSentinel(t, root)
	if sentinel["socket"] != original || sentinel["pending_socket"] != socket {
		t.Fatalf("pending bind lost previous ownership or intent: %v", sentinel)
	}
	if ready, err := runToReady(t, root, original); err != nil || !ready {
		t.Fatalf("previous owned socket not recoverable: ready=%v error=%v", ready, err)
	}
}

func TestFailedBindDoesNotClaimSocket(t *testing.T) {
	root := stateRoot(t)
	socket := filepath.Join(root, "run/core.sock")
	var before os.FileInfo
	injected := errors.New("unexpected READY")
	err := boot.Run(context.Background(), boot.Config{StateRoot: root, Socket: socket, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
		if step == boot.SocketPending {
			before = staleSocket(t, socket)
		}
		if step == boot.Ready {
			return injected
		}
		return nil
	}})
	if err == nil || errors.Is(err, injected) || before == nil {
		t.Fatalf("bind did not fail at occupied path: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if ready, err := runToReady(t, root, socket); err == nil || ready {
			t.Fatalf("attempt %d claimed failed bind: ready=%v error=%v", attempt, ready, err)
		}
		after, err := os.Lstat(socket)
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("attempt %d disturbed failed bind: %v", attempt, err)
		}
	}
}

func TestSocketChangeRetiresPendingBind(t *testing.T) {
	root := stateRoot(t)
	original := filepath.Join(root, "run/core.sock")
	crashAt(t, root, boot.Ready)
	pending := filepath.Join(root, "run/pending.sock")
	crashAtSocket(t, root, pending, boot.SocketBound)
	replacement := filepath.Join(root, "run/replacement.sock")
	crashAtSocket(t, root, replacement, boot.SocketPending)
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		t.Fatalf("old pending bind abandoned: %v", err)
	}
	sentinel := readSentinel(t, root)
	if sentinel["socket"] != original || sentinel["pending_socket"] != replacement {
		t.Fatalf("path change lost ownership or intent: %v", sentinel)
	}
	if ready, err := runToReady(t, root, original); err != nil || !ready {
		t.Fatalf("path change lost original socket: ready=%v error=%v", ready, err)
	}
}

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
