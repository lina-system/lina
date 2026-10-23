// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"fmt"
	"testing"
)

func passingInspection() Observation {
	proof := Proof{Revision: "head", Ref: "read-before-change", Passed: true}
	return Observation{Status: OutcomeSucceeded, PR: PRState{Head: "head", Checks: proof, Review: proof, Verified: proof, Mergeable: true}}
}

func TestInputVersionFencesInspectionAcrossSignalAndReplay(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint(restart), func(t *testing.T) {
			task := readyTask("a")
			task.Stage, task.Head = TaskDelivered, "head"
			e := reviewEngine(t, oneGoal(task))
			reviewReconcile(t, e)
			a := claimReviewAction(t, e, "g", InspectPR)
			// This is the result read by the dispatched inspection, before the
			// committed signal invalidates that read. No wall-clock race is used.
			old := passingInspection()
			signal := Signal{ID: "new-blocking-review", GoalID: "g", TaskID: "a"}
			if err := e.Signal(t.Context(), signal); err != nil {
				t.Fatal(err)
			}
			if restart {
				cfg := e.cfg
				cfg.Epoch++
				var err error
				e, err = NewEngine(cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := encode(reviewGoal(t, e, "g"))
			for range 2 {
				if err := e.Report(t.Context(), a.ID, e.cfg.Epoch, old); err != nil {
					t.Fatal(err)
				}
				if after := encode(reviewGoal(t, e, "g")); !bytes.Equal(before, after) {
					t.Fatalf("stale inspection changed the goal after invalidation: %s", after)
				}
			}
			assertRetiredReviewAction(t, e, a)
			reviewReconcile(t, e)
			next := claimReviewAction(t, e, "g", InspectPR)
			if next.ID == a.ID {
				t.Fatal("reused the obsolete dispatch identity")
			}
			// Replaying the same committed hint must not invalidate a genuinely
			// newer observation. It already contributed its generation once.
			if err := e.Signal(t.Context(), signal); err != nil {
				t.Fatal(err)
			}
			fresh := passingInspection()
			fresh.PR.OpenP0P1 = 1
			if err := e.Report(t.Context(), next.ID, e.cfg.Epoch, fresh); err != nil {
				t.Fatal(err)
			}
			reviewReconcile(t, e)
			s, err := e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range s.Actions {
				if action.Kind == Merge && action.State != "finished" {
					t.Fatal("merged despite the fresh blocking review")
				}
			}
			if got := reviewGoal(t, e, "g").Tasks[0].PR; got.OpenP0P1 != 1 || got.ObservedAt == 0 {
				t.Fatalf("fresh observation was not accepted: %+v", got)
			}
		})
	}
}

func TestInputVersionJudgmentsUseTheSameSignalFence(t *testing.T) {
	for _, kind := range []ActionKind{PlanGoal, CheckCriterion} {
		t.Run(string(kind), func(t *testing.T) {
			g := finishedGoal()
			if kind == PlanGoal {
				g.Tasks[0].Stage = TaskQAFailed
				g.Tasks[0].QA.Passed = false
			}
			e := reviewEngine(t, g)
			reviewReconcile(t, e)
			a := claimReviewAction(t, e, "g", kind)
			old := Observation{Status: OutcomeSucceeded}
			if kind == PlanGoal {
				repair := readyTask("obsolete-repair")
				repair.Repairs = "a"
				old.Tasks = []Task{repair}
			} else {
				old.Criterion = &CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: "old-output", Passed: true}
			}
			if err := e.Signal(t.Context(), Signal{ID: "new-task-input", GoalID: "g", TaskID: "a"}); err != nil {
				t.Fatal(err)
			}
			want := reviewGoal(t, e, "g")
			if kind == PlanGoal {
				if !want.Tasks[0].RepairAsked {
					t.Fatal("fixture has no outstanding repair assessment")
				}
				// Retirement clears only its assessment marker; the obsolete
				// judgment must not change any current plan or evidence.
				want.Tasks[0].RepairAsked = false
			}
			if err := e.Report(t.Context(), a.ID, e.cfg.Epoch, old); err != nil {
				t.Fatal(err)
			}
			if after := encode(reviewGoal(t, e, "g")); !bytes.Equal(encode(want), after) {
				t.Fatalf("stale judgment changed current state: %s", after)
			}
			assertRetiredReviewAction(t, e, a)
			reviewReconcile(t, e)
			if next := reviewAction(t, e, "g", kind); next.ID == a.ID {
				t.Fatal("judgment was not rederived")
			}
		})
	}
}

