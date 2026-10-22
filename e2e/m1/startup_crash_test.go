// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
)

// The only injected seam is an acknowledged boot boundary. Recovery uses the
// installed CLI and its real client socket, not a replacement boot service.
func TestM1StartupCrashRecovery(t *testing.T) {
	t.Parallel()

	binary := buildLina(t)
	bundle := makeInstallBundle(t, t.TempDir(), binary, buildCommand(t, "lina-node"))
	for step := boot.RootValidated; step <= boot.Ready; step++ {
		t.Run(strconv.Itoa(int(step)), func(t *testing.T) {
			h, start := installConversationBundle(t, newHarness(t, binary), bundle, "")
			core := start(false)
			before := restartClient(h, core)
			h.inputRefused(before)
			baseline, err := strconv.ParseUint(before.welcome.Epoch, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			h.stopCore(core)

			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			installed := h.binary
			h.binary = executable
			child := h.command("-test.run=^TestM1StartupChild$")
			h.binary = installed
			child.command.Env = append(child.command.Env,
				"LINA_M1_CRASH_ROOT="+h.root, "LINA_M1_CRASH_SOCKET="+h.socket,
				"LINA_M1_CRASH_STEP="+strconv.Itoa(int(step)))
			child.command.Stdout = nil
			out, err := child.command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			in, err := child.command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			child.start(t)
			type observation struct {
				snapshot boot.Snapshot
				err      error
			}
			received := make(chan observation, 1)
			go func() {
				var s boot.Snapshot
				err := json.NewDecoder(out).Decode(&s)
				received <- observation{s, err}
			}()
			var killed boot.Snapshot
			select {
			case result := <-received:
				if result.err != nil {
					child.killAndWait()
					t.Fatalf("boundary %d: %v; %s", step, result.err, &child.stderr)
				}
				killed = result.snapshot
			case <-h.ctx.Done():
				child.killAndWait()
				<-received
				t.Fatal("startup boundary was not acknowledged")
			}
			child.killAndWait()
			if status, ok := child.command.ProcessState.Sys().(syscall.WaitStatus); !ok || status.Signal() != syscall.SIGKILL {
				t.Fatalf("child was not SIGKILLed: %v", child.err)
			}
			core = start(false)
			after := restartClient(h, core)
			h.inputRefused(after) // READY, rather than merely a bound socket.
			epoch, err := strconv.ParseUint(after.welcome.Epoch, 10, 64)
			if err != nil || epoch <= max(baseline, killed.Epoch) {
				t.Fatalf("epoch=%d baseline=%d killed=%d: %v", epoch, baseline, killed.Epoch, err)
			}
			if after.welcome.MainID != before.welcome.MainID {
				t.Fatal("restart changed installed identity")
			}
			h.stopCore(core)
		})
	}
}

func TestM1StartupChild(t *testing.T) {
	root := os.Getenv("LINA_M1_CRASH_ROOT")
	if root == "" {
		return
	}
	target, err := strconv.Atoi(os.Getenv("LINA_M1_CRASH_STEP"))
	if err != nil {
		t.Fatal(err)
	}
	err = boot.Run(context.Background(), boot.Config{StateRoot: root, Socket: os.Getenv("LINA_M1_CRASH_SOCKET"), AfterStep: func(step boot.Step, s boot.Snapshot) error {
		if int(step) != target {
			return nil
		}
		if err := json.NewEncoder(os.Stdout).Encode(s); err != nil {
			return err
		}
		_, err := io.Copy(io.Discard, os.Stdin)
		return err
	}})
	if err != nil {
		t.Fatal(err)
	}
}
