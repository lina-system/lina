// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestObsoleteCriterionReportDoesNotStopEngine(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "head-a", EffectsSettled: true})
	revisionReport(t, e, "a", InspectPR, revisionPR("head-a"))
	revisionReport(t, e, "a", Merge, Observation{Status: OutcomeSucceeded, Head: "head-a", Proof: Proof{Revision: "merge-a", Ref: "merge", Passed: true}})
	revisionReport(t, e, "a", RunQA, Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: "merge-a", Ref: "qa", Passed: true}})
	revisionReport(t, e, "", Verify, Observation{Status: OutcomeSucceeded, Proof: Proof{Revision: "merge-a", Ref: "probe", Passed: true}})
	stale, err := e.Claim(t.Context(), revisionAction(t, e, "", CheckCriterion).ID)
	if err != nil {
		t.Fatal(err)
	}
	revisionEdit(t, e)
	// A new epoch observes the old read receipt. A stale result must be retired
	// without recording evidence or terminating the running engine.
	cfg := e.cfg
	cfg.Epoch++
	assessed := make(chan struct{}, 1)
	cfg.Driver = &fixtureDriver{
		observe: func(a Action) (Observation, error) {
			if a.ID != stale.ID {
				return Observation{}, ErrInvalid
			}
			return Observation{Status: OutcomeSucceeded, Criterion: &CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: "old-read", Passed: true}}, nil
		},
		run: func(a Action) (Observation, error) {
			if a.Kind != PlanGoal {
				return Observation{}, ErrInvalid
			}
			return Observation{Status: OutcomeSucceeded}, nil
		},
	}
	cfg.Changed = func(_ context.Context, s State) error {
		if s.Goals[0].State == GoalBlocked {
			select {
			case assessed <- struct{}{}:
			default:
			}
		}
		return nil
	}
	e, err = NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-assessed:
		cancel()
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	case err = <-done:
		cancel()
		t.Fatalf("obsolete criterion stopped engine: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		<-done
		t.Fatal("obsolete criterion blocked the corrected definition")
	}
	s := revisionSnapshot(t, e)
	if len(s.Goals[0].Checks) != 0 {
		t.Fatal("obsolete evidence applied to revised goal")
	}
	for _, a := range s.Actions {
		if a.ID == stale.ID && a.State != "finished" {
			t.Fatal("obsolete criterion still active")
		}
	}
}

func TestOldManagerCannotAssessCorrectedDefinition(t *testing.T) {
	for _, withTask := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "old-plan"}[withTask], func(t *testing.T) {
			e := revisionEngine(t)
			a, err := e.Claim(t.Context(), revisionAction(t, e, "", PlanGoal).ID)
			if err != nil {
				t.Fatal(err)
			}
			revisionEdit(t, e)
			o := Observation{Status: OutcomeSucceeded}
			if withTask {
				o.Tasks = []Task{readyTask("stale")}
			}
			if err = e.Report(t.Context(), a.ID, a.Epoch, o); err != nil {
				t.Fatal(err)
			}
			g := revisionSnapshot(t, e).Goals[0]
			if !g.DefinitionPending || g.AssessedFailure != "" || g.State == GoalBlocked || len(g.Tasks) != 0 {
				t.Fatalf("old manager assessed the corrected definition: %+v", g)
			}
			revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("current")}})
			g = revisionSnapshot(t, e).Goals[0]
			if g.DefinitionPending || len(g.Tasks) != 1 || g.Tasks[0].ID != "current" {
				t.Fatal("current definition was not replanned")
			}
		})
	}
}

