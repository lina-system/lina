// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package proc_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

// A command cannot start its own session or process group, so tearing down its
// process group reaches every descendant.
func TestOwnerDeniesSessionEscape(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	owner := proc.NewOwner()
	defer owner.Close(ctx)
	l := lease.New(func() {})
	defer l.Release()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	err = owner.Run(ctx, l, proc.Command{Path: exe, Args: []string{"-test.run=^TestDarwinEscapeProcess$"}, Env: []string{"LINA_PROC_DARWIN_TEST=deny"}, Stdout: &output, Stderr: os.Stderr})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(output.String()); got != "setsid=operation not permitted setpgid=operation not permitted" {
		t.Fatalf("child escape results = %q", got)
	}
}

// The command starts an intermediate process, which starts a leaf with an empty
// environment and exits. The kernel reparents the leaf to launchd, so neither
// the owner token nor a live parent names it. The leaf must still be gone after
// teardown.
func TestOwnerReapsOrphanedClearedEnvironmentDescendant(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	owner := proc.NewOwner()
	defer owner.Close(ctx)
	released := make(chan struct{})
	l := lease.New(func() { close(released) })
	defer l.Release()
	input, control, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer control.Close()
	output, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- owner.Run(ctx, l, proc.Command{Path: exe, Args: []string{"-test.run=^TestDarwinEscapeProcess$"}, Env: []string{"LINA_PROC_DARWIN_TEST=orphan"}, Stdin: input, Stdout: writer, Stderr: os.Stderr})
		writer.Close()
	}()
	reader := bufio.NewReader(output)
	pid := 0
	for pid == 0 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("descendant readiness: ", err)
		}
		_, _ = fmt.Sscanf(line, "pid=%d", &pid)
	}
	defer killEscaped(pid) // Also clean up the red run.
	// The command keeps running until its stdin closes, so its descendants are
	// live while the lease is checked.
	l.Release()
	select {
	case <-released:
		t.Fatal("lease released while descendants are still running")
	default:
	}
	control.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("command did not join: ", ctx.Err())
	}
	if err := escapedGone(pid); err != nil {
		t.Fatalf("orphaned descendant %d remains after teardown: %v", pid, err)
	}
	select {
	case <-released:
	default:
		t.Fatal("lease retained after the command tree was joined")
	}
	eof := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, reader); eof <- err }()
	select {
	case err := <-eof:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("descendant retained stdout")
	}
}

// TestDarwinEscapeProcess is a helper process, not a test: each mode does its
// work and exits without letting the test framework print its own result.
func TestDarwinEscapeProcess(t *testing.T) {
	switch os.Getenv("LINA_PROC_DARWIN_TEST") {
	case "deny":
		_, sidErr := syscall.Setsid()
		pgErr := syscall.Setpgid(0, 0)
		fmt.Printf("setsid=%v setpgid=%v\n", sidErr, pgErr)
		os.Exit(0)
	case "orphan":
		// Start the intermediate process, which starts the leaf, then wait for
		// the owner to end the command.
		cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinEscapeProcess$")
		cmd.Env = []string{"LINA_PROC_DARWIN_TEST=middle"}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			panic(err)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "middle":
		// Start the leaf with an empty environment and exit, so the kernel
		// reparents it to launchd while the command is still running.
		cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinEscapeProcess$")
		cmd.Env = []string{"LINA_PROC_DARWIN_TEST=leaf"}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			panic(err)
		}
		os.Exit(0)
	case "leaf":
		fmt.Printf("pid=%d\n", os.Getpid())
		time.Sleep(time.Hour)
		os.Exit(0)
	}
}
