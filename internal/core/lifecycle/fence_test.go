// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/lease"
)

var _ lifecycle.Fence = (*lifecycle.ActivityRegistry)(nil)

func TestFenceWaitsForAnEffectEvenAfterRelease(t *testing.T) {
	r := lifecycle.NewRegistry(nil)
	l, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Hard, Reason: "fake effect"})
	if err != nil {
		t.Fatal(err)
	}
	var token lifecycle.Token
	if err := lease.Do(t.Context(), l, func(context.Context) error {
		l.Release()
		var report lifecycle.IdleReport
		token, report = r.Prepare(t.Context())
		if !report.Busy() {
			t.Fatal("in-flight effect appeared idle")
		}
		if err := r.Commit(token); !errors.Is(err, lifecycle.ErrBusy) {
			t.Fatalf("commit: %v", err)
		}
		if _, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Live, Reason: "new stream"}); !errors.Is(err, lifecycle.ErrFenced) {
			t.Fatalf("admission while prepared: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(token); err != nil {
		t.Fatal(err)
	}
	r.Cancel(token)
	if _, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Hard, Reason: "late effect"}); !errors.Is(err, lifecycle.ErrFenced) {
		t.Fatalf("cancel reopened committed fence: %v", err)
	}
}

func TestFenceUnknownBlocksAndCanRecover(t *testing.T) {
	failure := error(fs.ErrPermission)
	r := lifecycle.NewRegistry(func(context.Context) ([]lease.Activity, error) { return nil, failure })
	token, report := r.Prepare(t.Context())
	if report.State != lifecycle.Unknown || !report.Busy() {
		t.Fatalf("report: %+v", report)
	}
	if err := r.Commit(token); !errors.Is(err, lifecycle.ErrBusy) {
		t.Fatalf("commit: %v", err)
	}
	failure = nil
	if err := r.Commit(token); err != nil {
		t.Fatal(err)
	}
}

func TestFenceCancelAndTokenOwnership(t *testing.T) {
	r, other := lifecycle.NewRegistry(nil), lifecycle.NewRegistry(nil)
	token, report := r.Prepare(t.Context())
	if report.Busy() {
		t.Fatal(report)
	}
	if err := other.Commit(token); !errors.Is(err, lifecycle.ErrToken) {
		t.Fatalf("foreign token: %v", err)
	}
	other.Cancel(token)
	if _, report := r.Prepare(t.Context()); !report.Busy() {
		t.Fatal("second prepare replaced fence")
	}
	r.Cancel(token)
	l, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Live, Reason: "resumed"})
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	next, _ := r.Prepare(t.Context())
	r.Cancel(token)
	if err := r.Commit(token); !errors.Is(err, lifecycle.ErrToken) {
		t.Fatalf("stale token: %v", err)
	}
	if err := r.Commit(next); err != nil {
		t.Fatal(err)
	}
}

func TestFenceRechecksCallerOwnedActivity(t *testing.T) {
	var activity []lease.Activity
	r := lifecycle.NewRegistry(func(context.Context) ([]lease.Activity, error) { return activity, nil })
	token, report := r.Prepare(t.Context())
	if report.Busy() {
		t.Fatal(report)
	}
	activity = []lease.Activity{{Class: lease.Live, Reason: "stream"}}
	if err := r.Commit(token); !errors.Is(err, lifecycle.ErrBusy) {
		t.Fatalf("stale idle committed: %v", err)
	}
	activity = []lease.Activity{{Class: lease.Reported, Reason: "queued input"}}
	if err := r.Commit(token); err != nil {
		t.Fatal(err)
	}
}

func TestFenceCancellationAndUninitializedFailClosed(t *testing.T) {
	r := lifecycle.NewRegistry(nil)
	ctx, cancel := context.WithCancel(t.Context())
	token, _ := r.Prepare(ctx)
	cancel()
	if err := r.Commit(token); !errors.Is(err, lifecycle.ErrBusy) {
		t.Fatalf("canceled prepare committed: %v", err)
	}
	r.Cancel(token)
	var zero lifecycle.ActivityRegistry
	var missing *lifecycle.ActivityRegistry
	for _, registry := range []*lifecycle.ActivityRegistry{&zero, missing} {
		token, report := registry.Prepare(t.Context())
		if !report.Busy() {
			t.Fatal("uninitialized fence was idle")
		}
		if err := registry.Commit(token); err == nil {
			t.Fatal("uninitialized fence committed")
		}
		registry.Cancel(token)
	}
}
