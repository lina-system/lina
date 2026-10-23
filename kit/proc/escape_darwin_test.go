// SPDX-License-Identifier: Apache-2.0

package proc_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

// Darwin records the parent identity at fork, so a child that starts its own
// session and clears its environment is still owned. The inherited environment
// token alone cannot see it.
func TestOwnerReapsClearedEnvironmentEscape(t *testing.T) {
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
	defer writer.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- owner.Run(ctx, l, proc.Command{Path: exe, Args: []string{"-test.run=^TestClearedEnvironmentProcess$"}, Env: []string{"LINA_PROC_CLEARED_TEST=1"}, Stdin: input, Stdout: writer, Stderr: os.Stderr})
		writer.Close()
	}()
	reader := bufio.NewReader(output)
	pid := 0
	for pid == 0 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("escape readiness: ", err)
		}
		_, _ = fmt.Sscanf(line, "pid=%d", &pid)
	}
	defer killEscaped(pid)
	l.Release()
	select {
	case <-released:
		t.Fatal("lease released while descendants are still running")
	default:
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("terminated command succeeded")
		}
	case <-ctx.Done():
		t.Fatal("command did not join: ", ctx.Err())
	}
	if err := escapedGone(pid); err != nil {
		t.Fatalf("escaped descendant %d remains after teardown: %v", pid, err)
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
		t.Fatal("escaped descendant retained stdout")
	}
}

func TestClearedEnvironmentProcess(t *testing.T) {
	if os.Getenv("LINA_PROC_CLEARED_TEST") != "1" {
		return
	}
	// The child leaves the process group and session and inherits nothing.
	cmd := exec.Command(os.Args[0], "-test.run=^TestEscapingProcess$", "--", "leaf", "setsid")
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	fmt.Printf("pid=%s\n", strconv.Itoa(cmd.Process.Pid))
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}
