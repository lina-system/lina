// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixtureDriver struct {
	mu           sync.Mutex
	calls        []Action
	observations []Action
	authorize    func(Action) error
	run          func(Action) (Observation, error)
	observe      func(Action) (Observation, error)
}

func (d *fixtureDriver) Authorize(_ context.Context, a Action) error {
	if d.authorize != nil {
		return d.authorize(a)
	}
	return nil
}
func (d *fixtureDriver) Execute(_ context.Context, a Action) (Observation, error) {
	d.mu.Lock()
	d.calls = append(d.calls, a)
	d.mu.Unlock()
	return d.run(a)
}
func (d *fixtureDriver) Observe(_ context.Context, a Action) (Observation, error) {
	d.mu.Lock()
	d.observations = append(d.observations, a)
	d.mu.Unlock()
	return d.observe(a)
}

func TestEngineCommitsBeforeDispatchAndReconcilesInterruptedEffects(t *testing.T) {
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	first, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Admit(t.Context(), oneGoal(readyTask("a"))); err != nil {
		t.Fatal(err)
	}
	if err = first.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err = first.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := first.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Actions) != 1 {
		t.Fatalf("no derived assignment: %+v", s)
	}
	action, err := first.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.Claim(t.Context(), action.ID); err == nil {
		t.Fatal("double claimed action")
	}
	// Restart after the durable claim and before any delivery acknowledgement.
	// A new controller must observe this exact key, never execute it again.
	observed := make(chan Action, 1)
	driver := &fixtureDriver{run: func(a Action) (Observation, error) {
		t.Errorf("replayed uncertain effect: %+v", a)
		return Observation{}, fmt.Errorf("must observe")
	}, observe: func(a Action) (Observation, error) {
		observed <- a
		return Observation{Status: OutcomeRunning, Handle: "live-worker"}, nil
	}}
	second, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 2, Now: now, Driver: driver})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- second.Run(ctx) }()
	select {
	case a := <-observed:
		if a.ID != action.ID {
			t.Fatal("observed another effect")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("startup did not observe in-flight claim")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = second.Report(t.Context(), action.ID, 1, Observation{Status: OutcomeSucceeded, Head: "late"}); err == nil {
		t.Fatal("old epoch completion accepted")
	}
}

func TestEngineGoalCannotBeAdmittedWithFabricatedCompletion(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	g.State = GoalDone
	if err = e.Admit(t.Context(), g); err == nil {
		t.Fatal("admitted preclosed goal")
	}
	g.State = ""
	g.Tasks[0].Merge = Proof{Revision: "fake", Ref: "claim", Passed: true}
	if err = e.Admit(t.Context(), g); err == nil {
		t.Fatal("admitted unobserved merge evidence")
	}
}

func TestEngineCompletionReplayAfterClockAdvance(t *testing.T) {
	_, db := testJournal(t)
	at := int64(100)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(at, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), oneGoal(readyTask("a"))); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	a, err := e.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	o := Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true}
	if err = e.Report(t.Context(), a.ID, 1, o); err != nil {
		t.Fatal(err)
	}
	at = 200
	if err = e.Report(t.Context(), a.ID, 1, o); err != nil {
		t.Fatalf("redelivery after clock advance: %v", err)
	}
}

func TestEngineObservesRecoveredHandleOnceUntilDeadline(t *testing.T) {
	_, db := testJournal(t)
	now := func() time.Time { return time.Unix(100, 0) }
	e, _ := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: now})
	if err := e.Admit(t.Context(), oneGoal(readyTask("a"))); err != nil {
		t.Fatal(err)
	}
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	if _, err := e.Claim(t.Context(), s.Actions[0].ID); err != nil {
		t.Fatal(err)
	}
	d := &fixtureDriver{observe: func(a Action) (Observation, error) { return Observation{Status: OutcomeRunning, Handle: "live"}, nil }}
	recovered, _ := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 2, Now: now, Driver: d})
	if err := recovered.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered.wg.Wait()
	if err := recovered.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered.wg.Wait()
	if len(d.observations) != 1 {
		t.Fatalf("recovery polled the same live handle %d times without an event or deadline", len(d.observations))
	}
}

