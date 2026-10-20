// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/lease"
)

var _ lifecycle.Registry = (*lifecycle.ActivityRegistry)(nil)

func TestIdleClassesAndIndependentLeases(t *testing.T) {
	for _, class := range []lease.Class{lease.Hard, lease.Live, lease.Reported} {
		t.Run(string(class), func(t *testing.T) {
			r := lifecycle.NewRegistry(nil)
			if report := r.Idle(t.Context()); report.State != lifecycle.Idle || report.Busy() {
				t.Fatalf("empty: %+v", report)
			}
			activity := lease.Activity{Class: class, Reason: "test activity"}
			first, err := r.Acquire(t.Context(), activity)
			if err != nil {
				t.Fatal(err)
			}
			second, err := r.Acquire(t.Context(), activity)
			if err != nil {
				t.Fatal(err)
			}
			report := r.Idle(t.Context())
			if report.Busy() != (class != lease.Reported) {
				t.Fatalf("class: %+v", report)
			}
			if class == lease.Reported {
				if len(report.Reported) != 2 || len(report.Blocking) != 0 {
					t.Fatalf("reported: %+v", report)
				}
			} else if report.State != lifecycle.Busy || len(report.Blocking) != 2 {
				t.Fatalf("blocking: %+v", report)
			}
			first.Release()
			first.Release()
			report = r.Idle(t.Context())
			if len(report.Blocking)+len(report.Reported) != 1 {
				t.Fatalf("independent lease lost: %+v", report)
			}
			second.Release()
			if report := r.Idle(t.Context()); report.Busy() || len(report.Reported) != 0 {
				t.Fatalf("released: %+v", report)
			}
		})
	}
}

func TestUnreadableRegistryCountsAsBusy(t *testing.T) {
	for _, failure := range []error{fs.ErrPermission, fs.ErrNotExist, context.DeadlineExceeded} {
		r := lifecycle.NewRegistry(func(context.Context) ([]lease.Activity, error) { return nil, failure })
		report := r.Idle(t.Context())
		if report.State != lifecycle.Unknown || !report.Busy() || !errors.Is(report.Err, failure) {
			t.Fatalf("unreadable: %+v", report)
		}
	}
	var zero lifecycle.ActivityRegistry
	var missing *lifecycle.ActivityRegistry
	for _, r := range []*lifecycle.ActivityRegistry{&zero, missing} {
		if report := r.Idle(t.Context()); report.State != lifecycle.Unknown || !report.Busy() {
			t.Fatalf("uninitialized: %+v", report)
		}
		if _, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Hard, Reason: "effect"}); err == nil {
			t.Fatal("uninitialized registry acquired lease")
		}
	}
	if !(lifecycle.IdleReport{}).Busy() {
		t.Fatal("zero report was idle")
	}
	if !(lifecycle.IdleReport{State: lifecycle.State(255)}).Busy() {
		t.Fatal("invalid report was idle")
	}
}

func TestReadFailureRecoveryAndNotifications(t *testing.T) {
	failure := fs.ErrPermission
	r := lifecycle.NewRegistry(func(context.Context) ([]lease.Activity, error) { return nil, failure })
	if !r.Idle(t.Context()).Busy() {
		t.Fatal("unreadable registry was idle")
	}
	failure = nil
	r.Notify()
	expectEdge(t, r)
	if report := r.Idle(t.Context()); report.State != lifecycle.Idle {
		t.Fatalf("recovered: %+v", report)
	}
	for range 10 {
		r.Notify()
	}
	expectEdge(t, r)
	select {
	case <-r.Edges():
		t.Fatal("edges were not coalesced")
	default:
	}
	l, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Live, Reason: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	expectEdge(t, r)
	l.Release()
	expectEdge(t, r)
	l.Release()
	select {
	case <-r.Edges():
		t.Fatal("duplicate release emitted an edge")
	default:
	}
}

