// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type evidenceDriver struct {
	fixtureDriver
	read func(Action, CriterionEvidence) (CriterionEvidence, error)
}

func (d *evidenceDriver) RereadEvidence(_ context.Context, a Action, old CriterionEvidence) (CriterionEvidence, error) {
	return d.read(a, old)
}

func evidenceEngine(t *testing.T, d *evidenceDriver, state GoalState) (*Engine, Goal) {
	t.Helper()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }, Driver: d})
	if err != nil {
		t.Fatal(err)
	}
	g := finishedGoal()
	g.State = state
	for _, c := range goalCriteria(g) {
		g.Checks = append(g.Checks, CriterionEvidence{Criterion: c.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: c.Check.Target, Revision: criterionRevision(g, c), Kind: c.Check.Kind, Ref: "core-read:" + c.ID, Passed: true, Collected: &CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}})
	}
	if err = e.prepareDefinition(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if err = e.journal.Update(t.Context(), Transition{ID: "fixture", Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	e.evidenceCtx = ctx
	t.Cleanup(func() { cancel(); e.wg.Wait() })
	return e, g
}

func TestCheckingRereadsEveryEvidenceAndReopensChangedTarget(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unchanged", "changed", "unreadable", "signal-during-read"} {
		t.Run(mode, func(t *testing.T) {
			var e *Engine
			reads := 0
			d := &evidenceDriver{read: func(a Action, old CriterionEvidence) (CriterionEvidence, error) {
				reads++
				if reads == 1 {
					switch mode {
					case "changed":
						old.Collected = &CollectedEvidence{Output: "changed document", Record: []byte(`{"observed":false}`)}
					case "unreadable":
						return CriterionEvidence{}, errors.New("record unavailable")
					case "signal-during-read":
						if err := e.TargetChanged(t.Context(), "changed", a.Check.Target); err != nil {
							t.Fatal(err)
						}
					}
				}
				return old, nil
			}}
			var g Goal
			e, g = evidenceEngine(t, d, GoalChecking)
			if err := e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			e.wg.Wait()
			if err := e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			s, err := e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if reads != len(g.Checks) {
				t.Fatalf("read %d of %d evidence records", reads, len(g.Checks))
			}
			if (s.Goals[0].State == GoalDone) != (mode == "unchanged") {
				t.Fatalf("state=%s", s.Goals[0].State)
			}
			if mode != "unchanged" && (len(s.Goals[0].Checks) != 0 || s.Goals[0].CheckRound != g.CheckRound+1) {
				t.Fatalf("stale evidence survived: %+v", s.Goals[0])
			}
		})
	}
}

func TestCompletedEvidenceRefreshAndChangeHintAreDurable(t *testing.T) {
	t.Parallel()
	d := &evidenceDriver{read: func(_ Action, old CriterionEvidence) (CriterionEvidence, error) {
		old.Collected = &CollectedEvidence{Output: "changed", Record: []byte(`{"revision":2}`)}
		return old, nil
	}}
	e, g := evidenceEngine(t, d, GoalDone)
	if err := e.RefreshEvidence(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.wg.Wait()
	s, _ := e.Snapshot(t.Context())
	if s.Goals[0].State != GoalRunning || len(s.Goals[0].Checks) != 0 {
		t.Fatalf("completed goal retained stale evidence: %+v", s.Goals[0])
	}
	if err := e.TargetChanged(t.Context(), "event-moved", g.Criteria[0].Check.Target); err != nil {
		t.Fatal(err)
	}
	e.cfg.Now = func() time.Time { return time.Unix(200, 0) }
	if err := e.TargetChanged(t.Context(), "event-moved", g.Criteria[0].Check.Target); err != nil {
		t.Fatal(err)
	}
	if err := e.journal.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ = e.Snapshot(t.Context())
	if s.Goals[0].CheckRound != g.CheckRound+2 {
		t.Fatalf("change hint repeated or lost: %d", s.Goals[0].CheckRound)
	}
}

func TestTargetChangeFencesAnInFlightCriterion(t *testing.T) {
	t.Parallel()
	e, g := evidenceEngine(t, &evidenceDriver{}, GoalChecking)
	if err := e.TargetChanged(t.Context(), "begin", g.Criteria[0].Check.Target); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	a, err := e.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.TargetChanged(t.Context(), "moved-again", a.Check.Target); err != nil {
		t.Fatal(err)
	}
	old := CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: "old-read", Passed: true, Collected: &CollectedEvidence{Output: "old output"}}
	if err = e.Report(t.Context(), a.ID, a.Epoch, Observation{Status: OutcomeSucceeded, Criterion: &old}); err != nil {
		t.Fatal(err)
	}
	s, _ = e.Snapshot(t.Context())
	if len(s.Goals[0].Checks) != 0 || !s.Actions[0].Superseded {
		t.Fatal("old read satisfied a new checking round")
	}
}

func TestPluginPassedFlagCannotOverrideExpectedRecord(t *testing.T) {
	t.Parallel()
	g := finishedGoal()
	g.Criteria = []Criterion{{ID: "event", Statement: "Event at 09:00", Check: CriterionCheck{Kind: ReadEvidence, Method: "calendar.read", Target: "calendar:event", Expected: []byte(`{"time":"09:00"}`)}}}
	c := g.Criteria[0]
	a := Action{Kind: CheckCriterion, CriterionID: c.ID, Check: &c.Check, GoalRevision: g.Revision, CheckRound: g.CheckRound, Revision: criterionRevision(g, c)}
	o := Observation{Status: OutcomeSucceeded, Criterion: &CriterionEvidence{Criterion: c.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: c.Check.Target, Revision: a.Revision, Kind: ReadEvidence, Ref: "calendar-record", Passed: true, Collected: &CollectedEvidence{Record: []byte(`{"time":"10:00"}`)}}}
	if err := applySuccess(&g, nil, a, o, 100); err != nil {
		t.Fatal(err)
	}
	if len(g.Checks) != 1 || g.Checks[0].Passed {
		t.Fatal("connector success overrode the requested fields")
	}
	p, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil || len(p.Close) != 0 || len(p.Actions) != 1 || p.Actions[0].Kind != PlanGoal {
		t.Fatalf("mismatch did not return to repair: %+v %v", p, err)
	}
}

// stays serial: its two-second dispatch deadline fails when the test shares the process with the other package tests.
func TestSlowEvidenceDoesNotBlockAnotherGoal(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	assigned := make(chan Action, 1)
	var once sync.Once
	d := &evidenceDriver{read: func(_ Action, old CriterionEvidence) (CriterionEvidence, error) {
		once.Do(func() { close(started) })
		<-release
		return old, nil
	}}
	d.run = func(a Action) (Observation, error) {
		if a.Kind == Assign && a.GoalID == "unrelated" {
			assigned <- a
			return Observation{Status: OutcomeRunning, Handle: "worker"}, nil
		}
		return Observation{}, ErrInvalid
	}
	e, _ := evidenceEngine(t, d, GoalChecking)
	other := oneGoal(readyTask("other"))
	other.ID = "unrelated"
	if err := e.Admit(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	defer func() {
		close(release)
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not start")
	}
	select {
	case <-assigned:
	case <-time.After(2 * time.Second):
		t.Fatal("slow evidence blocked another goal's dispatch")
	}
}

func TestEvidenceInvalidationRetiresAuthorizationWait(t *testing.T) {
	t.Parallel()
	e, g := evidenceEngine(t, &evidenceDriver{}, GoalChecking)
	if err := e.TargetChanged(t.Context(), "first", g.Criteria[0].Check.Target); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	a, err := e.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.awaitAuthorization(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = e.TargetChanged(t.Context(), "second", a.Check.Target); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = e.Signal(t.Context(), Signal{ID: "late-approval", ActionID: a.ID}); err != nil {
		t.Fatal(err)
	}
	s, _ = e.Snapshot(t.Context())
	for _, old := range s.Actions {
		if old.ID == a.ID && (old.State != "finished" || !old.Superseded) {
			t.Fatalf("obsolete authorization wait revived: %+v", old)
		}
	}
}

func TestChangedRoundDuringAuthorizationCannotRefreshOldCheck(t *testing.T) {
	t.Parallel()
	e, g := evidenceEngine(t, &evidenceDriver{}, GoalChecking)
	if err := e.TargetChanged(t.Context(), "first", g.Criteria[0].Check.Target); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	a, err := e.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// The authority call is still in flight when the target changes.
	if err = e.TargetChanged(t.Context(), "changed-in-authorize", a.Check.Target); err != nil {
		t.Fatal(err)
	}
	if err = e.awaitAuthorization(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = e.Signal(t.Context(), Signal{ID: "late-grant", ActionID: a.ID}); err != nil {
		t.Fatal(err)
	}
	s, _ = e.Snapshot(t.Context())
	for _, old := range s.Actions {
		if old.ID == a.ID && (old.State != "finished" || !old.Superseded) {
			t.Fatalf("stale authorization requeued: %+v", old)
		}
	}
}

func TestStartupDerivationDoesNotStartEvidenceCommands(t *testing.T) {
	t.Parallel()
	d := &evidenceDriver{read: func(_ Action, old CriterionEvidence) (CriterionEvidence, error) {
		t.Error("startup ran an external reader")
		return old, nil
	}}
	e, _ := evidenceEngine(t, d, GoalChecking)
	e.evidenceCtx = nil
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	if s.Goals[0].State == GoalDone {
		t.Fatal("startup closed before the runner rechecked evidence")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.evidenceReads) != 0 {
		t.Fatal("startup scheduled a reader without its runner lifetime")
	}
}
