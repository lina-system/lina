// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

type readinessAdapter struct {
	quotaStopAdapter
	name  string
	start func(worker.StartSpec) error
}

func TestReadinessRecheckedForNewTask(t *testing.T) {
	s, r, _, _, _ := switchDispatchFixture(t, false)
	r.Definition.Worker = "cursor"
	if err := s.save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	signedIn, starts := false, 0
	s.cfg.Workers = []Factory{{Name: "cursor", Explicit: true, Chooser: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		a := &readinessAdapter{name: "cursor", quotaStopAdapter: quotaStopAdapter{events: make(chan worker.Event, 1)}, start: func(worker.StartSpec) error {
			starts++
			if !signedIn {
				return worker.ErrNeedsSignIn
			}
			return nil
		}}
		return a, func(context.Context) error { return nil }, nil
	}}}
	s.execute(s.ctx, r)
	records, err := s.records(t.Context())
	if err != nil || len(records) != 1 || records[0].Code != "needs_sign_in" {
		t.Fatal(records, err)
	}
	signedIn = true
	// A fresh task has its own attempt budget, including after a service restart.
	restarted := &Service{cfg: s.cfg, ctx: s.ctx}
	l := lease.New(func() {})
	defer l.Release()
	a, close, err := restarted.selectWorker(t.Context(), l, Record{Definition: r.Definition})
	if err != nil || a == nil {
		t.Fatalf("external sign-in left the next task blocked: adapter=%v err=%v", a, err)
	}
	defer close(t.Context())
	if _, err := a.Start(t.Context(), l, worker.StartSpec{}); err != nil || starts != 2 {
		t.Fatalf("signed-in session was not rechecked: starts=%d err=%v", starts, err)
	}
	a, _, err = restarted.selectWorker(t.Context(), l, records[0])
	if err != nil || a != nil {
		t.Fatal("sign-in granted the old task another attempt", a, err)
	}
}

