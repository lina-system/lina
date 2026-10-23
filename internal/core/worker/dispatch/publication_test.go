// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestQueuedCancelSettlesBeforePublicationAuthority(t *testing.T) {
	db := store(t)
	record := Record{ID: "published-work", State: "queued", Canceled: true}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES(?,?,?,?,?)", record.ID, "turn", "repo", record.State, mustJSON(record))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	checks := 0
	s := &Service{ctx: t.Context(), cfg: Config{DB: db, AuthorizeExecution: func(context.Context, Record) (bool, error) { checks++; return false, nil }}, running: map[string]bool{}}
	s.sync()
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	if got := records[0]; got.State != "finished" || got.Code != "cancelled" || got.Attempt != 0 || checks != 0 {
		t.Fatalf("never-launched cancellation became uncertain: %+v authority checks=%d", got, checks)
	}
}

func TestPublicationWorkerDenialRetainsTerminalReceipt(t *testing.T) {
	s, r, _, _, _ := switchDispatchFixture(t, false)
	fixture := t.TempDir()
	t.Setenv("LINA_WORKER_FIXTURE", fixture)
	t.Setenv("LINA_WORKER_CASE", "denied")
	script, err := os.ReadFile("../../../../e2e/m3/a2a-worker.mjs")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(fixture, "codex")
	if err = os.WriteFile(binary, script, 0700); err != nil {
		t.Fatal(err)
	}
	r.Definition.Worker = ""
	if err = s.save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	s.cfg.Workers = []Factory{CodexFactory(binary)}
	s.cfg.Bind = func(_ Record, a worker.Adapter, th worker.Thread, _, _ string) (func(worker.Event), func()) {
		return func(event worker.Event) {
			if event.Approval != nil {
				// The owner bridge persists this before relaying an external denial.
				if err := s.Cancel(t.Context(), r.ID); err != nil {
					t.Fatal(err)
				}
				if err := a.Answer(t.Context(), th, worker.Answer{RequestID: event.Approval.ID, Result: json.RawMessage(`{"decision":"decline"}`)}); err != nil {
					t.Error(err)
				}
			}
		}, func() {}
	}
	s.execute(s.ctx, r)
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	card, err := s.cfg.Work.Get(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Recoveries != 0 || records[0].State != "finished" || card.Active || card.Receipt == nil || card.TurnStatus != "interrupted" {
		t.Fatalf("denied worker lost terminal evidence: dispatch=%+v card=%+v", records[0], card)
	}
}

// A cancellation may be committed after Start's initial control read and with
// no owner-session bridge registered. The dispatcher still owns stopping it.
type publicationCancelAdapter struct {
	executionAdapter
	thread      worker.Thread
	interrupted bool
}

func (a *publicationCancelAdapter) Start(_ context.Context, _ lease.Lease, spec worker.StartSpec) (worker.Thread, error) {
	a.thread = worker.Thread{ID: "thread", TurnID: "turn", TaskID: spec.TaskID, AttemptID: spec.AttemptID, BindingEpoch: 1, ProcessGeneration: 1}
	return a.thread, nil
}
func (a *publicationCancelAdapter) Interrupt(_ context.Context, th worker.Thread, _ worker.Reason) error {
	a.interrupted = true
	a.events <- worker.Event{Thread: th, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Interrupted, Code: "cancelled"}}
	return nil
}
func (a *publicationCancelAdapter) Snapshot(_ context.Context, th worker.Thread) (worker.ThreadSnapshot, error) {
	return worker.ThreadSnapshot{Thread: th, State: worker.Idle, EffectsKnown: true, Finish: &worker.Finish{Status: worker.Interrupted, Code: "cancelled"}}, nil
}
func TestPublicationCancellationAfterWorkerStart(t *testing.T) {
	s, r := executionFixture(t, false, func(context.Context) error { return nil })
	a := &publicationCancelAdapter{executionAdapter: executionAdapter{quotaStopAdapter: quotaStopAdapter{events: make(chan worker.Event, 1)}}}
	s.cfg.Workers[0].Open = func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		return a, func(context.Context) error { return nil }, nil
	}
	s.cfg.Bind = func(_ Record, _ worker.Adapter, _ worker.Thread, _, _ string) (func(worker.Event), func()) {
		if err := s.Cancel(t.Context(), r.ID); err != nil {
			t.Fatal(err)
		}
		return func(worker.Event) {}, func() {}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s.execute(ctx, r)
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	if !a.interrupted || ctx.Err() != nil || records[0].State != "finished" || !records[0].Canceled || records[0].Recoveries != 1 {
		t.Fatalf("durable cancellation missed after start: interrupted=%v context=%v record=%+v", a.interrupted, ctx.Err(), records[0])
	}
}
