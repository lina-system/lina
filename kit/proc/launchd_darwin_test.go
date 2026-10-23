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

const (
	procInfoCallPidInfo      = 0x2
	procPidCoalitionInfo     = 20
	procPidCoalitionInfoSize = 5 * 8
)

// coalitionOf reads the resource coalition the kernel records for pid. It is
// deliberately independent of the package under test.
func coalitionOf(t *testing.T, pid int) uint64 {
	t.Helper()
	var record [5]uint64
	got, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO, procInfoCallPidInfo,
		uintptr(pid), procPidCoalitionInfo, 0, uintptr(unsafe.Pointer(&record[0])), procPidCoalitionInfoSize)
	if errno != 0 {
		t.Fatalf("coalition of %d: %v", pid, errno)
	}
	if got != procPidCoalitionInfoSize {
		t.Fatalf("coalition of %d: %d bytes", pid, got)
	}
	return record[0]
}

// TestOwnerOwnsCommandThroughLaunchdJob proves the macOS contract: every
// command runs in a launchd job of its own, descendants inherit that job's
// coalition even when they clear their environment and start a session, and
// teardown removes them.
func TestOwnerOwnsCommandThroughLaunchdJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	owner := proc.NewOwner()
	defer owner.Close(ctx)
	released := false
	l := lease.New(func() { released = true })
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
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		// The command gets no inherited environment. Its intermediate child
		// and the orphaned leaf get none either.
		done <- owner.Run(runCtx, l, proc.Command{
			Path: exe, Args: []string{"-test.run=^TestLaunchdJobProcess$", "--", "root"},
			Env: []string{}, Stdin: input, Stdout: writer, Stderr: os.Stderr,
		})
		writer.Close()
	}()
	lines := make(chan []string, 1)
	go func() {
		reader := bufio.NewReader(output)
		var read []string
		for len(read) < 3 {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			read = append(read, strings.TrimSpace(line))
		}
		lines <- read
	}()
	var root, leaf int
	var orphaned bool
	select {
	case read := <-lines:
		for _, line := range read {
			switch {
			case strings.HasPrefix(line, "root="):
				root, _ = strconv.Atoi(strings.TrimPrefix(line, "root="))
			case strings.HasPrefix(line, "leaf="):
				leaf, _ = strconv.Atoi(strings.TrimPrefix(line, "leaf="))
			case line == "orphaned":
				orphaned = true
			}
		}
	case <-ctx.Done():
		t.Fatal("readiness: ", ctx.Err())
	}
	if root == 0 || leaf == 0 || !orphaned {
		t.Fatalf("readiness: root=%d leaf=%d orphaned=%t", root, leaf, orphaned)
	}
	defer func() { _ = syscall.Kill(leaf, syscall.SIGKILL) }()
	job := coalitionOf(t, root)
	if got := coalitionOf(t, leaf); got != job {
		t.Fatalf("orphan coalition = %d, command coalition = %d", got, job)
	}
	if job == coalitionOf(t, os.Getpid()) {
		t.Fatal("command shares the owner's coalition")
	}
	if err := syscall.Kill(leaf, 0); err != nil {
		t.Fatalf("orphan %d not running before teardown: %v", leaf, err)
	}
	l.Release()
	if released {
		t.Fatal("lease released while the orphan is still running")
	}
	stop()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("terminated command succeeded")
		}
	case <-ctx.Done():
		t.Fatal("command did not join: ", ctx.Err())
	}
	if !released {
		t.Fatal("lease retained after the command tree was joined")
	}
	if err := escapedGone(leaf); err != nil {
		t.Fatalf("orphan %d remains after teardown: %v", leaf, err)
	}
}

// TestLaunchdJobProcess is the owned command. root starts an intermediate
// child, reaps it, and stays alive until teardown. The intermediate starts the
// leaf in a new session with an empty environment and exits, so the leaf has no
// recorded ancestor and no owner record in its environment.
func TestLaunchdJobProcess(t *testing.T) {
	mode := ""
	if at := indexOf(os.Args, "--"); at >= 0 && at+1 < len(os.Args) {
		mode = os.Args[at+1]
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(70)
	}
	switch mode {
	case "leaf":
		fmt.Printf("leaf=%d\n", os.Getpid())
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
		fork.Env = []string{}
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

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}
