// SPDX-License-Identifier: Apache-2.0

package boot_test

import (
	"bytes"
	"context"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
)

func TestSandboxFailureLeavesCoreReady(t *testing.T) {
	missingLauncher := filepath.Join(t.TempDir(), "missing-launcher")
	root := stateRoot(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reached := false
	err := boot.Run(ctx, boot.Config{SandboxLauncher: missingLauncher, StateRoot: root, Socket: filepath.Join(root, "run/core.sock"), AfterStep: func(step boot.Step, snapshot boot.Snapshot) error {
		if step == boot.Ready {
			reached = true
			if snapshot.Sandbox.Available || snapshot.Sandbox.Cause == "" {
				t.Errorf("sandbox status=%+v", snapshot.Sandbox)
			}
			if snapshot.State != boot.Running {
				t.Errorf("state=%s", snapshot.State)
			}
			cancel()
		}
		return nil
	}})
	if err != nil || !reached {
		t.Fatalf("Core did not remain ready: %v", err)
	}
}

func TestStartupProvidesProbedSandbox(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("OS launcher absent")
	}
	root := stateRoot(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var box *sandbox.Sandbox
	err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/core.sock"), Services: boot.Services{SandboxReady: func(value *sandbox.Sandbox) { box = value }}, AfterStep: func(step boot.Step, snapshot boot.Snapshot) error {
		if step == boot.Ready {
			if box == nil || !box.Status().Available || !snapshot.Sandbox.Available {
				t.Error("startup did not provide an available sandbox")
			}
			cancel()
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	l := lease.New(func() {})
	defer l.Release()
	if box == nil {
		t.Fatal("sandbox service was not composed")
	}
	if err := box.Run(t.Context(), l, sandbox.Command{Path: "/bin/true"}); err == nil {
		t.Fatal("Core shutdown left process admission enabled")
	}
}

func TestSandboxCannotReadMaterialRevisions(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("OS launcher absent")
	}
	root := stateRoot(t)
	for _, area := range []string{"materials", "derived"} {
		if err := os.Mkdir(filepath.Join(root, area), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, area, "private"), []byte("private-material"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var box *sandbox.Sandbox
	err := boot.Run(ctx, boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/core.sock"), Services: boot.Services{SandboxReady: func(value *sandbox.Sandbox) { box = value }}, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
		if step == boot.Ready {
			l := lease.New(func() {})
			defer l.Release()
			var out bytes.Buffer
			err := box.Run(ctx, l, sandbox.Command{Path: "/bin/sh", Args: []string{"-c", `[ ! -r "$1/materials/private" ] && [ ! -r "$1/derived/private" ]`, "canary", root}, Stdout: &out, Stderr: &out})
			if err != nil {
				t.Errorf("private material readable: %v %s", err, &out)
			}
			cancel()
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}
