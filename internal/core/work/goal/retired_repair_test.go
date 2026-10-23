// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"testing"
)

func TestRetiredSiblingRepairAssessmentIsDerivedAgain(t *testing.T) {
	for _, tc := range []struct {
		name      string
		corrected bool
	}{
		{"execution-revision", false},
		{"definition-correction", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			// A canonical correction already retires the judgment. Without it,
			// the obsolete execution-revision receipt awaits reconciliation.
			if tc.corrected {
				revisionEdit(t, e)
			}
			repair.ID, repair.Repairs, repair.DependsOn = "obsolete-repair-b", "b", []string{"b"}
			late := Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}
			expected := revisionSnapshot(t, e).Goals[0]
			if !tc.corrected {
				// The first receipt clears only the obsolete assessment marker.
				expected.Tasks[1].RepairAsked = false
			}
			before := encode(expected)
			for range 2 {
				if err := e.Report(t.Context(), managers[1].ID, managers[1].Epoch, late); err != nil {
					t.Fatal(err)
				}
			}
			if after := encode(revisionSnapshot(t, e).Goals[0]); !bytes.Equal(before, after) {
				t.Fatal("stale report changed the current goal")
			}
			retired := revisionSnapshot(t, e)
			if b := retired.Goals[0].Tasks[1]; b.Stage != TaskQAFailed || b.RepairAsked || b.RepairedBy != "" || b.QA.Ref != "failed-qa" {
				t.Fatalf("retired receipt did not clear only its assessment marker: %+v", b)
			}
			found := false
			for _, a := range retired.Actions {
				if a.ID != managers[1].ID {
					continue
				}
				found = true
				if a.State != "finished" || !a.Superseded || a.RetirementReconciled != tc.corrected || !bytes.Equal(a.Receipt, encode(late)) {
					t.Fatalf("retired assessment lost its state or late receipt: %+v", a)
				}
			}
			if !found {
				t.Fatal("obsolete assessment history was lost")
			}
			cfg := e.cfg
			cfg.Epoch++
			var err error
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			// Restart must recover pending retirement, preserve settled retirement,
			// and retain the late receipt without reviving its old marker.
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			restarted := revisionSnapshot(t, e)
			for _, a := range restarted.Actions {
				if a.ID == managers[1].ID && (a.State != "finished" || !a.Superseded || !a.RetirementReconciled || !bytes.Equal(a.Receipt, encode(late))) {
					t.Fatalf("restart lost settled retirement or its late receipt: %+v", a)
				}
			}
			if b := restarted.Goals[0].Tasks[1]; b.Stage != TaskQAFailed || b.RepairedBy != "" || b.QA.Ref != "failed-qa" {
				t.Fatalf("restart changed the failed task: %+v", b)
			}
			if tc.corrected {
				if revisionSnapshot(t, e).Goals[0].Tasks[1].RepairAsked {
					t.Fatal("repair assessment bypassed the corrected definition")
				}
				currentRepair := readyTask("repair-a")
				currentRepair.Repairs, currentRepair.DependsOn = "a", []string{"a"}
				revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("a"), readyTask("b"), currentRepair, readyTask("clarification")}})
			}
			fresh := revisionAction(t, e, "b", PlanGoal)
			g := revisionSnapshot(t, e).Goals[0]
			if fresh.ID == managers[1].ID || fresh.RecordRevision != g.RecordRevision || fresh.GoalRevision != g.Revision || g.Tasks[0].RepairedBy != "repair-a" {
				t.Fatalf("missing current sibling assessment: %+v; goal=%+v", fresh, g)
			}
			for _, w := range g.Waits {
				if w.TaskID == "b" && w.Reason == "person" {
					t.Fatal("unchanged failed task was sent to the person")
				}
			}
			// A later, different obsolete response must not clear the replacement.
			repair.ID = "late-repair-b"
			before = encode(revisionSnapshot(t, e).Goals[0])
			if err := e.Report(t.Context(), managers[1].ID, cfg.Epoch, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}); err != nil {
				t.Fatal(err)
			}
			if after := encode(revisionSnapshot(t, e).Goals[0]); !bytes.Equal(before, after) {
				t.Fatal("late obsolete response changed the current goal")
			}
			if !revisionSnapshot(t, e).Goals[0].Tasks[1].RepairAsked {
				t.Fatal("late obsolete response cleared the replacement assessment")
			}
			if err := e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			plans := 0
			for _, a := range revisionSnapshot(t, e).Actions {
				if a.TaskID == "b" && a.Kind == PlanGoal && a.State == "queued" {
					plans++
					if a.ID != fresh.ID {
						t.Fatal("late obsolete response replaced the current assessment")
					}
				}
			}
			if plans != 1 {
				t.Fatalf("current repair assessments=%d, want 1", plans)
			}
		})
	}
}
