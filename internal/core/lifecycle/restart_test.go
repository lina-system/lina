// SPDX-License-Identifier: Apache-2.0

package lifecycle_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/lease"
)

func restartStore(t *testing.T, root string) (*ctlfiles.Store, uint64) {
	t.Helper()
	s, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := s.MintEpoch(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return s, epoch
}

func restartRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func restarter(t *testing.T, s lifecycle.RestartStore, epoch uint64, r *lifecycle.ActivityRegistry) *lifecycle.Restarter {
	t.Helper()
	x, err := lifecycle.OpenRestarter(s, epoch, r)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestRestartModesNeverExitDuringAnEffect(t *testing.T) {
	for _, mode := range []string{"", "drain"} {
		t.Run(mode, func(t *testing.T) {
			s, epoch := restartStore(t, restartRoot(t))
			r := lifecycle.NewRegistry(nil)
			x := restarter(t, s, epoch, r)
			if err := x.Ready(); err != nil {
				t.Fatal(err)
			}
			l, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Hard, Reason: "fake effect"})
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Do(t.Context(), l, func(context.Context) error {
				l.Release()
				request, err := x.Request(t.Context(), mode)
				if err != nil || !request.Owed() {
					t.Fatalf("request: %+v %v", request, err)
				}
				exit, report, err := x.Step(t.Context())
				if exit || err != nil || !report.Busy() {
					t.Fatalf("in flight: %v %+v %v", exit, report, err)
				}
				next, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Live, Reason: "next stream"})
				if mode == "drain" {
					if !errors.Is(err, lifecycle.ErrFenced) {
						t.Fatalf("drain admission: %v", err)
					}
				} else {
					if err != nil {
						t.Fatalf("when_idle stopped admission: %v", err)
					}
					next.Release()
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			exit, report, err := x.Step(t.Context())
			if !exit || err != nil || report.Busy() {
				t.Fatalf("settled: %v %+v %v", exit, report, err)
			}
			status := x.Status()
			if !status.Owed() || status.CommittedEpoch != "1" {
				t.Fatalf("commit receipt: %+v", status)
			}
		})
	}
}

func TestRestartCrashOwedAndDischargeOnlyAfterReady(t *testing.T) {
	root := restartRoot(t)
	s, epoch := restartStore(t, root)
	x := restarter(t, s, epoch, lifecycle.NewRegistry(nil))
	request, err := x.Request(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	// Reopen without orderly discharge, as after the writer process disappears.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, epoch = restartStore(t, root)
	x = restarter(t, s, epoch, lifecycle.NewRegistry(nil))
	if got := x.Status(); got.ID != request.ID || !got.Owed() {
		t.Fatalf("lost request: %+v", got)
	}
	if exit, _, err := x.Step(t.Context()); exit || err != nil {
		t.Fatalf("exit before ready: %v %v", exit, err)
	}
	if err := x.Ready(); err != nil {
		t.Fatal(err)
	}
	if !x.Status().Owed() {
		t.Fatal("crash discharged uncommitted request")
	}
	if exit, _, err := x.Step(t.Context()); !exit || err != nil {
		t.Fatalf("planned restart: %v %v", exit, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, epoch = restartStore(t, root)
	x = restarter(t, s, epoch, lifecycle.NewRegistry(nil))
	if !x.Status().Owed() {
		t.Fatal("startup discharged before ready")
	}
	if err := x.Ready(); err != nil {
		t.Fatal(err)
	}
	if got := x.Status(); got.Owed() || got.DischargedEpoch != "3" || got.ID != request.ID {
		t.Fatalf("discharge: %+v", got)
	}
	if exit, _, err := x.Step(t.Context()); exit || err != nil {
		t.Fatalf("restart loop: %v %v", exit, err)
	}
}

func TestRestartUnknownAndCanceledNeverCommit(t *testing.T) {
	s, epoch := restartStore(t, restartRoot(t))
	failure := errors.New("unreadable activity")
	r := lifecycle.NewRegistry(func(context.Context) ([]lease.Activity, error) { return nil, failure })
	x := restarter(t, s, epoch, r)
	if _, err := x.Request(t.Context(), "drain"); err != nil {
		t.Fatal(err)
	}
	if err := x.Ready(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if exit, report, err := x.Step(t.Context()); exit || err != nil || report.State != lifecycle.Unknown {
			t.Fatalf("unknown: %v %+v %v", exit, report, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if exit, _, err := x.Step(ctx); exit || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v %v", exit, err)
	}
	failure = nil
	if exit, _, err := x.Step(t.Context()); !exit || err != nil {
		t.Fatalf("recovered: %v %v", exit, err)
	}
}

func TestRestartRequestIsIdempotentAndDoesNotMintEpoch(t *testing.T) {
	s, epoch := restartStore(t, restartRoot(t))
	x := restarter(t, s, epoch, lifecycle.NewRegistry(nil))
	first, err := x.Request(t.Context(), "")
	if err != nil || first.Mode != "when_idle" {
		t.Fatalf("default: %+v %v", first, err)
	}
	second, err := x.Request(t.Context(), "when_idle")
	if err != nil || second != first {
		t.Fatalf("duplicate: %+v %v", second, err)
	}
	if _, err := x.Request(t.Context(), "drain"); !errors.Is(err, lifecycle.ErrPending) {
		t.Fatalf("changed pending mode: %v", err)
	}
	if _, err := x.Request(t.Context(), "force"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	if got, err := s.Epoch(); err != nil || got != epoch {
		t.Fatalf("request minted epoch: %d %v", got, err)
	}
}

type failingRestartStore struct {
	*ctlfiles.Store
	fail bool
}

func (s *failingRestartStore) WriteRestart(r ctlfiles.RestartRequest) error {
	if s.fail {
		return os.ErrPermission
	}
	return s.Store.WriteRestart(r)
}

func TestRestartStorageFailureCannotAcknowledgeOrExit(t *testing.T) {
	for _, boundary := range []string{"request", "commit"} {
		t.Run(boundary, func(t *testing.T) {
			s, epoch := restartStore(t, restartRoot(t))
			fault := &failingRestartStore{Store: s, fail: boundary == "request"}
			r := lifecycle.NewRegistry(nil)
			x := restarter(t, fault, epoch, r)
			if err := x.Ready(); err != nil {
				t.Fatal(err)
			}
			request, err := x.Request(t.Context(), "")
			if boundary == "request" {
				if err == nil || request.ID != "" {
					t.Fatalf("unpersisted acknowledgment: %+v %v", request, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			fault.fail = true
			if exit, _, err := x.Step(t.Context()); exit || err == nil {
				t.Fatalf("failed receipt exited: %v %v", exit, err)
			}
			if _, err := r.Acquire(t.Context(), lease.Activity{Class: lease.Hard, Reason: "late effect"}); !errors.Is(err, lifecycle.ErrFenced) {
				t.Fatalf("failed commit reopened admission: %v", err)
			}
		})
	}
}

func TestFailedDischargeRemainsOwed(t *testing.T) {
	root := restartRoot(t)
	s, epoch := restartStore(t, root)
	x := restarter(t, s, epoch, lifecycle.NewRegistry(nil))
	if _, err := x.Request(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if err := x.Ready(); err != nil {
		t.Fatal(err)
	}
	if exit, _, err := x.Step(t.Context()); !exit || err != nil {
		t.Fatalf("commit: %v %v", exit, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, epoch = restartStore(t, root)
	x = restarter(t, &failingRestartStore{Store: s, fail: true}, epoch, lifecycle.NewRegistry(nil))
	if err := x.Ready(); err == nil {
		t.Fatal("failed discharge reached ready")
	}
	if !x.Status().Owed() {
		t.Fatal("failed discharge acknowledged completion")
	}
	if saved, err := s.ReadRestart(); err != nil || !saved.Owed() {
		t.Fatalf("failed discharge changed receipt: %+v %v", saved, err)
	}
}

func TestRestartRunWakesWhenUnknownActivityRecovers(t *testing.T) {
	s, epoch := restartStore(t, restartRoot(t))
	read := make(chan struct{}, 1)
	var recovered atomic.Bool
	r := lifecycle.NewRegistry(func(context.Context) ([]lease.Activity, error) {
		if !recovered.Load() {
			select {
			case read <- struct{}{}:
			default:
			}
			return nil, os.ErrPermission
		}
		return nil, nil
	})
	x := restarter(t, s, epoch, r)
	if _, err := x.Request(t.Context(), "drain"); err != nil {
		t.Fatal(err)
	}
	if err := x.Ready(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- x.Run(ctx) }()
	select {
	case <-read:
	case err := <-done:
		t.Fatalf("unknown activity exited: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	recovered.Store(true)
	r.Notify()
	select {
	case err := <-done:
		if !errors.Is(err, lifecycle.ErrRestart) {
			t.Fatalf("run: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
