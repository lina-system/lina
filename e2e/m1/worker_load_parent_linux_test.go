// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestWorkerLoadParentThread retires the caller thread after Bubblewrap is
// ready and requires a normal child exit, with parent-death cleanup enabled.
func TestWorkerLoadParentThread(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	defer cancel()
	root := t.TempDir()
	h := &harness{t: t, ctx: ctx, binary: "/bin/sh", root: root}
	p := h.command("-c", `printf R; read line; test "$line" = continue`)
	ready, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	defer readyWriter.Close()
	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer inputWriter.Close()
	p.command.Stdin, p.command.Stdout = input, readyWriter
	newWorkerLoad(t, root).wrap(t, p)
	launched := make(chan int, 1)
	var callers sync.WaitGroup
	t.Cleanup(func() { cancel(); callers.Wait() })
	var launch func()
	launch = func() {
		defer callers.Done()
		// Exiting without UnlockOSThread retires this caller's OS thread.
		// The launcher must keep its own parent thread alive until Wait.
		runtime.LockOSThread()
		if syscall.Gettid() == os.Getpid() {
			// Go parks m0 instead of terminating the process's main thread.
			// Hold it here so the actual caller uses a retireable thread.
			t.Logf("holding main thread %d while another caller launches", os.Getpid())
			callers.Add(1)
			go launch()
			<-ctx.Done()
			runtime.UnlockOSThread()
			return
		}
		p.start(t)
		_ = readyWriter.Close()
		var marker [1]byte
		if _, err := io.ReadFull(ready, marker[:]); err != nil {
			t.Error(err)
			launched <- 0
			return
		}
		launched <- syscall.Gettid()
	}
	callers.Add(1)
	go launch()
	var tid int
	select {
	case tid = <-launched:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if tid == 0 {
		t.Fatal("launcher never became ready")
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		_, err := os.Stat(fmt.Sprintf("/proc/self/task/%d", tid))
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			stat, _ := os.ReadFile(fmt.Sprintf("/proc/self/task/%d/stat", tid))
			t.Fatalf("caller thread %d did not exit (child %d): %s: %v", tid, p.command.Process.Pid, stat, ctx.Err())
		}
	}
	// Require a real response and normal exit after the caller thread has
	// disappeared. No sleep or increased startup timeout can satisfy this.
	if _, err := io.WriteString(inputWriter, "continue\n"); err != nil {
		t.Logf("launcher input: %v", err)
	}
	_ = inputWriter.Close()
	p.wait(t, 0)
}
