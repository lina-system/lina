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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

// The resource coalition XNU records for a process, read here without the
// package under test. PROC_PIDCOALITIONINFO returns five 64-bit ids; the first
// is the resource coalition launchd assigns to a job.
const (
	procInfoCallPidInfo      = 0x2
	procPidCoalitionInfo     = 20
	procPidCoalitionInfoSize = 5 * 8
)

func coalitionOf(t *testing.T, pid int) uint64 {
	t.Helper()
	var record [5]uint64
	got, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, procInfoCallPidInfo,
		uintptr(pid), procPidCoalitionInfo, 0, uintptr(unsafe.Pointer(&record[0])), procPidCoalitionInfoSize)
	if errno != 0 {
		t.Fatalf("coalition of %d: %v", pid, errno)
	}
	if int(got) != procPidCoalitionInfoSize {
		t.Fatalf("coalition of %d: %d bytes", pid, got)
	}
	return record[0]
}

// TestOwnerRunsCommandsAsLaunchdJobs proves the macOS contract. Each command
// runs in a launchd job of its own, so its coalition differs from the owner's;
// a descendant that clears its environment and is orphaned through a reaped
// intermediate parent inherits the job's coalition and is killed with it.
func TestOwnerRunsCommandsAsLaunchdJobs(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
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
		done <- owner.Run(ctx, l, proc.Command{Path: exe, Args: []string{"-test.run=^TestLaunchdJobProcess$", "--", "root"}, Env: []string{"LINA_PROC_JOB_TEST=1"}, Stdin: input, Stdout: writer, Stderr: os.Stderr})
		writer.Close()
	}()
	reader := bufio.NewReader(output)
	var root, leaf int
	orphaned := false
	for root == 0 || leaf == 0 || !orphaned {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("escape readiness: ", err)
		}
		switch {
		case strings.HasPrefix(line, "root="):
			root, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "root=")))
		case strings.HasPrefix(line, "leaf="):
			leaf, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "leaf=")))
		case strings.TrimSpace(line) == "orphaned":
			orphaned = true
		}
	}
	defer killEscaped(leaf)
	job := coalitionOf(t, root)
	if got := coalitionOf(t, leaf); got != job {
		t.Fatalf("orphan coalition = %d, command coalition = %d", got, job)
	}
	if job == coalitionOf(t, os.Getpid()) {
		t.Fatal("the command shares the owner's coalition instead of a job of its own")
	}
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
	if err := escapedGone(leaf); err != nil {
		t.Fatalf("orphaned descendant %d remains after teardown: %v", leaf, err)
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
		t.Fatal("orphaned descendant retained stdout")
	}
}

// TestLaunchdJobProcess is the owned command. The root starts an intermediate
// child and reaps it, so the leaf is reparented to launchd. The intermediate
// starts the leaf in a new session with no environment at all and exits.
func TestLaunchdJobProcess(t *testing.T) {
	mode := ""
	for i := range len(os.Args) - 1 {
		if os.Args[i] == "--" {
			mode = os.Args[i+1]
		}
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(70)
	}
	switch mode {
	case "leaf":
		fmt.Printf("leaf=%d\n", os.Getpid())
		_, _ = io.Copy(io.Discard, os.Stdin)
		time.Sleep(time.Hour)
		os.Exit(0)
	case "fork":
		leaf := exec.Command(exe, "-test.run=^TestLaunchdJobProcess$", "--", "leaf")
		leaf.Env = []string{}
		leaf.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		leaf.Stdout, leaf.Stderr = os.Stdout, os.Stderr
		if err := leaf.Start(); err != nil {
			os.Exit(71)
		}
		os.Exit(0)
	case "root":
		fmt.Printf("root=%d\n", os.Getpid())
		fork := exec.Command(exe, "-test.run=^TestLaunchdJobProcess$", "--", "fork")
		fork.Env = os.Environ()
		fork.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		fork.Stdout, fork.Stderr = os.Stdout, os.Stderr
		if err := fork.Start(); err != nil {
			os.Exit(71)
		}
		if err := fork.Wait(); err != nil {
			os.Exit(71)
		}
		fmt.Println("orphaned")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}
}