func TestClaimedSiblingRepairsRederiveAfterExecutionRevision(t *testing.T) {
	e := revisionEngine(t, readyTask("a"), readyTask("b"))
	var actions []Action
	for _, id := range []string{"a", "b"} {
		revisionReport(t, e, id, Assign, Observation{Status: OutcomeSucceeded, Head: "head-" + id, EffectsSettled: true})
		revisionReport(t, e, id, InspectPR, revisionPR("head-"+id))
		revisionReport(t, e, id, Merge, Observation{Status: OutcomeSucceeded, Head: "head-" + id, Proof: Proof{Revision: "merge-" + id, Ref: "merge", Passed: true}})
		revisionReport(t, e, id, RunQA, Observation{Status: OutcomeFailed, Proof: Proof{Revision: "merge-" + id, Ref: "failed-qa"}})
		a, err := e.Claim(t.Context(), revisionAction(t, e, id, PlanGoal).ID)
		if err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	for _, a := range actions {
		repair := readyTask("repair-" + a.TaskID)
		repair.Repairs, repair.DependsOn = a.TaskID, []string{a.TaskID}
		if err := e.Report(t.Context(), a.ID, a.Epoch, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}); err != nil {
			t.Fatal(err)
		}
		if a.TaskID == "b" {
			if len(revisionSnapshot(t, e).Goals[0].Tasks) != 3 {
				t.Fatal("stale sibling plan bypassed the dispatch-version fence")
			}
			fresh := revisionAction(t, e, "b", PlanGoal)
			if fresh.ID == a.ID || fresh.GoalRevision <= a.GoalRevision {
				t.Fatal("sibling repair did not get a current-input planning turn")
			}
			revisionReport(t, e, "b", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}})
		}
	}
	g := revisionSnapshot(t, e).Goals[0]
	if len(g.Tasks) != 4 || g.Tasks[0].RepairedBy != "repair-a" || g.Tasks[1].RepairedBy != "repair-b" {
		t.Fatal("independent sibling repair was discarded")
	}
}

func TestOldRepairPlanCannotReenterAfterDefinitionAssessment(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "head-a", EffectsSettled: true})
	revisionReport(t, e, "a", InspectPR, revisionPR("head-a"))
	revisionReport(t, e, "a", Merge, Observation{Status: OutcomeSucceeded, Head: "head-a", Proof: Proof{Revision: "merge-a", Ref: "merge", Passed: true}})
	revisionReport(t, e, "a", RunQA, Observation{Status: OutcomeFailed, Proof: Proof{Revision: "merge-a", Ref: "failed-qa"}})
	a, err := e.Claim(t.Context(), revisionAction(t, e, "a", PlanGoal).ID)
	if err != nil {
		t.Fatal(err)
	}
	revisionEdit(t, e)
	revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("a"), readyTask("current")}})
	repair := readyTask("stale-repair")
	repair.Repairs, repair.DependsOn = "a", []string{"a"}
	if err = e.Report(t.Context(), a.ID, a.Epoch, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}); err != nil {
		t.Fatal(err)
	}
	g := revisionSnapshot(t, e).Goals[0]
	if g.DefinitionPending || len(g.Tasks) != 2 || g.Tasks[0].RepairAsked || g.Tasks[0].RepairedBy != "" {
		t.Fatalf("obsolete repair changed the assessed definition: %+v", g)
	}
	fresh, err := e.Claim(t.Context(), revisionAction(t, e, "a", PlanGoal).ID)
	if err != nil {
		t.Fatal(err)
	}
	// A different late receipt bypasses exact-delivery deduplication, but the
	// old assessment's completed retirement cannot clear the fresh one's marker.
	if err = e.Report(t.Context(), a.ID, a.Epoch, Observation{Status: OutcomeNeedsPerson}); err != nil {
		t.Fatal(err)
	}
	if !revisionSnapshot(t, e).Goals[0].Tasks[0].RepairAsked {
		t.Fatal("late obsolete report cleared the current assessment marker")
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !revisionSnapshot(t, e).Goals[0].Tasks[0].RepairAsked {
		t.Fatal("repeated retirement cleared the current assessment marker")
	}
	repair.ID = "current-repair"
	if err = e.Report(t.Context(), fresh.ID, fresh.Epoch, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}); err != nil {
		t.Fatal(err)
	}
	if revisionSnapshot(t, e).Goals[0].Tasks[0].RepairedBy != repair.ID {
		t.Fatal("discarded plan prevented a current repair")
	}
}

func revisionEngine(t *testing.T, tasks ...Task) *Engine {
	t.Helper()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Admit(t.Context(), oneGoal(tasks...)); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	return e
}

func revisionSnapshot(t *testing.T, e *Engine) State {
	t.Helper()
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func revisionAction(t *testing.T, e *Engine, task string, kind ActionKind) Action {
	t.Helper()
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := revisionSnapshot(t, e)
	for _, a := range s.Actions {
		if a.TaskID == task && a.Kind == kind && a.State == "queued" {
			return a
		}
	}
	t.Fatalf("no queued %s for %s: %+v", kind, task, s)
	return Action{}
}

func revisionReport(t *testing.T, e *Engine, task string, kind ActionKind, o Observation) Action {
	t.Helper()
	a := revisionAction(t, e, task, kind)
	claimed, err := e.Claim(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), a.ID, claimed.Epoch, o); err != nil {
		t.Fatal(err)
	}
	return claimed
}