func TestSnapshotIsValidatedSortedAndIndependent(t *testing.T) {
	activities := []lease.Activity{{Class: lease.Reported, Reason: "timer"}, {Class: lease.Live, Reason: "stream"}}
	r := lifecycle.NewRegistry(func(context.Context) ([]lease.Activity, error) { return activities, nil })
	l, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Hard, Reason: "send"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	want := []lease.Activity{{Class: lease.Hard, Reason: "send"}, {Class: lease.Live, Reason: "stream"}}
	if report := r.Idle(t.Context()); !reflect.DeepEqual(report.Blocking, want) || len(report.Reported) != 1 {
		t.Fatalf("snapshot: %+v", report)
	}
	copy := r.Idle(t.Context())
	copy.Blocking[0].Reason = "mutated"
	copy.Reported[0].Reason = "mutated"
	if report := r.Idle(t.Context()); !reflect.DeepEqual(report.Blocking, want) || report.Reported[0].Reason != "timer" {
		t.Fatalf("aliased report: %+v", report)
	}
	for _, bad := range []lease.Activity{{}, {Class: "future", Reason: "unknown"}, {Class: lease.Hard, Reason: " "}} {
		activities = []lease.Activity{bad}
		if report := r.Idle(t.Context()); report.State != lifecycle.Unknown || !report.Busy() || report.Err == nil {
			t.Fatalf("invalid snapshot: %+v", report)
		}
		if _, err := r.Acquire(t.Context(), bad); err == nil {
			t.Fatal("invalid activity acquired")
		}
	}
}

func TestCancellationNeverForgetsAnEffect(t *testing.T) {
	r := lifecycle.NewRegistry(nil)
	ctx, cancel := context.WithCancel(t.Context())
	l, err := r.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "send"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if report := r.Idle(ctx); report.State != lifecycle.Unknown || !errors.Is(report.Err, context.Canceled) {
		t.Fatalf("cancelled read: %+v", report)
	}
	if _, err := r.Acquire(ctx, lease.Activity{Class: lease.Hard, Reason: "send"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire: %v", err)
	}
	if !r.Idle(t.Context()).Busy() {
		t.Fatal("cancellation forgot effect")
	}
	if err := lease.Do(t.Context(), l, func(context.Context) error {
		l.Release()
		if !r.Idle(t.Context()).Busy() {
			t.Fatal("release forgot running call")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if r.Idle(t.Context()).Busy() {
		t.Fatal("finished call stayed busy")
	}
}

func TestExternalHTTPCallKeepsRegistryBusy(t *testing.T) {
	r := lifecycle.NewRegistry(nil)
	l, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Hard, Reason: "send"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		l.Release()
		if report := r.Idle(request.Context()); report.State != lifecycle.Busy {
			t.Errorf("request still outside: %+v", report)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := lease.Do(t.Context(), l, func(ctx context.Context) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
		if err != nil {
			return err
		}
		response, err := server.Client().Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Errorf("response: %s", response.Status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if report := r.Idle(t.Context()); report.State != lifecycle.Idle {
		t.Fatalf("request completed: %+v", report)
	}
}

func TestCancellationDuringSnapshotIsUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := lifecycle.NewRegistry(func(got context.Context) ([]lease.Activity, error) {
		if got != ctx {
			t.Error("reader context was replaced")
		}
		cancel()
		return nil, nil
	})
	if report := r.Idle(ctx); report.State != lifecycle.Unknown || !report.Busy() || !errors.Is(report.Err, context.Canceled) {
		t.Fatalf("canceled snapshot: %+v", report)
	}
}

func TestConcurrentActivity(t *testing.T) {
	r := lifecycle.NewRegistry(nil)
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			l, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Hard, Reason: "send"})
			if err != nil {
				t.Error(err)
				return
			}
			defer l.Release()
			if report := r.Idle(t.Context()); report.State != lifecycle.Busy {
				t.Errorf("active: %+v", report)
			}
		})
	}
	workers.Wait()
	if r.Idle(t.Context()).Busy() {
		t.Fatal("completed activities stayed busy")
	}
}

func expectEdge(t *testing.T, r lifecycle.Registry) {
	t.Helper()
	select {
	case <-r.Edges():
	default:
		t.Fatal("missing registry edge")
	}
}