func TestInputVersionStaleWorkerIsReadAgainWithoutRedispatch(t *testing.T) {
	e, a := claimedAssignment(t)
	if err := e.Signal(t.Context(), Signal{ID: "worker-changed", GoalID: "g", TaskID: "a"}); err != nil {
		t.Fatal(err)
	}
	before := encode(reviewGoal(t, e, "g"))
	old := Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true}
	if err := e.Report(t.Context(), a.ID, e.cfg.Epoch, old); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, encode(reviewGoal(t, e, "g"))) {
		t.Fatal("obsolete worker completion changed current state")
	}
	observations := 0
	e.cfg.Driver = &fixtureDriver{run: func(Action) (Observation, error) {
		t.Error("redispatched a worker instead of reading its existing receipt")
		return Observation{Status: OutcomeUnknown}, nil
	}, observe: func(got Action) (Observation, error) {
		observations++
		if got.ID != a.ID {
			t.Errorf("observed another external effect: %s", got.ID)
		}
		return old, nil
	}}
	if err := e.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.wg.Wait()
	if observations != 1 {
		t.Fatalf("fresh receipt reads = %d", observations)
	}
	task := reviewGoal(t, e, "g").Tasks[0]
	if task.Stage != TaskDelivered || task.Head != "head" || task.Recoveries != 0 || task.Attempt != a.Attempt {
		t.Fatalf("fresh observation lost reservation/receipt: %+v", task)
	}
}

func TestInputVersionRetainsLateWorkerHandleAcrossRestart(t *testing.T) {
	e, a := claimedAssignment(t)
	if err := e.Signal(t.Context(), Signal{ID: "worker-changed", GoalID: "g", TaskID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Report(t.Context(), a.ID, e.cfg.Epoch, Observation{Status: OutcomeRunning, Handle: "late-handle"}); err != nil {
		t.Fatal(err)
	}
	cfg := e.cfg
	cfg.Epoch++
	var err error
	e, err = NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.cfg.Driver = &fixtureDriver{observe: func(got Action) (Observation, error) {
		if got.ID != a.ID || got.Handle != "late-handle" {
			t.Errorf("lost original effect receipt: id=%s handle=%s", got.ID, got.Handle)
		}
		return Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true}, nil
	}}
	if err := e.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.wg.Wait()
	if reviewGoal(t, e, "g").Tasks[0].Stage != TaskDelivered {
		t.Fatal("fresh read did not finish the existing worker")
	}
}

func TestInputVersionOriginalEffectSignalFencesItsReplacementRead(t *testing.T) {
	e, original := claimedAssignment(t)
	if err := e.Signal(t.Context(), Signal{ID: "first-change", ActionID: original.ID}); err != nil {
		t.Fatal(err)
	}
	old := Observation{Status: OutcomeRunning, Handle: "worker"}
	if err := e.Report(t.Context(), original.ID, e.cfg.Epoch, old); err != nil {
		t.Fatal(err)
	}
	reviewReconcile(t, e)
	read := claimReviewAction(t, e, "g", Assign)
	// Integrations continue reporting the stable external effect id, even
	// after its first result was retired and a new receipt read was claimed.
	if err := e.Signal(t.Context(), Signal{ID: "second-change", ActionID: original.ID}); err != nil {
		t.Fatal(err)
	}
	before := encode(reviewGoal(t, e, "g"))
	if err := e.Report(t.Context(), read.ID, e.cfg.Epoch, Observation{Status: OutcomeSucceeded, Head: "old-head", EffectsSettled: true}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, encode(reviewGoal(t, e, "g"))) {
		t.Fatal("original effect's signal did not fence its active observation")
	}
}

func TestInputVersionRetiresSettledEffectsForObsoleteTargets(t *testing.T) {
	g := finishedGoal()
	g.ServiceTarget, g.NoServiceReason = "private-service", ""
	e := reviewEngine(t, g)
	reviewReconcile(t, e)
	a := claimReviewAction(t, e, "g", Deploy)
	// Another merged result changes the deployment input, without changing
	// the person's canonical definition. This is also an input-version fence.
	if err := e.journal.Update(t.Context(), Transition{ID: "new-merge", Kind: "merge.observed", At: 100}, func(v *View) error {
		g, err := loadGoal(v, "g")
		if err != nil {
			return err
		}
		if err = e.hydrate(&g); err != nil {
			return err
		}
		g.TargetRevision = "new-merge"
		g.MergedRevisions[g.ServiceRepository] = "new-merge"
		return put(v, "goal", g.ID, g)
	}); err != nil {
		t.Fatal(err)
	}
	old := Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: a.Revision, Ref: "old-deploy", Passed: true}}
	before := encode(reviewGoal(t, e, "g"))
	if err := e.Report(t.Context(), a.ID, e.cfg.Epoch, old); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, encode(reviewGoal(t, e, "g"))) {
		t.Fatal("obsolete deployment changed the goal")
	}
	reviewReconcile(t, e)
	next := reviewAction(t, e, "g", Deploy)
	if next.Revision != "new-merge" {
		t.Fatalf("settled obsolete effect prevents current deployment: %+v", next)
	}
}

