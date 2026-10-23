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

// The deepest descendant clears its environment and outlives the intermediate
// parent the kernel reaps and replaces with launchd, so neither the owner token
// nor the parent chain names it. It cannot leave the process group, so teardown
// still reaps it.
func TestOwnerReapsOrphanedClearedEnvironmentDescendant(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	owner := proc.NewOwner()
	defer owner.Close(ctx)
	released := make(chan struct{})
	l := lease.New(func() { close(released) })
	defer l.Release()
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
		done <- owner.Run(ctx, l, proc.Command{Path: exe, Args: []string{"-test.run=^TestDarwinEscapeProcess$"}, Env: []string{"LINA_PROC_DARWIN_TEST=orphan"}, Stdout: writer, Stderr: os.Stderr})
		writer.Close()
	}()
	reader := bufio.NewReader(output)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal("descendant readiness: ", err)
	}
	pid := 0
	for _, field := range strings.Fields(line) {
		_, _ = fmt.Sscanf(field, "pid=%d", &pid)
	}
	if pid == 0 || !strings.Contains(line, "setsid=operation not permitted") {
		t.Fatalf("descendant readiness = %q", line)
	}
	defer killEscaped(pid) // Also clean up the red run.
	select {
	case <-released:
		t.Fatal("lease released while descendants are still running")
	default:
	}
	// The intermediate parent has exited, so the kernel has already handed the
	// descendant to launchd; the command itself completes here.
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
		// Start a descendant with a cleared environment, then exit so the
		// kernel reparents it to launchd before teardown.
		cmd := exec.Command(os.Args[0], "-test.run=^TestDarwinEscapeProcess$")
		cmd.Env = []string{"LINA_PROC_DARWIN_TEST=leaf"}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			panic(err)
		}
		os.Exit(0)
	case "leaf":
		_, err := syscall.Setsid()
		fmt.Printf("setsid=%v pid=%d\n", err, os.Getpid())
		time.Sleep(time.Hour)
		os.Exit(0)
	}
}
