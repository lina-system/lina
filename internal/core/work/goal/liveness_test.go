// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func containsText(items []string, text string) bool {
	for _, item := range items {
		if item == text {
			return true
		}
	}
	return false
}

func TestLivenessTurnNamesOpenCriteriaThenBlocks(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	first, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Admit(t.Context(), oneGoal()); err != nil {
		t.Fatal(err)
	}
	if err = first.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, err := first.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Actions) != 1 || queued.Actions[0].Kind != PlanGoal || queued.Actions[0].State != "queued" {
		t.Fatalf("orphan was not given one derivation turn: %+v", queued.Actions)
	}
	if !containsText(queued.Actions[0].OpenCriteria, "all changes run") {
		t.Fatalf("manager turn did not name the open criteria: %+v", queued.Actions[0].OpenCriteria)
	}

	blocked := make(chan Goal, 1)
	driver := &fixtureDriver{run: func(a Action) (Observation, error) {
		if a.Kind != PlanGoal {
			return Observation{}, fmt.Errorf("orphan ran %s", a.Kind)
		}
		if !containsText(a.OpenCriteria, "all changes run") {
			return Observation{}, fmt.Errorf("dispatched turn omitted the open criteria: %v", a.OpenCriteria)
		}
		return Observation{Status: OutcomeSucceeded}, nil
	}, observe: func(Action) (Observation, error) {
		return Observation{}, fmt.Errorf("queued turn was observed instead of run")
	}}
	second, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 2, Now: now, Driver: driver, Changed: func(_ context.Context, s State) error {
		for _, g := range s.Goals {
			if g.State == GoalBlocked && strings.Contains(g.BlockedReason, "all changes run") {
				select {
				case blocked <- g:
				default:
				}
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- second.Run(ctx) }()
	var got Goal
	select {
	case got = <-blocked:
	case err := <-done:
		t.Fatalf("controller stopped: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("orphan stayed idle")
	}
	if got.ManagerTurns != 1 || got.AttentionID == "" || !strings.Contains(got.BlockedReason, "could not find a way") {
		t.Fatalf("blocked row: %+v", got)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	calls := len(driver.calls)
	driver.mu.Unlock()
	if calls != 1 {
		t.Fatalf("manager calls before restart: %d", calls)
	}

	third, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 3, Now: now, Driver: driver})
	if err != nil {
		t.Fatal(err)
	}
	if err = third.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	again, err := third.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if again.Goals[0].State != GoalBlocked || again.Goals[0].AttentionID != got.AttentionID || again.Goals[0].ManagerTurns != 1 {
		t.Fatalf("restart repeated or skipped the blocked row: %+v", again.Goals[0])
	}
	for _, a := range again.Actions {
		if a.Kind == PlanGoal && a.State != "finished" {
			t.Fatalf("restart queued another manager turn: %+v", a)
		}
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if len(driver.calls) != 1 {
		t.Fatalf("restart repeated the manager turn: %d", len(driver.calls))
	}
}

func TestLivenessClaimedTurnIsObservedOnceAfterRestart(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	first, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Admit(t.Context(), oneGoal()); err != nil {
		t.Fatal(err)
	}
	if err = first.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, err := first.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Actions) != 1 || !containsText(queued.Actions[0].OpenCriteria, "all changes run") {
		t.Fatalf("check did not survive until claim: %+v", queued.Actions)
	}
	if _, err = first.Claim(t.Context(), queued.Actions[0].ID); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan Goal, 1)
	driver := &fixtureDriver{run: func(Action) (Observation, error) {
		return Observation{}, fmt.Errorf("claimed manager turn ran again")
	}, observe: func(a Action) (Observation, error) {
		if !containsText(a.OpenCriteria, "all changes run") {
			return Observation{}, fmt.Errorf("observed turn omitted the open criteria")
		}
		return Observation{Status: OutcomeSucceeded}, nil
	}}
	second, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 2, Now: now, Driver: driver, Changed: func(_ context.Context, s State) error {
		for _, g := range s.Goals {
			if g.State == GoalBlocked && strings.Contains(g.BlockedReason, "all changes run") {
				select {
				case blocked <- g:
				default:
				}
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- second.Run(ctx) }()
	select {
	case <-blocked:
	case err := <-done:
		t.Fatalf("controller stopped: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("claimed manager turn was skipped")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if len(driver.calls) != 0 || len(driver.observations) != 1 {
		t.Fatalf("restart repeated or skipped the claimed turn: runs %d observes %d", len(driver.calls), len(driver.observations))
	}
}

func TestLivenessManagerWaitHoldsTheGoal(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	waiting := make(chan Goal, 1)
	wrong := make(chan Goal, 1)
	driver := &fixtureDriver{run: func(a Action) (Observation, error) {
		if a.Kind != PlanGoal {
			return Observation{}, fmt.Errorf("wait path ran %s", a.Kind)
		}
		return Observation{Status: OutcomeSucceeded, Wait: &Wait{Reason: "reply", Wake: "mail.changed", Until: 500}}, nil
	}, observe: func(Action) (Observation, error) {
		return Observation{}, fmt.Errorf("wait was observed")
	}}
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now, Driver: driver, Changed: func(_ context.Context, s State) error {
		for _, g := range s.Goals {
			if g.State == GoalBlocked && g.ManagerTurns > 0 {
				select {
				case wrong <- g:
				default:
				}
			}
			if g.State == GoalWaiting && len(g.Waits) == 1 && g.Waits[0].Wake == "mail.changed" && g.Waits[0].Until == 500 {
				select {
				case waiting <- g:
				default:
				}
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), oneGoal()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	var got Goal
	select {
	case got = <-waiting:
	case got = <-wrong:
		t.Fatalf("a recorded wait blocked the goal: %s", got.BlockedReason)
	case err := <-done:
		t.Fatalf("controller stopped: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("recorded wait did not hold the goal")
	}
	if got.ManagerTurns != 1 {
		t.Fatalf("wait did not spend the one manager turn: %+v", got)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if len(driver.calls) != 1 {
		t.Fatalf("wait was asked again: %d", len(driver.calls))
	}
}

func TestLivenessManagerDecisionBecomesTheRow(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	const decision = "Which city should the trip use?"
	blocked := make(chan Goal, 1)
	wrong := make(chan Goal, 1)
	driver := &fixtureDriver{run: func(a Action) (Observation, error) {
		if a.Kind != PlanGoal {
			return Observation{}, fmt.Errorf("decision path ran %s", a.Kind)
		}
		return Observation{Status: OutcomeSucceeded, Decision: decision}, nil
	}, observe: func(Action) (Observation, error) {
		return Observation{}, fmt.Errorf("decision was observed")
	}}
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now, Driver: driver, Changed: func(_ context.Context, s State) error {
		for _, g := range s.Goals {
			if g.State == GoalBlocked && g.BlockedReason == decision && g.ManagerTurns == 1 {
				select {
				case blocked <- g:
				default:
				}
			} else if g.State == GoalBlocked && g.ManagerTurns > 0 {
				select {
				case wrong <- g:
				default:
				}
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), oneGoal()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case got := <-blocked:
		if got.AttentionID == "" {
			t.Fatalf("decision row: %+v", got)
		}
	case got := <-wrong:
		t.Fatalf("decision was replaced: %s", got.BlockedReason)
	case err := <-done:
		t.Fatalf("controller stopped: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("decision did not become the row")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if len(driver.calls) != 1 {
		t.Fatalf("decision was asked again: %d", len(driver.calls))
	}
}

func TestLivenessManagerTaskStartsWork(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	started := make(chan Goal, 1)
	driver := &fixtureDriver{run: func(a Action) (Observation, error) {
		switch a.Kind {
		case PlanGoal:
			return Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("next")}}, nil
		case Assign:
			return Observation{Status: OutcomeSucceeded, Head: "head-next", EffectsSettled: true}, nil
		default:
			return Observation{}, fmt.Errorf("unexpected %s", a.Kind)
		}
	}, observe: func(Action) (Observation, error) {
		return Observation{}, fmt.Errorf("planned work was observed")
	}}
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now, Driver: driver, Changed: func(_ context.Context, s State) error {
		for _, g := range s.Goals {
			if g.State == GoalBlocked {
				return fmt.Errorf("recorded task blocked the goal: %s", g.BlockedReason)
			}
			for _, task := range g.Tasks {
				if task.ID == "next" && task.Stage == TaskDelivered {
					select {
					case started <- g:
					default:
					}
				}
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), oneGoal()); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	queued, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Actions) != 1 || !containsText(queued.Actions[0].OpenCriteria, "all changes run") {
		t.Fatalf("manager turn did not name the open criteria: %+v", queued.Actions)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case got := <-started:
		if got.ManagerTurns != 1 || got.State == GoalBlocked {
			t.Fatalf("task did not start after the one turn: %+v", got)
		}
	case err := <-done:
		t.Fatalf("controller stopped: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("recorded task did not start")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLivenessReadyTaskStartsWithoutAManagerTurn(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	started := make(chan Goal, 1)
	driver := &fixtureDriver{run: func(a Action) (Observation, error) {
		if a.Kind != Assign {
			return Observation{}, fmt.Errorf("ready work ran %s", a.Kind)
		}
		return Observation{Status: OutcomeSucceeded, Head: "head-a", EffectsSettled: true}, nil
	}, observe: func(Action) (Observation, error) {
		return Observation{}, fmt.Errorf("ready work was observed")
	}}
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now, Driver: driver, Changed: func(_ context.Context, s State) error {
		for _, g := range s.Goals {
			for _, task := range g.Tasks {
				if task.ID == "a" && task.Stage == TaskDelivered {
					select {
					case started <- g:
					default:
					}
				}
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), oneGoal(readyTask("a"))); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case got := <-started:
		if got.ManagerTurns != 0 {
			t.Fatalf("ready work spent a manager turn: %+v", got)
		}
	case err := <-done:
		t.Fatalf("controller stopped: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("ready task did not start")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
