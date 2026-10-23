// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"errors"
	"testing"
)

func TestInspectionConflictSignalUsesCentralFence(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "restart"}[restart], func(t *testing.T) {
			e := revisionEngine(t, readyTask("a"))
			revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true})
			old := claimOnlyAction(t, e, InspectPR)
			if err := e.Signal(t.Context(), Signal{ID: "pr-changed", GoalID: old.GoalID, TaskID: old.TaskID}); err != nil {
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
				reviewReconcile(t, e) // Run's startup pass loads canonical definitions.
			}
			conflict := Observation{Status: OutcomeConflict, PR: PRState{Head: "head"}}
			if err := e.Report(t.Context(), old.ID, e.cfg.Epoch, conflict); err != nil {
				t.Fatal(err)
			}
			if task := revisionSnapshot(t, e).Goals[0].Tasks[0]; task.Stage != TaskDelivered || task.PR != (PRState{}) || task.Conflicts != 0 {
				t.Fatalf("stale inspection conflict changed the current task: %+v", task)
			}
			fresh := claimOnlyAction(t, e, InspectPR)
			if fresh.ID == old.ID {
				t.Fatal("reused stale inspection identity")
			}
			if err := e.Report(t.Context(), fresh.ID, e.cfg.Epoch, conflict); err != nil {
				t.Fatal(err)
			}
			repair := claimOnlyAction(t, e, RepairConflict)
			if repair.TaskID != old.TaskID || revisionSnapshot(t, e).Goals[0].Tasks[0].Conflicts != 1 {
				t.Fatal("fresh inspection conflict lost the single recovery allowance")
			}
		})
	}
}

func TestInspectionConflictReturnsToOwnWorkerOnce(t *testing.T) {
	e, worker := claimedAssignment(t)
	if err := e.Report(t.Context(), worker.ID, worker.Epoch, Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true}); err != nil {
		t.Fatal(err)
	}
	inspection := claimOnlyAction(t, e, InspectPR)
	conflict := Observation{Status: OutcomeConflict, PR: PRState{Head: "head"}}
	for range 2 {
		if err := e.Report(t.Context(), inspection.ID, inspection.Epoch, conflict); err != nil {
			t.Fatalf("inspection conflict must enter recovery: %v", err)
		}
	}
	s := revisionSnapshot(t, e)
	if task := s.Goals[0].Tasks[0]; task.Stage != TaskConflicted || task.Conflicts != 0 {
		t.Fatalf("observation spent or skipped recovery: %+v", task)
	}
	p, err := Derive(s, e.cfg.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Kind != RepairConflict || p.Actions[0].NodeID != worker.NodeID || p.Actions[0].TaskID != worker.TaskID {
		t.Fatalf("conflict did not return to its worker: %+v", p)
	}
	repair := claimOnlyAction(t, e, RepairConflict)
	if err := e.Report(t.Context(), repair.ID, repair.Epoch, Observation{Status: OutcomeSucceeded, Head: "rebased", EffectsSettled: true}); err != nil {
		t.Fatal(err)
	}
	// A repair still needs fresh exact-head proofs before a merge.
	claimOnlyAction(t, e, InspectPR)
}

func TestInspectionSecondConflictNeedsPerson(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true})
	revisionReport(t, e, "a", InspectPR, revisionPR("head"))
	// Inspection and merge conflicts share the same single repair allowance.
	revisionReport(t, e, "a", Merge, Observation{Status: OutcomeConflict})
	revisionReport(t, e, "a", RepairConflict, Observation{Status: OutcomeSucceeded, Head: "rebased", EffectsSettled: true})
	revisionReport(t, e, "a", InspectPR, Observation{Status: OutcomeConflict, PR: PRState{Head: "rebased"}})
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := revisionSnapshot(t, e)
	g := s.Goals[0]
	if g.Tasks[0].Conflicts != 1 || g.Tasks[0].Stage != TaskConflicted || g.State != GoalBlocked || g.AttentionID == "" {
		t.Fatalf("second conflict did not need a decision: %+v", g)
	}
	if len(g.Waits) != 1 || g.Waits[0].Reason != "person" || g.Waits[0].Wake != "decision.resolved" {
		t.Fatalf("missing person wait: %+v", g.Waits)
	}
	for _, a := range s.Actions {
		if a.State != "finished" {
			t.Fatalf("second conflict dispatched more work: %+v", a)
		}
	}
}

func TestInspectionUnknownMergeabilityKeepsInspecting(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true})
	observation := revisionPR("head")
	observation.PR.Mergeable = false // Not yet computed; no explicit conflict.
	revisionReport(t, e, "a", InspectPR, observation)
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	g := revisionSnapshot(t, e).Goals[0]
	if g.Tasks[0].Stage != TaskDelivered || g.Tasks[0].Conflicts != 0 || len(g.Waits) != 1 || g.Waits[0].Reason != "ci_review" {
		t.Fatalf("unknown mergeability triggered recovery: %+v", g)
	}
	if err := e.Signal(t.Context(), Signal{ID: "mergeability-ready", GoalID: g.ID, TaskID: "a"}); err != nil {
		t.Fatal(err)
	}
	claimOnlyAction(t, e, InspectPR)
}

func TestInspectionConflictRequiresCurrentHead(t *testing.T) {
	for _, head := range []string{"", "other-head"} {
		t.Run("head="+head, func(t *testing.T) {
			e := revisionEngine(t, readyTask("a"))
			revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "head", EffectsSettled: true})
			inspection := claimOnlyAction(t, e, InspectPR)
			err := e.Report(t.Context(), inspection.ID, inspection.Epoch, Observation{Status: OutcomeConflict, PR: PRState{Head: head}})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("mismatched conflict accepted: %v", err)
			}
			if task := revisionSnapshot(t, e).Goals[0].Tasks[0]; task.Stage != TaskDelivered || task.Head != "head" || task.Conflicts != 0 {
				t.Fatalf("mismatched conflict changed task: %+v", task)
			}
		})
	}
}
