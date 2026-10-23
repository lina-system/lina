// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
)

type executionAdapter struct {
	quotaStopAdapter
	version func() error
}

func (a *executionAdapter) Name() string { return "fixture" }
func (a *executionAdapter) Version(context.Context) (string, error) {
	if a.version != nil {
		return "fixture", a.version()
	}
	return "fixture", nil
}
func (a *executionAdapter) Start(ctx context.Context, l lease.Lease, spec worker.StartSpec) (worker.Thread, error) {
	thread, err := a.quotaStopAdapter.Start(ctx, l, spec)
	event := <-a.events
	event.Finish = &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "worker_failed"}
	a.events <- event
	return thread, err
}
func (a *executionAdapter) Snapshot(_ context.Context, thread worker.Thread) (worker.ThreadSnapshot, error) {
	return worker.ThreadSnapshot{Thread: thread, State: worker.Idle, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.WorkerFailure, Code: "worker_failed"}, EffectsKnown: !a.unknown}, nil
}

func executionReservations(t *testing.T, s *Service, id string) int {
	t.Helper()
	var count int
	if err := s.cfg.DB.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_worker_reservations WHERE worker_id GLOB ?", id+":*").Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func executionFixture(t *testing.T, unknown bool, close func(context.Context) error) (*Service, Record) {
	t.Helper()
	s, r, _, _, _ := switchDispatchFixture(t, false)
	r.Definition.Worker = "fixture"
	r.Recoveries = 1 // A known failure needs a decision, not another attempt.
	if err := s.save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	s.cfg.Workers = []Factory{{Name: "fixture", Explicit: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		return &executionAdapter{quotaStopAdapter: quotaStopAdapter{events: make(chan worker.Event, 1), unknown: unknown}}, close, nil
	}}}
	return s, r
}

// One attempt joins its worker once. A deferred cleanup after delivery must not
// join the process a second time: the live registry owns a kept session, and a
// closed adapter cannot be joined twice.
func TestRunAttemptsKeepsDeferredCloseAfterDelivery(t *testing.T) {
	closes := 0
	s, r := executionFixture(t, false, func(context.Context) error { closes++; return nil })
	l := lease.New(func() {})
	defer l.Release()
	owner := proc.NewOwner()
	defer owner.Close(context.Background())
	e := execution{record: r, lease: l, git: func(dir string, args ...string) (string, error) {
		return s.command(t.Context(), l, owner, dir, args...)
	}}
	defer e.closeDeferred()
	if !s.prepareWorkspace(t.Context(), &e) || !s.runAttempts(t.Context(), &e) {
		t.Fatalf("execution stopped: %+v", e.record)
	}
	if closes != 1 || executionReservations(t, s, r.ID) != 5 {
		t.Fatalf("attempt extraction changed cleanup timing: closes=%d", closes)
	}
	card, err := s.cfg.Work.Get(t.Context(), r.ID)
	if err != nil || card.Receipt != nil || !card.Active {
		t.Fatalf("attempt phase delivered prematurely: %+v %v", card, err)
	}
	s.deliver(t.Context(), &e)
	card, err = s.cfg.Work.Get(t.Context(), r.ID)
	if err != nil || card.Receipt == nil || card.Active || closes != 1 {
		t.Fatalf("delivery lost receipt or closed adapter early: %+v closes=%d err=%v", card, closes, err)
	}
	e.closeDeferred()
	if closes != 1 || executionReservations(t, s, r.ID) != 5 {
		t.Fatalf("deferred adapter cleanup joined the process twice: closes=%d", closes)
	}
}

func TestExecutePreservesDeliveryAndCleanupOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, state, code                                    string
		unknown, lateFailure, receiptFailure, releaseFailure bool
		reservations                                         int
	}{
		{name: "settled", state: "decision", code: "worker_failed"},
		{name: "unknown effects", state: "unknown", code: "worker_failed", unknown: true, reservations: 5},
		{name: "cleanup failure", state: "unknown", code: "cleanup_failed", lateFailure: true, reservations: 5},
		{name: "receipt failure", state: "running", code: "start_failed", receiptFailure: true, reservations: 5},
		{name: "reservation failure", state: "unknown", code: "reservation_unreleased", releaseFailure: true, reservations: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closes := 0
			var s *Service
			var r Record
			s, r = executionFixture(t, tc.unknown, func(context.Context) error {
				closes++
				if executionReservations(t, s, r.ID) != 5 {
					t.Error("reservations released before adapter cleanup")
				}
				card, err := s.cfg.Work.Get(t.Context(), r.ID)
				if err != nil {
					t.Fatal(err)
				}
				// The attempt joins its process before delivery commits the
				// receipt. A failed join is retried after delivery, and a
				// successful one is never repeated.
				if closes == 1 && card.Receipt != nil {
					t.Errorf("adapter joined after the receipt committed: closes=%d", closes)
				}
				if tc.receiptFailure {
					if err := s.cfg.DB.Write(t.Context(), func(tx *canon.Tx) error {
						_, err := tx.ExecContext(t.Context(), "CREATE TRIGGER reject_delivery BEFORE INSERT ON work_completions BEGIN SELECT RAISE(ABORT, 'fixture delivery failure'); END")
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				if tc.lateFailure {
					return errors.New("fixture cleanup failure")
				}
				return nil
			})
			if tc.releaseFailure {
				if err := s.cfg.DB.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), "CREATE TRIGGER reject_release BEFORE DELETE ON work_worker_reservations BEGIN SELECT RAISE(ABORT, 'fixture release failure'); END")
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			s.execute(t.Context(), r)
			records, err := s.records(t.Context())
			if err != nil || len(records) != 1 {
				t.Fatalf("read execution: %+v %v", records, err)
			}
			got := records[0]
			wantCloses := 1
			if tc.lateFailure {
				// The first join failed, so the deferred cleanup retries it.
				wantCloses = 2
			}
			if got.State != tc.state || got.Code != tc.code || closes != wantCloses || executionReservations(t, s, r.ID) != tc.reservations {
				t.Fatalf("outcome changed: state=%s code=%s closes=%d reservations=%d", got.State, got.Code, closes, executionReservations(t, s, r.ID))
			}
			if (tc.lateFailure || tc.receiptFailure || tc.releaseFailure) && got.Snapshot != nil {
				t.Fatal("uncertain cleanup or failed receipt retained a recoverable snapshot")
			}
		})
	}
}

func TestExecuteRetainsFenceWhenParkedAdapterCleanupFails(t *testing.T) {
	closes := 0
	s, r := executionFixture(t, false, func(context.Context) error { return nil })
	s.cfg.Workers[0].Open = func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		a := &executionAdapter{version: func() error {
			return s.ObserveQuota(t.Context(), "fixture", []provider.QuotaRow{{State: provider.QuotaBlocked, Window: provider.UnknownWindow}})
		}}
		return a, func(context.Context) error {
			closes++
			if closes == 1 {
				return errors.New("fixture cleanup failure before launch")
			}
			return nil
		}, nil
	}
	s.execute(t.Context(), r)
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("read execution: %+v %v", records, err)
	}
	got := records[0]
	if got.State != "queued" || got.Wait != "worker_quota" || got.Code != "cleanup_failed" || len(got.Tried) != 0 || closes != 2 || executionReservations(t, s, r.ID) != 5 {
		t.Fatalf("quota wait lost cleanup uncertainty: %+v closes=%d", got, closes)
	}
}

func TestExecutionCleanupPreservesDefersDuringPanic(t *testing.T) {
	var closed []int
	e := execution{deferred: []func(){
		func() { closed = append(closed, 1) },
		func() { closed = append(closed, 2); panic("fixture cleanup panic") },
		func() { closed = append(closed, 3) },
	}}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("cleanup panic was swallowed")
			}
		}()
		e.closeDeferred()
	}()
	if !slices.Equal(closed, []int{3, 2, 1}) {
		t.Fatalf("cleanup skipped an earlier defer after panic: %v", closed)
	}
}