func TestEngineRunsCodeGoalThroughRuntimeEvidence(t *testing.T) {
	_, db := testJournal(t)
	closed := make(chan Goal, 1)
	driver := &fixtureDriver{run: func(a Action) (Observation, error) {
		out := Observation{Status: OutcomeSucceeded}
		switch a.Kind {
		case Assign:
			out.Head = "head"
			out.EffectsSettled = true
		case InspectPR:
			out.PR = PRState{Head: "head", Checks: Proof{Revision: "head", Ref: "ci", Passed: true}, Review: Proof{Revision: "head", Ref: "review", Passed: true}, Verified: Proof{Revision: "head", Ref: "manager", Passed: true}, Mergeable: true}
		case Merge:
			out.Head = "head"
			out.Proof = Proof{Revision: "merged", Ref: "merge-observation", Passed: true}
		case RunQA:
			out.Proof = Proof{Revision: "merged", Ref: "qa-result", Passed: true}
		case Deploy:
			out.Proof = Proof{Revision: "merged", Ref: "deployment-read", Passed: true}
		case Verify:
			out.Proof = Proof{Revision: "merged", Ref: "runtime-probe", Passed: true}
		case CheckCriterion:
			out.Criterion = &CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: "read:" + a.CriterionID, Passed: true}
		case ContinueWorker, Resume, RepairConflict, RepairQA, Observe, Decide, PlanGoal:
			return Observation{}, fmt.Errorf("unexpected action %s", a.Kind)
		default:
			return Observation{}, fmt.Errorf("unexpected action %s", a.Kind)
		}
		return out, nil
	}}
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }, Driver: driver, Changed: func(_ context.Context, s State) error {
		for _, g := range s.Goals {
			if g.State == GoalDone {
				select {
				case closed <- g:
				default:
				}
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	g.ServiceTarget = "service"
	g.NoServiceReason = ""
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case got := <-closed:
		if len(got.Evidence) != 4 {
			t.Fatalf("missing lifecycle proof: %+v", got)
		}
	case err := <-done:
		t.Fatalf("controller stopped: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("goal did not close")
	}
	summary, err := e.Summary(t.Context(), "g")
	if err != nil {
		t.Fatal(err)
	}
	for _, proof := range []string{"merged", "merge-observation", "qa-result", "deployment-read", "runtime-probe"} {
		if !strings.Contains(summary, proof) {
			t.Fatalf("summary omitted %q: %s", proof, summary)
		}
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	want := []ActionKind{Assign, InspectPR, Merge, RunQA, Deploy, Verify}
	for range goalCriteria(g) {
		want = append(want, CheckCriterion)
	}
	if len(driver.calls) != len(want) {
		t.Fatalf("calls: %+v", driver.calls)
	}
	for i, k := range want {
		if driver.calls[i].Kind != k {
			t.Fatalf("stage %d=%s want %s", i, driver.calls[i].Kind, k)
		}
	}
}

func TestEngineOrphanGetsOneManagerTurnThenBlocks(t *testing.T) {
	_, db := testJournal(t)
	blocked := make(chan Goal, 1)
	driver := &fixtureDriver{run: func(a Action) (Observation, error) {
		if a.Kind != PlanGoal {
			return Observation{}, fmt.Errorf("orphan ran %s", a.Kind)
		}
		return Observation{Status: OutcomeSucceeded}, nil
	}}
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }, Driver: driver, Changed: func(_ context.Context, s State) error {
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
	g := oneGoal()
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case got := <-blocked:
		if got.ManagerTurns != 1 || got.AttentionID == "" || got.BlockedReason == "" {
			t.Fatalf("silent orphan: %+v", got)
		}
	case err := <-done:
		t.Fatalf("controller stopped: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("orphan stayed idle")
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
		t.Fatalf("manager was called %d times", len(driver.calls))
	}
}
