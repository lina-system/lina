// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/sandbox"
)

func TestM1SandboxCannotConnectClient(t *testing.T) {
	h := newHarness(t, "")
	dir := filepath.Join(h.root, "system", "core")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	var box *sandbox.Sandbox
	cfg := boot.Config{StateRoot: h.root, Socket: h.socket, Services: boot.Services{SandboxReady: func(value *sandbox.Sandbox) { box = value }}}
	cfg.AfterStep = func(step boot.Step, _ boot.Snapshot) error {
		if step != boot.Ready {
			return nil
		}
		defer cancel()
		if box == nil || !box.Status().Available {
			return fmt.Errorf("actual Core sandbox unavailable: %+v", box)
		}
		args := []string{"-test.run=^TestM1SocketCanary$", "--", h.socket, "allowed"}
		control := exec.CommandContext(ctx, os.Args[0], args...)
		control.Env = []string{"PATH="}
		if output, err := control.CombinedOutput(); err != nil {
			return fmt.Errorf("outside control: %w\n%s", err, output)
		}
		args[len(args)-1] = readAdmissionScenario(t).SandboxConnect
		l := lease.New(func() {})
		defer l.Release()
		var output bytes.Buffer
		// The network-enabled profile ensures that a disabled network syscall is
		// not the reason the connection fails: Core's socket path must be hidden.
		if err := box.Run(ctx, l, sandbox.Command{Path: os.Args[0], Args: args, Network: true, Stdout: &output, Stderr: &output}); err != nil {
			return fmt.Errorf("sandbox canary: %w\n%s", err, &output)
		}
		return nil
	}
	if err := boot.Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestM1SocketCanary(t *testing.T) {
	args := flag.Args()
	if len(args) != 2 {
		t.Skip("subprocess helper")
	}
	conn, err := net.DialTimeout("unix", args[0], time.Second)
	if err == nil {
		conn.Close()
	}
	if args[1] == "denied" && err == nil {
		t.Fatal("sandbox connected to Core's client socket")
	}
	if args[1] == "allowed" && err != nil {
		t.Fatalf("outside client cannot connect: %v", err)
	}
}
