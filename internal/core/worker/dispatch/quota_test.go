// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"testing"
	"time"
)

type quotaAdapter struct {
	worker.Adapter
	name string
	rows []provider.QuotaRow
	err  error
}

func (a *quotaAdapter) Name() string                                       { return a.name }
func (a *quotaAdapter) Quota(context.Context) ([]provider.QuotaRow, error) { return a.rows, a.err }

func TestQuotaPersistsAndBlockedWorkersAreNeverOpenedBeforeReset(t *testing.T) {
	db := store(t)
	opens := map[string]int{}
	closed := 0
	factory := func(name string, rows []provider.QuotaRow) Factory {
		return Factory{Name: name, Explicit: true, Chooser: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
			opens[name]++
			return &quotaAdapter{name: name, rows: rows}, func(context.Context) error { closed++; return nil }, nil
		}}
	}
	s := &Service{cfg: Config{DB: db, Workers: []Factory{factory("blocked", nil), factory("unknown", nil)}}}
	reset := time.Now().Add(time.Hour)
	if err := s.ObserveQuota(t.Context(), "blocked", []provider.QuotaRow{{Window: provider.FiveHour, State: provider.QuotaBlocked, ResetsAt: &reset}}); err != nil {
		t.Fatal(err)
	}
	// A new Service has no in-memory quota and must still honor the cache.
	restarted := &Service{cfg: s.cfg}
	l := lease.New(func() {})
	defer l.Release()
	a, close, err := restarted.selectWorker(t.Context(), l, Record{})
	if err != nil || a == nil || a.Name() != "unknown" || opens["blocked"] != 0 {
		t.Fatalf("blocked opened or unknown lost: %v %v", opens, err)
	}
	close(t.Context())
	reset = time.Now().Add(-time.Second)
	if err := s.ObserveQuota(t.Context(), "blocked", []provider.QuotaRow{{Window: provider.FiveHour, State: provider.QuotaBlocked, ResetsAt: &reset}}); err != nil {
		t.Fatal(err)
	}
	a, close, err = restarted.selectWorker(t.Context(), l, Record{Tried: []string{"unknown"}})
	if err != nil || a == nil || a.Name() != "blocked" || opens["blocked"] != 1 {
		t.Fatalf("explicit reset ignored: %v %v", opens, err)
	}
	close(t.Context())
	// A fresh quota stop must not reuse an expired reset from an old observation.
	if err = s.park(t.Context(), &quotaAdapter{name: "blocked"}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.quota(t.Context(), "blocked")
	if err != nil || len(rows) != 1 || rows[0].State != provider.QuotaBlocked || rows[0].ResetsAt != nil {
		t.Fatalf("quota stop invented reset: %+v %v", rows, err)
	}
	if closed != 2 {
		t.Fatalf("probe leak: closes=%d", closed)
	}
}

func TestQuotaChangeBetweenSelectionAndReservation(t *testing.T) {
	db := store(t)
	s := &Service{cfg: Config{DB: db}}
	r := Record{ID: "task", State: "running", Packet: "immutable"}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES('task','turn','repo','running',?)", mustJSON(r))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	if err := s.ObserveQuota(t.Context(), "codex", []provider.QuotaRow{{Window: provider.FiveHour, State: provider.QuotaBlocked, ResetsAt: &reset}}); err != nil {
		t.Fatal(err)
	}
	if err := s.reserveWorker(t.Context(), &r, "codex"); !errors.Is(err, errWorkerParked) || len(r.Tried) != 0 {
		t.Fatalf("blocked reservation spent an attempt: %+v %v", r, err)
	}
}

func TestLateQuotaSnapshotCannotClearNewerStop(t *testing.T) {
	s := &Service{cfg: Config{DB: store(t)}}
	now := time.Now()
	if err := s.ObserveQuota(t.Context(), "codex", []provider.QuotaRow{{Window: provider.UnknownWindow, State: provider.QuotaBlocked, Source: provider.ErrorCode, Observed: now}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveQuota(t.Context(), "codex", []provider.QuotaRow{{Window: provider.UnknownWindow, State: provider.QuotaOK, Source: provider.Endpoint, Observed: now.Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.quota(t.Context(), "codex")
	if err != nil || len(rows) != 1 || rows[0].State != provider.QuotaBlocked {
		t.Fatalf("old snapshot cleared quota stop: %+v %v", rows, err)
	}
}
