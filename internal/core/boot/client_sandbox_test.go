// SPDX-License-Identifier: Apache-2.0
package boot_test

import (
	"bytes"
	"context"
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

func TestClientSocketExcludedFromSandbox(t *testing.T) {
	if len(os.Args) > 2 && os.Args[len(os.Args)-2] == "--socket-canary" {
		conn, err := net.DialTimeout("unix", os.Args[len(os.Args)-1], time.Second)
		if err == nil {
			conn.Close()
			t.Fatal("sandbox connected to Core")
		}
		fmt.Println("socket-connect-refused")
		return
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("OS launcher absent")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := stateRoot(t)
	socket := filepath.Join(root, "run/core.sock")
	args := []string{"-test.run=^TestClientSocketExcludedFromSandbox$"}
	if testing.CoverMode() != "" {
		// The instrumented child must write coverage inside the existing
		// writable workspace; its inherited test output directory is read-only.
		coverage := filepath.Join(root, "workspace")
		t.Setenv("GOCOVERDIR", coverage)
		args = append(args, "-test.gocoverdir="+coverage)
	}
	args = append(args, "--", "--socket-canary", socket)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var box *sandbox.Sandbox
	err = boot.Run(ctx, boot.Config{StateRoot: root, Socket: socket, Services: boot.Services{SandboxReady: func(s *sandbox.Sandbox) { box = s }}, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
		if step != boot.Ready {
			return nil
		}
		defer cancel()
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			return fmt.Errorf("unconfined control connect: %w", err)
		}
		conn.Close()
		if box == nil || !box.Status().Available {
			return fmt.Errorf("sandbox probe unavailable")
		}
		for _, network := range []bool{false, true} {
			l := lease.New(func() {})
			var output bytes.Buffer
			err := box.Run(ctx, l, sandbox.Command{Path: executable, Args: args, Network: network, Stdout: &output, Stderr: &output})
			l.Release()
			if err != nil {
				return fmt.Errorf("sandbox canary network=%v: %w: %s", network, err, &output)
			}
			if !bytes.Contains(output.Bytes(), []byte("socket-connect-refused")) {
				return fmt.Errorf("canary did not run: %s", &output)
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}
