// SPDX-License-Identifier: Apache-2.0
package claudeproc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "proc-job" {
		os.Exit(proc.JobMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

type enteredReader struct {
	io.Reader
	entered chan struct{}
}

func (r *enteredReader) Read(p []byte) (int, error) {
	select {
	case <-r.entered:
	default:
		close(r.entered)
	}
	return r.Reader.Read(p)
}

func TestSilentRuntimeCancellation(t *testing.T) {
	for _, mode := range []string{"stream", "stream-deadline", "next", "deadline", "already-ended"} {
		t.Run(mode, func(t *testing.T) {
			program := filepath.Join(t.TempDir(), "silent.sh")
			// A readiness file proves the child consumed start. It emits no stdout.
			if err := os.WriteFile(program, []byte("# SPDX-License-Identifier: Apache-2.0\nread -r start\nprintf '%s %s\\n' \"$$\" \"$PPID\" > \"$0.pids\"\nexec /bin/sleep 60\n"), 0600); err != nil {
				t.Fatal(err)
			}
			life, cancelLife := context.WithCancel(t.Context())
			defer cancelLife()
			if mode == "stream-deadline" {
				var stop context.CancelFunc
				life, stop = context.WithTimeout(life, 2*time.Second)
				defer stop()
			}
			l := lease.New(func() {})
			defer l.Release()
			adapter := &Adapter{Node: "/bin/sh", Program: program}
			opened, err := adapter.Stream(life, l, provider.Request{Model: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			s := opened.(*stream)
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			})
			var pid, supervisor int
			ready, stopReady := context.WithTimeout(t.Context(), 5*time.Second)
			defer stopReady()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				data, err := os.ReadFile(program + ".pids")
				if err == nil {
					if n, _ := fmt.Sscanf(string(data), "%d %d", &pid, &supervisor); n == 2 {
						break
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				select {
				case <-ready.Done():
					t.Fatal("silent runtime did not consume start")
				case <-tick.C:
				}
			}
			next, cancelNext := context.WithCancel(t.Context())
			defer cancelNext()
			want := context.Canceled
			if mode == "stream-deadline" {
				want = context.DeadlineExceeded
			}
			if mode == "deadline" {
				var stop context.CancelFunc
				next, stop = context.WithTimeout(next, 100*time.Millisecond)
				defer stop()
				want = context.DeadlineExceeded
			}
			if mode == "already-ended" {
				cancelNext()
			}
			entered := make(chan struct{})
			s.scanner = bufio.NewScanner(&enteredReader{Reader: s.stdout, entered: entered})
			result := make(chan error, 1)
			go func() { _, err := s.Next(next); result <- err }()
			if mode == "stream" || mode == "next" {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("Next did not begin reading")
				}
				if mode == "stream" {
					cancelLife()
				} else {
					cancelNext()
				}
			}
			if mode == "stream-deadline" {
				<-life.Done()
			}
			select {
			case err := <-result:
				if !errors.Is(err, want) {
					t.Fatalf("Next error = %v, want %v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Next stayed blocked after cancellation without Close")
			}
			// Join and inspect the actual PIDs before cleanup calls Close.
			select {
			case <-s.done:
			case <-time.After(5 * time.Second):
				t.Fatal("process owner did not finish without Close")
			}
			for _, id := range []int{pid, supervisor} {
				if id <= 1 {
					t.Fatalf("invalid child PID %d", id)
				}
				if err := syscall.Kill(id, 0); !errors.Is(err, syscall.ESRCH) {
					t.Errorf("process %d survived cancellation: %v", id, err)
				}
			}
		})
	}
}