func revisionEdit(t *testing.T, e *Engine) {
	t.Helper()
	g := revisionSnapshot(t, e).Goals[0]
	r, rev, err := e.cfg.Records.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.cfg.Records.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: append(r.Data, []byte("\nInclude one additional task.\n")...)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	if err = e.RefreshDefinitions(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func revisionPR(head string) Observation {
	return Observation{Status: OutcomeSucceeded, PR: PRState{Head: head, Mergeable: true,
		Checks: Proof{Revision: head, Ref: "ci", Passed: true}, Review: Proof{Revision: head, Ref: "review", Passed: true}, Verified: Proof{Revision: head, Ref: "manager", Passed: true}}}
}

func TestRevisionCancelsOnlyUndispatchedWorkerReservations(t *testing.T) {
	for _, kind := range []ActionKind{Assign, Resume, RepairConflict, ContinueWorker} {
		t.Run(string(kind), func(t *testing.T) {
			e := revisionEngine(t, readyTask("a"), readyTask("live"))
			live, err := e.Claim(t.Context(), revisionAction(t, e, "live", Assign).ID)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case Assign:
			case Resume:
				revisionReport(t, e, "a", Assign, Observation{Status: OutcomeFailed, EffectsSettled: true})
			case RepairConflict:
				revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "head-a", EffectsSettled: true})
				revisionReport(t, e, "a", InspectPR, revisionPR("head-a"))
				revisionReport(t, e, "a", Merge, Observation{Status: OutcomeConflict})
			case ContinueWorker:
				revisionReport(t, e, "a", Assign, Observation{Status: OutcomeNoReceipt, Handle: "thread-a", EffectsSettled: true})
			case InspectPR, Merge, RunQA, RepairQA, Deploy, Verify, Observe, Decide, PlanGoal, CheckCriterion:
				t.Fatalf("unsupported worker reservation fixture: %s", kind)
			}
			old := revisionAction(t, e, "a", kind)
			revisionEdit(t, e)
			s := revisionSnapshot(t, e)
			if got := s.Goals[0].Tasks[0]; got.Stage == TaskBuilding {
				t.Fatalf("cancelled %s stranded task waiting for an undispatched worker: %+v", kind, got)
			}
			for _, a := range s.Actions {
				if a.ID == old.ID && a.State != "finished" || a.ID == live.ID && a.State != "claimed" {
					t.Fatalf("wrong action retired: %+v", a)
				}
			}
			if _, err = e.Claim(t.Context(), old.ID); !errors.Is(err, ErrConflict) {
				t.Fatalf("cancelled action claim: %v", err)
			}
			// Recovery is durable, and a claimed sibling still reports its result.
			cfg := e.cfg
			cfg.Epoch++
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = e.Report(t.Context(), live.ID, cfg.Epoch, Observation{Status: OutcomeSucceeded, Head: "live-head", EffectsSettled: true}); err != nil {
				t.Fatal(err)
			}
			observeRevisedWorker(t, e, live, Observation{Status: OutcomeSucceeded, Head: "live-head", EffectsSettled: true})
			revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("a"), readyTask("live"), readyTask("extra")}})
			replacement := revisionAction(t, e, "a", kind)
			if replacement.ID == old.ID || replacement.Attempt != old.Attempt {
				t.Fatalf("cancellation consumed an attempt or reused its identity: old=%+v new=%+v", old, replacement)
			}
			if _, err = e.Claim(t.Context(), replacement.ID); err != nil {
				t.Fatalf("restored action cannot dispatch: %v", err)
			}
		})
	}
}

