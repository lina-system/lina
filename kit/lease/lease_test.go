// SPDX-License-Identifier: Apache-2.0

package lease_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
)

func TestExternalCallRequiresLease(t *testing.T) {
	for _, fixture := range []string{"with_lease", "without_lease"} {
		t.Run(fixture, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "go", "test", "-mod=readonly", "./testdata/"+fixture)
			output, err := cmd.CombinedOutput()
			if fixture == "with_lease" {
				if err != nil {
					t.Fatalf("valid external call: %v\n%s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), "not enough arguments in call to lease.Do") {
				t.Fatalf("expected missing lease compile error, got %v\n%s", err, output)
			}
		})
	}
}

func TestInvalidAndReleasedLeasesCannotCallOutside(t *testing.T) {
	ctx := context.Background()
	called := false
	outside := func(context.Context) error { called = true; return nil }
	if err := lease.Do(ctx, lease.Lease{}, outside); !errors.Is(err, lease.ErrInvalid) {
		t.Fatalf("zero lease: %v", err)
	}
	var releases atomic.Int32
	l := lease.New(func() { releases.Add(1) })
	copy := l
	l.Release()
	copy.Release()
	if err := lease.Do(ctx, copy, outside); !errors.Is(err, lease.ErrReleased) {
		t.Fatalf("released copy: %v", err)
	}
	if called || releases.Load() != 1 {
		t.Fatalf("called=%v, releases=%d", called, releases.Load())
	}
	var zero lease.Lease
	zero.Release()
}

func TestReleaseWaitsForEveryExternalCall(t *testing.T) {
	var releases atomic.Int32
	l := lease.New(func() { releases.Add(1) })
	entered := make(chan struct{}, 2)
	finish := make(chan struct{})
	var calls sync.WaitGroup
	for range 2 {
		calls.Go(func() {
			if err := lease.Do(t.Context(), l, func(context.Context) error {
				entered <- struct{}{}
				<-finish
				return nil
			}); err != nil {
				t.Error(err)
			}
		})
	}
	<-entered
	<-entered
	l.Release()
	if releases.Load() != 0 {
		t.Error("release forgot calls still in flight")
	}
	if err := lease.Do(t.Context(), l, func(context.Context) error { t.Error("new call after release"); return nil }); !errors.Is(err, lease.ErrReleased) {
		t.Errorf("new call: %v", err)
	}
	close(finish)
	calls.Wait()
	if releases.Load() != 1 {
		t.Fatalf("releases=%d", releases.Load())
	}
}

func TestCallOutcomeAndCancellation(t *testing.T) {
	var releases int
	l := lease.New(func() { releases++ })
	defer l.Release()
	want := errors.New("external failure")
	ctx, cancel := context.WithCancel(t.Context())
	if err := lease.Do(ctx, l, func(got context.Context) error {
		if got != ctx {
			t.Error("context was replaced")
		}
		cancel()
		if releases != 0 {
			t.Error("cancellation released live work")
		}
		return want
	}); !errors.Is(err, want) {
		t.Fatalf("outcome: %v", err)
	}
	if err := lease.Do(ctx, l, func(context.Context) error { t.Error("called after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	func() {
		defer func() {
			if recover() != "outside panic" {
				t.Error("panic was swallowed")
			}
		}()
		_ = lease.Do(t.Context(), l, func(context.Context) error { panic("outside panic") })
	}()
	l.Release()
	if releases != 1 {
		t.Fatalf("release after panic: %d", releases)
	}
}