func TestReadinessLegacyQuotaRecovery(t *testing.T) {
	for _, mode := range []string{"refusal", "refusal-with-fallback", "measured-quota", "account-quota", "reset", "multiple-windows", "unattributed-stop", "executed-attempt"} {
		t.Run(mode, func(t *testing.T) {
			db := store(t)
			r := Record{ID: "old-task", State: "finished", Worker: "cursor", Tried: []string{"cursor"}, ReadinessFailures: []ReadinessFailure{{Worker: "cursor", Code: "needs_sign_in"}}}
			if mode == "refusal-with-fallback" {
				r.Worker = "gemini"
				r.Tried = append(r.Tried, "gemini")
				r.Thread = &worker.Thread{ID: "fallback-session"}
			}
			if mode == "unattributed-stop" || mode == "executed-attempt" {
				r.ReadinessFailures = nil
			}
			if err := db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES(?,?,?,?,?)", r.ID, "turn", "repo", r.State, mustJSON(r))
				if err == nil && mode == "executed-attempt" {
					r.ID = "refused-task"
					r.ReadinessFailures = []ReadinessFailure{{Worker: "cursor", Code: "needs_sign_in"}}
					_, err = tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch VALUES(?,?,?,?,?)", r.ID, "turn", "repo", r.State, mustJSON(r))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			s := &Service{cfg: Config{DB: db}}
			row := provider.QuotaRow{Window: provider.UnknownWindow, State: provider.QuotaBlocked, Source: provider.ErrorCode, Observed: time.Now().UTC()}
			if mode == "measured-quota" {
				row.Window = provider.FiveHour
			}
			if mode == "account-quota" {
				row.Account = "primary"
			}
			if mode == "reset" {
				reset := row.Observed.Add(time.Hour)
				row.ResetsAt = &reset
			}
			prior := []provider.QuotaRow{row}
			if mode == "multiple-windows" {
				prior = append(prior, provider.QuotaRow{Window: provider.Weekly, State: provider.QuotaBlocked, Source: provider.ProviderEvent, Observed: row.Observed})
			}
			if err := s.ObserveQuota(t.Context(), "cursor", prior); err != nil {
				t.Fatal(err)
			}
			restarted, err := Open(t.Context(), s.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := restarted.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			rows, err := restarted.quota(t.Context(), "cursor")
			if err != nil {
				t.Fatal(err)
			}
			recovered := mode == "refusal" || mode == "refusal-with-fallback"
			if recovered && len(rows) != 0 {
				t.Fatal("legacy sign-in block survived restart", rows)
			}
			if !recovered && !reflect.DeepEqual(rows, prior) {
				t.Fatal("readiness recovery erased a real or unattributed quota stop", rows)
			}
		})
	}
}

func (a *readinessAdapter) Name() string { return a.name }
func (a *readinessAdapter) Start(_ context.Context, _ lease.Lease, spec worker.StartSpec) (worker.Thread, error) {
	if err := a.start(spec); err != nil {
		return worker.Thread{}, err
	}
	thread := worker.Thread{ID: "ready", TurnID: "turn", TaskID: spec.TaskID, AttemptID: spec.AttemptID, BindingEpoch: 1, ProcessGeneration: 1}
	a.events <- worker.Event{Thread: thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Interrupted, Code: "worker_cancelled"}}
	return thread, nil
}

func TestReadinessFallback(t *testing.T) {
	for _, tc := range []struct {
		name               string
		startErr, closeErr error
		wantStarts         int
		wantState          string
	}{
		{"sign-in", fmt.Errorf("session: %w", worker.ErrNeedsSignIn), nil, 2, "finished"},
		{"connection", errors.New("connection lost"), nil, 1, "unknown"},
		{"protocol", errors.New("unsupported protocol"), nil, 1, "unknown"},
		{"cleanup", worker.ErrNeedsSignIn, errors.New("child still running"), 1, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, r, _, _, _ := switchDispatchFixture(t, false)
			r.Definition.Worker = ""
			if err := s.save(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			starts := 0
			firstClosed := false
			factory := func(name string) Factory {
				return Factory{Name: name, Chooser: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
					started := false
					a := &readinessAdapter{name: name, quotaStopAdapter: quotaStopAdapter{events: make(chan worker.Event, 1)}}
					a.start = func(spec worker.StartSpec) error {
						started = true
						starts++
						if name == "first" {
							return tc.startErr
						}
						if !firstClosed {
							t.Error("replacement started before failed worker cleanup")
						}
						if spec.TaskPacket != r.Packet && r.Packet != "" {
							t.Error("task changed during readiness fallback")
						}
						return nil
					}
					return a, func(context.Context) error {
						if name == "first" && started {
							firstClosed = true
							return tc.closeErr
						}
						return nil
					}, nil
				}}
			}
			s.cfg.Workers = []Factory{factory("first"), factory("second")}
			s.execute(s.ctx, r)
			records, err := s.records(t.Context())
			if err != nil || len(records) != 1 {
				t.Fatal(records, err)
			}
			got := records[0]
			if starts != tc.wantStarts || got.State != tc.wantState {
				t.Fatalf("starts=%d state=%s code=%s tried=%v", starts, got.State, got.Code, got.Tried)
			}
			card, err := s.cfg.Work.Get(t.Context(), r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantStarts == 2 {
				if len(got.ReadinessFailures) != 1 || got.ReadinessFailures[0] != (ReadinessFailure{Worker: "first", Version: "fixture", Code: "needs_sign_in"}) {
					t.Fatal("readiness refusal not retained", got.ReadinessFailures)
				}
				if strings.Join(got.Tried, ",") != "first,second" || card.Run == nil || card.Run.Worker != "second" || card.Run.Generation != 1 || card.Receipt == nil || card.Receipt.Outcome != "interrupted" {
					t.Fatal("fallback did not reach a real turn", got, card)
				}
				if got.Attempt != card.Run.Generation {
					t.Fatalf("readiness refusal split recovery identity: dispatch attempt=%d run generation=%d", got.Attempt, card.Run.Generation)
				}
				ledger, err := s.cfg.Work.Ledger(t.Context(), r.ID)
				if err != nil {
					t.Fatal(err)
				}
				seen := false
				for _, event := range ledger {
					seen = seen || event.Kind == "worker_progress" && strings.Contains(event.Value, "first") && strings.Contains(event.Value, "sign-in")
				}
				if !seen {
					t.Fatal("readiness not reported on work card")
				}
			} else if card.Run != nil || card.Receipt != nil {
				t.Fatal("failed readiness invented a session or receipt", card)
			}
		})
	}
}

func TestReadinessRefusalsKeepAttemptLimitAndExplicitChoice(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			s, r, _, _, _ := switchDispatchFixture(t, false)
			r.Definition.Worker = ""
			if explicit {
				r.Definition.Worker = "first"
			}
			if err := s.save(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			var starts []string
			s.cfg.Workers = nil
			for _, name := range []string{"first", "second", "third"} {
				s.cfg.Workers = append(s.cfg.Workers, Factory{Name: name, Chooser: true, Explicit: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
					return &readinessAdapter{name: name, start: func(worker.StartSpec) error { starts = append(starts, name); return worker.ErrNeedsSignIn }}, func(context.Context) error { return nil }, nil
				}})
			}
			s.execute(s.ctx, r)
			records, err := s.records(t.Context())
			if err != nil || len(records) != 1 {
				t.Fatal(records, err)
			}
			got := records[0]
			want, state := "first,second", "finished"
			if explicit {
				want, state = "first", "queued"
			}
			if strings.Join(starts, ",") != want || got.State != state || got.Code != "needs_sign_in" || len(got.ReadinessFailures) != len(starts) {
				t.Fatal("readiness widened retry policy", starts, got)
			}
			card, err := s.cfg.Work.Get(t.Context(), r.ID)
			if err != nil || card.Run != nil || card.Receipt != nil || card.Active {
				t.Fatal("refused sessions invented execution", card, err)
			}
		})
	}
}