// A late receipt is history only. The same external effect must be read with a
// fresh action token before its result can release a correction's effect wait.
func observeRevisedWorker(t *testing.T, e *Engine, original Action, receipt Observation) {
	t.Helper()
	fresh := revisionAction(t, e, original.TaskID, original.Kind)
	if fresh.ID == original.ID || fresh.ObserveActionID != original.ID || fresh.Attempt != original.Attempt {
		t.Fatalf("revised worker would be redispatched instead of observed: %+v", fresh)
	}
	claimed, err := e.Claim(t.Context(), fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Report(t.Context(), claimed.ID, e.cfg.Epoch, receipt); err != nil {
		t.Fatal(err)
	}
}

func TestRepairRevisionRederivesQueuedSiblingActions(t *testing.T) {
	e := revisionEngine(t, readyTask("a"), readyTask("b"), readyTask("waiting"))
	for _, id := range []string{"a", "b"} {
		revisionReport(t, e, id, Assign, Observation{Status: OutcomeSucceeded, Head: "head-" + id, EffectsSettled: true})
		revisionReport(t, e, id, InspectPR, revisionPR("head-"+id))
		revisionReport(t, e, id, Merge, Observation{Status: OutcomeSucceeded, Head: "head-" + id, Proof: Proof{Revision: "merge-" + id, Ref: "merge", Passed: true}})
		revisionReport(t, e, id, RunQA, Observation{Status: OutcomeFailed, Proof: Proof{Revision: "merge-" + id, Ref: "failed-qa"}})
	}
	old := revisionAction(t, e, "b", PlanGoal)
	repair := readyTask("repair-a")
	repair.Repairs, repair.DependsOn = "a", []string{"a"}
	revisionReport(t, e, "a", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}})
	// A second queued manager and an unrelated queued worker must both progress.
	for _, tc := range []struct {
		task string
		kind ActionKind
	}{{"b", PlanGoal}, {"waiting", Assign}} {
		a := revisionAction(t, e, tc.task, tc.kind)
		if a.GoalRevision != revisionSnapshot(t, e).Goals[0].Revision {
			t.Fatalf("repair left sibling %s on stale goal revision: %+v", tc.task, a)
		}
		if _, err := e.Claim(t.Context(), a.ID); err != nil {
			t.Fatalf("repair stranded sibling %s: %v", tc.task, err)
		}
	}
	if _, err := e.Claim(t.Context(), old.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("obsolete manager remains claimable: %v", err)
	}
}

func TestTaskSignalInvalidatesProofsWhileWaitingForMergeSlot(t *testing.T) {
	e := revisionEngine(t, readyTask("a"), readyTask("b"))
	for _, id := range []string{"a", "b"} {
		revisionReport(t, e, id, Assign, Observation{Status: OutcomeSucceeded, Head: "head-" + id, EffectsSettled: true})
		revisionReport(t, e, id, InspectPR, revisionPR("head-"+id))
	}
	_ = revisionAction(t, e, "a", Merge)
	if err := e.Signal(t.Context(), Signal{ID: "b-pr-changed", GoalID: "g", TaskID: "b"}); err != nil {
		t.Fatal(err)
	}
	revisionReport(t, e, "a", Merge, Observation{Status: OutcomeSucceeded, Head: "head-a", Proof: Proof{Revision: "merge-a", Ref: "merge", Passed: true}})
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := revisionSnapshot(t, e)
	for _, a := range s.Actions {
		if a.TaskID == "b" && a.Kind == Merge && a.State == "queued" {
			t.Fatal("PR change reused cached passing proofs after the merge slot opened")
		}
	}
	changed := revisionPR("new-head-b")
	changed.PR.Checks.Passed = false
	revisionReport(t, e, "b", InspectPR, changed)
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s = revisionSnapshot(t, e)
	for _, a := range s.Actions {
		if a.TaskID == "b" && a.Kind == Merge && a.State == "queued" {
			t.Fatal("fresh failing check allowed merge")
		}
	}
	if s.Goals[0].Tasks[1].Head != "head-b" || s.Goals[0].Tasks[1].PR.Head != "new-head-b" {
		t.Fatal("PR head was not read back")
	}
}

func TestInspectionCannotReplaceReceiptedHead(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "receipted-A", EffectsSettled: true})
	revisionReport(t, e, "a", InspectPR, revisionPR("unreceipted-B"))
	g := revisionSnapshot(t, e).Goals[0]
	if g.Tasks[0].Head != "receipted-A" || g.Tasks[0].PR.Head != "unreceipted-B" {
		t.Fatalf("inspection overwrote the delivered head: %+v", g.Tasks[0])
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, a := range revisionSnapshot(t, e).Actions {
		if a.Kind == Merge && a.State == "queued" {
			t.Fatalf("unreceipted head allowed merge: %+v", a)
		}
	}
}
