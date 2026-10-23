// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// PR_SET_PDEATHSIG follows the OS thread that forked the process, not the
// lifetime of the Go process. A fixture launcher must survive that thread's
// retirement while the test still owns its Core.
func TestWorkerLoadSurvivesLaunchingThreadExit(t *testing.T) {
	fixture := t.TempDir()
	load := newWorkerLoad(t, fixture)
	ready, release := filepath.Join(fixture, "ready"), filepath.Join(fixture, "release")
	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	defer cancel()
	p := &process{command: exec.CommandContext(ctx, "/bin/sh", "-c", `printf ready > "$1"; while [ ! -f "$2" ]; do sleep 0.01; done`, "fixture", ready, release), done: make(chan struct{})}
	p.command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + fixture, "LANG=C"}
	load.wrap(t, p)
	retire := make(chan struct{})
	defer func() {
		select {
		case <-retire:
		default:
			close(retire)
		}
	}()
	started := make(chan int, 1)
	var launch func()
	launch = func() {
		runtime.LockOSThread()
		if syscall.Gettid() == os.Getpid() {
			// Go parks its initial thread instead of retiring it. Keep it
			// occupied so the actual launch uses a disposable runtime thread.
			go launch()
			<-retire
			runtime.UnlockOSThread()
			return
		}
		p.start(t)
		started <- syscall.Gettid()
		<-retire
		// Returning without UnlockOSThread makes Go retire this OS thread.
	}
	go launch()
	var tid int
	select {
	case tid = <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitPath := func(path string, want bool) {
		t.Helper()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			_, err := os.Stat(path)
			if (err == nil) == want && (err == nil || os.IsNotExist(err)) {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("path condition: %s: %v", path, ctx.Err())
			case <-ticker.C:
			}
		}
	}
	waitPath(ready, true)
	close(retire)
	waitPath(filepath.Join("/proc/self/task", strconv.Itoa(tid)), false)
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	p.wait(t, 0)
}
