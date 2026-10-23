// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"testing"
	"time"
)

func reviseDuringAction(t *testing.T, e *Engine, records Definitions) {
	t.Helper()
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g := s.Goals[0]
	r, rev, err := records.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = records.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: append(r.Data, []byte("\nClarified acceptance scope.\n")...)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	if err = e.RefreshDefinitions(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestStaleCriterionResultDoesNotStopEngine(t *testing.T) {
	_, db := testJournal(t)
	started := make(chan Action, 1)
	release := make(chan struct{})
	blocked := make(chan Goal, 1)
	observed := make(chan Action, 1)
	driver := &fixtureDriver{run: func(a Action) (Observation, error) {
		if a.Kind == CheckCriterion {
			started <- a
			<-release
			return Observation{Status: OutcomeSucceeded, Criterion: &CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: "old-check-output", Passed: true}}, nil
		}
		if a.Kind != PlanGoal {
			t.Errorf("unexpected action: %+v", a)
		}
		return Observation{Status: OutcomeSucceeded}, nil
	}}
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Driver: driver, Now: func() time.Time { return time.Unix(100, 0) }, Changed: func(_ context.Context, s State) error {
		for _, a := range s.Actions {
			if a.Kind == CheckCriterion && a.State == "finished" && len(a.Receipt) > 0 {
				select {
				case observed <- a:
				default:
				}
			}
		}
		for _, g := range s.Goals {
			if g.State == GoalBlocked {
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
	g := finishedGoal()
	if err = e.prepareDefinition(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if err = e.journal.Update(t.Context(), Transition{ID: "fixture", Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		cancel()
		if err := <-done; err != nil {
			t.Errorf("engine shutdown: %v", err)
		}
	})
	var old Action
	select {
	case old = <-started:
	case err = <-done:
		done <- err
		t.Fatalf("engine stopped before check: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("check did not start")
	}
	reviseDuringAction(t, e, db.Definitions)
	close(release)
	released = true
	select {
	case current := <-blocked:
		if current.Revision <= old.GoalRevision || len(current.Checks) != 0 {
			t.Fatalf("stale proof accepted or current plan skipped: %+v", current)
		}
	case err = <-done:
		done <- err
		t.Fatalf("stale check stopped engine: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("current goal was not replanned")
	}
	// Replanning can finish before the released old check reports. Wait for
	// its committed observation independently of the current goal's state.
	select {
	case observation := <-observed:
		if observation.ID != old.ID {
			t.Fatalf("unexpected check observation: %+v", observation)
		}
	case err = <-done:
		done <- err
		t.Fatalf("engine stopped before retaining old observation: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("old check observation was not retained")
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plans, retired := 0, false
	for _, a := range s.Actions {
		if a.ID == old.ID && (a.State != "finished" || len(a.Receipt) == 0) {
			t.Fatalf("old check was not retired with its observation: %+v", a)
		}
		retired = retired || a.ID == old.ID
		if a.Kind == PlanGoal && a.State == "finished" && a.RecordRevision == s.Goals[0].RecordRevision {
			plans++
		}
	}
	if !retired || plans != 1 {
		t.Fatalf("missing retired check or current assessment: %+v", s.Actions)
	}
}

func TestStaleManagerPlanCannotClearCorrection(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), oneGoal()); err != nil {
		t.Fatal(err)
	}
	old := claimOnlyAction(t, e, PlanGoal)
	reviseDuringAction(t, e, db.Definitions)
	observation := Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("old-plan")}}
	for range 2 {
		if err = e.Report(t.Context(), old.ID, 1, observation); err != nil {
			t.Fatal(err)
		}
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Goals[0].Tasks) != 0 || !s.Goals[0].DefinitionPending {
		t.Fatalf("stale plan applied: %+v", s.Goals[0])
	}
	current := claimOnlyAction(t, e, PlanGoal)
	if current.GoalRevision != s.Goals[0].Revision || current.GoalRevision == old.GoalRevision {
		t.Fatalf("not replanned against correction: %+v", current)
	}
}

func TestWorkerRecoveryRequiresCurrentActionScope(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	action := claimOnlyAction(t, e, Assign)
	if allowed, err := e.CanContinue(t.Context(), action.ID); err != nil || !allowed {
		t.Fatalf("current assignment refused: %v %v", allowed, err)
	}
	revisionEdit(t, e)
	if allowed, err := e.CanContinue(t.Context(), action.ID); err != nil || allowed {
		t.Fatalf("retired assignment recovered: %v %v", allowed, err)
	}
}
