// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type coreLifetimeObservation struct {
	PID          int
	Root, Socket string
}

// Re-execute the real test binary so os.Exit, SIGKILL and testing's timeout
// bypass Cleanup exactly as they do when an acceptance shard is interrupted.
func TestHarnessCoreLifetimeChild(t *testing.T) {
	binary := os.Getenv("LINA_E2E_LIFETIME_BINARY")
	if binary == "" {
		return
	}
	h := newHarnessWithin(t, binary, 2*time.Minute)
	core := h.startCore()
	h.connect(core).Close()
	ready := os.NewFile(3, "ready")
	if err := json.NewEncoder(ready).Encode(coreLifetimeObservation{core.command.Process.Pid, h.root, h.socket}); err != nil {
		t.Fatal(err)
	}
	ready.Close()
	var release [1]byte
	if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
		t.Fatal(err)
	}
	switch os.Getenv("LINA_E2E_LIFETIME_MODE") {
	case "exit":
		os.Exit(23)
	case "kill", "timeout":
		// An external kill or testing's watchdog must end this process.
		select {}
	}
}

func TestHarnessCoreLifetime(t *testing.T) {
	binary := buildLina(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"cleanup", "exit", "kill", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			root := t.TempDir()
			lockPath := filepath.Join(root, "slot.lock")
			lock, err := os.Create(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			probe, err := os.OpenFile(lockPath, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Close()
			ready, readyWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer ready.Close()
			defer readyWriter.Close()
			deadline, _ := ctx.Deadline()
			if err := ready.SetReadDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHarnessCoreLifetimeChild$")
			if mode == "timeout" {
				cmd.Args = append(cmd.Args, "-test.timeout=20s")
			}
			cmd.Env = append(os.Environ(), "LINA_E2E_LIFETIME_BINARY="+binary, "LINA_E2E_LIFETIME_MODE="+mode, "TMPDIR="+root)
			// Model the inherited heavy-slot lock at fd 9, without touching
			// any real coordination lock. ExtraFiles deliberately clears CLOEXEC.
			cmd.ExtraFiles = []*os.File{readyWriter, nil, nil, nil, nil, nil, lock}
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			cmd.WaitDelay = time.Second
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					cmd.Process.Kill()
					cmd.Wait()
				}
			}()
			readyWriter.Close()
			lock.Close()
			var observed coreLifetimeObservation
			if err := json.NewDecoder(ready).Decode(&observed); err != nil {
				cmd.Process.Kill()
				cmd.Wait()
				waited = true
				t.Fatalf("Core readiness: %v; child: %s", err, &output)
			}
			// Even the red run owns and removes the deliberately orphaned Core.
			defer syscall.Kill(observed.PID, syscall.SIGKILL)
			if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
				t.Fatalf("test parent did not retain its lock: %v", err)
			}
			fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", observed.PID))
			if err != nil {
				t.Fatal(err)
			}
			for _, fd := range fds {
				target, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", observed.PID, fd.Name()))
				if target == lockPath {
					t.Errorf("Core inherited the parent's slot lock at fd %s", fd.Name())
				}
			}
			if mode == "kill" {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else if _, err := input.Write([]byte("R")); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			if mode == "cleanup" && err != nil || mode != "cleanup" && err == nil {
				t.Fatalf("unexpected helper exit: %v; %s", err, &output)
			}
			if mode == "timeout" && !strings.Contains(output.String(), "test timed out after") {
				t.Fatalf("helper did not reach testing's watchdog: %v; %s", err, &output)
			}
			waitHarnessCoreExit(t, observed.PID)
			if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatalf("slot lock survived the test parent: %v", err)
			}
			t.Logf("Core exited and inherited lock released after %s", mode)
		})
	}
}

func waitHarnessCoreExit(t *testing.T, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		// An orphan may await init's reaper, but a zombie has already
		// released its address space and descriptors and cannot run.
		end := strings.LastIndexByte(string(stat), ')')
		if end >= 0 && strings.HasPrefix(string(stat[end+1:]), " Z") {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Core %d survived its test process: %v", pid, ctx.Err())
		case <-tick.C:
		}
	}
}
