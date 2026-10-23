// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// sessionAdapter is an already-finished worker turn with no process.
type sessionAdapter struct{ events chan worker.Event }

func newSessionAdapter() *sessionAdapter { return &sessionAdapter{events: make(chan worker.Event)} }
func (*sessionAdapter) Name() string     { return "fixture" }
func (*sessionAdapter) Version(context.Context) (string, error) {
	return "fixture-1", nil
}
func (*sessionAdapter) Capabilities() worker.CapabilitySet { return nil }
func (*sessionAdapter) Start(context.Context, lease.Lease, worker.StartSpec) (worker.Thread, error) {
	return worker.Thread{}, errors.New("not startable")
}
func (*sessionAdapter) Input(context.Context, worker.Thread, worker.Input) error {
	return errors.New("not startable")
}
func (*sessionAdapter) Interrupt(context.Context, worker.Thread, worker.Reason) error {
	return errors.New("not startable")
}
func (a *sessionAdapter) Events() <-chan worker.Event { return a.events }
func (*sessionAdapter) Answer(context.Context, worker.Thread, worker.Answer) error {
	return errors.New("not startable")
}
func (*sessionAdapter) Quota(context.Context) ([]provider.QuotaRow, error) { return nil, nil }
func (*sessionAdapter) Snapshot(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	return worker.ThreadSnapshot{}, errors.New("not startable")
}

func keepFixture(t *testing.T, r *Record) (*Service, *execution, *int) {
	t.Helper()
	live := worker.NewLive()
	closes := 0
	adapter := newSessionAdapter()
	s := &Service{cfg: Config{Sessions: live}}
	e := &execution{record: *r, adapter: adapter, closer: &attemptCloser{close: func(context.Context) error { closes++; return nil }}}
	return s, e, &closes
}

func deliveredGoal() *Record {
	return &Record{ID: "task", Goal: true, GoalAction: "action", Code: "ready_for_review", Worker: "fixture", Version: "fixture-1", PR: &antigravity.PullRequest{Head: "head"}, Thread: &worker.Thread{ID: "thread", TurnID: "turn", TaskID: "task"}}
}

func TestDeliveredGoalStepKeepsItsSession(t *testing.T) {
	r := deliveredGoal()
	s, e, closes := keepFixture(t, r)
	s.keepSession(e, r, *r.Thread)
	if *closes != 0 || s.cfg.Sessions.Count() != 1 {
		t.Fatalf("delivered step ended its worker: closes=%d live=%d", *closes, s.cfg.Sessions.Count())
	}
	list := s.cfg.Sessions.List()
	if len(list) != 1 || list[0].ID != "action" || list[0].Thread.ID != "thread" || list[0].Worker != "fixture" {
		t.Fatalf("session record = %+v", list)
	}
	// The deferred cleanup of the attempt never joins an adopted process.
	if err := e.closer.Close(t.Context()); err != nil || *closes != 0 {
		t.Fatalf("deferred cleanup joined an adopted session: %v closes=%d", err, *closes)
	}
	// A landed step leaves the keep set and ends the process once.
	if err := s.cfg.Sessions.EndUnlisted(t.Context(), nil); err != nil || *closes != 1 || s.cfg.Sessions.Count() != 0 {
		t.Fatalf("landing did not end the session: %v closes=%d live=%d", err, *closes, s.cfg.Sessions.Count())
	}
	if _, ok := s.cfg.Sessions.Ended("action"); !ok {
		t.Fatal("ended session lost its thread record")
	}
}

func TestOtherOutcomesEndTheWorker(t *testing.T) {
	for name, mutate := range map[string]func(*Record){
		"not a goal":        func(r *Record) { r.Goal, r.GoalAction = false, "" },
		"no receipt":        func(r *Record) { r.Code = "pr_unverified" },
		"no pull request":   func(r *Record) { r.PR = nil },
		"failed goal turn":  func(r *Record) { r.Code = "worker_failed" },
		"conversation work": func(r *Record) { r.GoalAction = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := deliveredGoal()
			mutate(r)
			s, e, closes := keepFixture(t, r)
			s.keepSession(e, r, *r.Thread)
			if *closes != 1 || s.cfg.Sessions.Count() != 0 {
				t.Fatalf("worker outlived its step: closes=%d live=%d", *closes, s.cfg.Sessions.Count())
			}
		})
	}
}

func TestSessionEndIsIdempotent(t *testing.T) {
	r := deliveredGoal()
	s, e, closes := keepFixture(t, r)
	s.keepSession(e, r, *r.Thread)
	for range 3 {
		if err := e.closer.End(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if *closes != 1 {
		t.Fatalf("repeated end joined the process %d times", *closes)
	}
}

func TestFailedJoinStaysLiveForRetry(t *testing.T) {
	r := deliveredGoal()
	s, e, closes := keepFixture(t, r)
	failed := true
	e.closer = &attemptCloser{close: func(context.Context) error {
		if failed {
			failed = false
			return errors.New("process did not join")
		}
		*closes++
		return nil
	}}
	s.keepSession(e, r, *r.Thread)
	// A join that fails keeps the session; the next event retries it.
	if err := s.cfg.Sessions.EndUnlisted(t.Context(), nil); err == nil {
		t.Fatal("a failed join reported the session as ended")
	}
	if s.cfg.Sessions.Count() != 1 {
		t.Fatal("a failed join dropped the session")
	}
	if err := s.cfg.Sessions.EndUnlisted(t.Context(), nil); err != nil || *closes != 1 || s.cfg.Sessions.Count() != 0 {
		t.Fatalf("retry did not end the session: %v closes=%d live=%d", err, *closes, s.cfg.Sessions.Count())
	}
}
