// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func TestQAFailureBecomesManagerRepairTask(t *testing.T) {
	g := finishedGoal()
	g.Tasks[0].Stage = TaskQAFailed
	g.Tasks[0].QA.Passed = false
	g.Verification = Proof{}
	p, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Kind != PlanGoal || p.Actions[0].TaskID != "a" {
		t.Fatalf("QA did not become scoped manager repair: %+v", p)
	}
}

func TestPassingRepairQASupersedesFailedCommitWithoutErasingEvidence(t *testing.T) {
	g := finishedGoal()
	g.Tasks[0].Stage = TaskQAFailed
	g.Tasks[0].QA.Passed = false
	g.Verification = Proof{}
	g.Evidence = []Proof{g.Tasks[0].QA}
	repair := readyTask("repair")
	repair.Repairs = "a"
	repair.DependsOn = []string{"a"}
	if err := applySuccess(&g, &g.Tasks[0], Action{Kind: PlanGoal, TaskID: "a"}, Observation{Tasks: []Task{repair}}, 100); err != nil {
		t.Fatal(err)
	}
	if len(g.Tasks) != 2 || g.Tasks[0].RepairedBy != "repair" {
		t.Fatalf("repair not linked to failed commit: %+v", g.Tasks)
	}
	t2 := &g.Tasks[1]
	t2.Stage = TaskMerged
	t2.Merge = Proof{Revision: "repair-merge", Ref: "merge-2", Passed: true}
	g.TargetRevision = "repair-merge"
	if err := applySuccess(&g, t2, Action{Kind: RunQA, Revision: "repair-merge"}, Observation{Proof: Proof{Revision: "repair-merge", Ref: "qa-2", Passed: true}}, 100); err != nil {
		t.Fatal(err)
	}
	if !taskDone(g.Tasks[0]) || g.Tasks[0].Merge.Revision != "merged" || g.Tasks[0].QA.Revision != "repair-merge" {
		t.Fatalf("failed result was not superseded: %+v", g.Tasks[0])
	}
	if len(g.Evidence) != 2 || g.Evidence[0].Passed {
		t.Fatal("failed QA evidence was erased")
	}
	p, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Kind != Verify {
		t.Fatalf("repaired goal did not advance: %+v", p)
	}
}
