// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
)

// TestGoalDispatchCardKeepsItsGoalLine checks that the work card a goal
// dispatch creates carries the 일 it serves. Without that link the progress
// projection would draw the worker as a second line beside its 일.
func TestGoalDispatchCardKeepsItsGoalLine(t *testing.T) {
	s, r, _, _, _ := switchDispatchFixture(t, false)
	a := &lifecycleAdapter{quotaStopAdapter: quotaStopAdapter{events: make(chan worker.Event, 2)}}
	r.Definition.Worker = a.Name()
	r.Goal, r.GoalID = true, "goal-line"
	if err := s.save(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	s.cfg.Workers = []Factory{{Name: a.Name(), Explicit: true, Chooser: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		return a, func(context.Context) error { return nil }, nil
	}}}
	s.cfg.Bind = func(_ Record, _ worker.Adapter, thread worker.Thread, _, _ string) (func(worker.Event), func()) {
		return func(e worker.Event) {
			if e.Kind == worker.ProgressEvent {
				a.events <- worker.Event{Thread: thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Interrupted, Code: "cancelled"}}
			}
		}, func() {}
	}
	s.execute(s.ctx, r)
	card, err := s.cfg.Work.Get(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if card.Task.GoalID != "goal-line" {
		t.Fatalf("dispatch card lost its 일: %+v", card.Task)
	}
}