func TestInputVersionWaitsForUnknownObsoleteEffectsWithoutPolling(t *testing.T) {
	g := finishedGoal()
	g.ServiceTarget, g.NoServiceReason = "private-service", ""
	e := reviewEngine(t, g)
	reviewReconcile(t, e)
	a := claimReviewAction(t, e, "g", Deploy)
	if err := e.journal.Update(t.Context(), Transition{ID: "new-target", Kind: "merge.observed", At: 100}, func(v *View) error {
		g, err := loadGoal(v, "g")
		if err != nil {
			return err
		}
		if err = e.hydrate(&g); err != nil {
			return err
		}
		g.TargetRevision = "new-merge"
		g.MergedRevisions[g.ServiceRepository] = "new-merge"
		return put(v, "goal", g.ID, g)
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Report(t.Context(), a.ID, e.cfg.Epoch, Observation{Status: OutcomeUnknown, Handle: "old-deploy"}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	e.cfg.Driver = &fixtureDriver{observe: func(got Action) (Observation, error) {
		reads++
		if got.ID != a.ID {
			t.Error("lost unresolved deployment identity")
		}
		return Observation{Status: OutcomeRunning, Handle: "old-deploy"}, nil
	}, run: func(Action) (Observation, error) {
		t.Error("started another deployment before observing the old one")
		return Observation{Status: OutcomeUnknown}, nil
	}}
	if err := e.Signal(t.Context(), Signal{ID: "deploy-progress", ActionID: a.ID}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := e.step(t.Context()); err != nil {
			t.Fatal(err)
		}
		e.wg.Wait()
	}
	if reads != 1 {
		t.Fatalf("unchanged unresolved effect was polled %d times", reads)
	}
	if err := e.Report(t.Context(), a.ID, e.cfg.Epoch, Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: a.Revision, Ref: "old-deploy-complete", Passed: true}}); err != nil {
		t.Fatal(err)
	}
	reviewReconcile(t, e)
	if next := reviewAction(t, e, "g", Deploy); next.Revision != "new-merge" {
		t.Fatal("settled old deployment still blocks the current target")
	}
}
