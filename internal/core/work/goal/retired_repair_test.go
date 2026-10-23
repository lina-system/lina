// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"testing"
)

func TestRetiredSiblingRepairAssessmentIsDerivedAgain(t *testing.T) {
	e := revisionEngine(t, readyTask("a"), readyTask("b"))
	var managers []Action
	for _, id := range []string{"a", "b"} {
		revisionReport(t, e, id, Assign, Observation{Status: OutcomeSucceeded, Head: "head-" + id, EffectsSettled: true})
		revisionReport(t, e, id, InspectPR, revisionPR("head-"+id))
		revisionReport(t, e, id, Merge, Observation{Status: OutcomeSucceeded, Head: "head-" + id, Proof: Proof{Revision: "merge-" + id, Ref: "merge", Passed: true}})
		revisionReport(t, e, id, RunQA, Observation{Status: OutcomeFailed, Proof: Proof{Revision: "merge-" + id, Ref: "failed-qa"}})
		a, err := e.Claim(t.Context(), revisionAction(t, e, id, PlanGoal).ID)
		if err != nil {
			t.Fatal(err)
		}
		managers = append(managers, a)
	}
	repair := readyTask("repair-a")
	repair.Repairs, repair.DependsOn = "a", []string{"a"}
	if err := e.Report(t.Context(), managers[0].ID, managers[0].Epoch, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}); err != nil {
		t.Fatal(err)
	}
	if revisionSnapshot(t, e).Goals[0].Revision <= managers[1].GoalRevision {
		t.Fatal("QA repair did not advance the goal revision")
	}
	// Execution-only revisions now preserve valid sibling plans. Also force
	// actual retirement: an appended clarification changes the record revision
	// while leaving b's failed task inputs and QA evidence unchanged.
	revisionEdit(t, e)
	repair.ID, repair.Repairs, repair.DependsOn = "obsolete-repair-b", "b", []string{"b"}
	before := encode(revisionSnapshot(t, e).Goals[0])
	for range 2 {
		if err := e.Report(t.Context(), managers[1].ID, managers[1].Epoch, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}); err != nil {
			t.Fatal(err)
		}
	}
	if after := encode(revisionSnapshot(t, e).Goals[0]); !bytes.Equal(before, after) {
		t.Fatal("stale report changed the current goal")
	}
	// The central input fence retains stale reports as history. The next
	// derivation releases the retired judgment's one-shot planning flag.
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	g := revisionSnapshot(t, e).Goals[0]
	if b := g.Tasks[1]; b.Stage != TaskQAFailed || b.RepairAsked || b.RepairedBy != "" || b.QA.Ref != "failed-qa" {
		t.Fatalf("retired assessment stranded or changed its task: %+v", b)
	}
	cfg := e.cfg
	cfg.Epoch++
	var err error
	e, err = NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Accept the clarification, then require a new assessment of the same b.
	revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("clarification")}})
	fresh := revisionAction(t, e, "b", PlanGoal)
	g = revisionSnapshot(t, e).Goals[0]
	if fresh.ID == managers[1].ID || fresh.RecordRevision != g.RecordRevision || fresh.GoalRevision != g.Revision || g.Tasks[0].RepairedBy != "repair-a" {
		t.Fatalf("missing current sibling assessment: %+v; goal=%+v", fresh, g)
	}
	for _, w := range g.Waits {
		if w.TaskID == "b" && w.Reason == "person" {
			t.Fatal("unchanged failed task was sent to the person")
		}
	}
}
