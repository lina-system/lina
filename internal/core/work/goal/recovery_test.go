// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"fmt"
	"reflect"
	"testing"
)

// Keep the expected fields independent of the production kind table.
var recoveryCases = []struct {
	kind    ActionKind
	stage   TaskStage
	counter func(*Task) *int
}{
	{Assign, TaskReady, nil},
	{Resume, TaskWorkerLost, func(t *Task) *int { return &t.Recoveries }},
	{RepairConflict, TaskConflicted, func(t *Task) *int { return &t.Conflicts }},
	{RepairQA, TaskQAFailed, func(t *Task) *int { return &t.QARepairs }},
	{ContinueWorker, TaskNoReceipt, func(t *Task) *int { return &t.Continuations }},
}

func TestActionKindRecoverySpec(t *testing.T) {
	for _, tc := range recoveryCases {
		t.Run(string(tc.kind), func(t *testing.T) {
			spec := kindSpecs[tc.kind]
			if spec.priorStage != tc.stage {
				t.Errorf("prior stage = %q, want %q", spec.priorStage, tc.stage)
			}
			if tc.counter == nil {
				if spec.counter != nil {
					t.Fatal("assignment must not consume recovery")
				}
				return
			}
			if spec.counter == nil {
				t.Fatal("missing recovery counter")
			}
			task := Task{}
			if spec.counter(&task) != tc.counter(&task) {
				t.Fatal("counter selects the wrong task field")
			}
		})
	}
	for kind, spec := range kindSpecs {
		if !spec.worker && (spec.priorStage != "" || spec.counter != nil) {
			t.Errorf("non-worker %s has worker recovery state", kind)
		}
	}
}

func TestRestoreWorkerAwardsPreservesPathRules(t *testing.T) {
	for _, tc := range recoveryCases {
		for _, count := range []int{0, 2} {
			for _, denied := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/count=%d/denied=%t", tc.kind, count, denied), func(t *testing.T) {
					task := Task{ID: "task", Stage: TaskBuilding, NodeID: "host", Attempt: count,
						Recoveries: count, Conflicts: count, QARepairs: count, Continuations: count,
						RepairAsked: true, Head: "retained", ThreadHandle: "thread"}
					want := task
					want.Stage = tc.stage
					want.Attempt--
					want.EffectsSettled = !denied || tc.kind != Assign
					if tc.kind == Assign {
						want.NodeID = ""
					}
					if tc.counter != nil {
						*tc.counter(&want)--
						if denied {
							*tc.counter(&want) = max(0, *tc.counter(&want))
						}
					}
					a := Action{Kind: tc.kind, TaskID: task.ID, Attempt: count, State: "queued"}
					if denied {
						want.Attempt = max(0, want.Attempt)
						restoreDeniedAssignment(&task, a)
					} else {
						g := Goal{Tasks: []Task{task}}
						restoreUndispatchedTask(&g, a)
						task = g.Tasks[0]
					}
					if !reflect.DeepEqual(task, want) {
						t.Fatalf("restored task = %+v, want %+v", task, want)
					}
				})
			}
		}
	}
}

func TestRestoreAwardGuardsAndPlanning(t *testing.T) {
	initial := Task{ID: "task", Stage: TaskBuilding, Attempt: 2, NodeID: "host", RepairAsked: true}
	for _, a := range []Action{
		{Kind: Resume, Attempt: 1},
		{Kind: Assign, Attempt: 2, State: "finished"},
		{Kind: PlanGoal, Attempt: 2},
	} {
		task := initial
		restoreDeniedAssignment(&task, a)
		if !reflect.DeepEqual(task, initial) {
			t.Fatalf("denied action %+v changed task: %+v", a, task)
		}
	}
	restoreDeniedAssignment(nil, Action{Kind: Assign})
	for _, kind := range []ActionKind{InspectPR, Merge, RunQA, Deploy, Verify, Observe, Decide, CheckCriterion, PlanGoal, "unknown"} {
		g := Goal{Tasks: []Task{initial}}
		restoreUndispatchedTask(&g, Action{Kind: kind, TaskID: initial.ID})
		want := initial
		if kind == PlanGoal {
			want.RepairAsked = false
		}
		if !reflect.DeepEqual(g.Tasks[0], want) {
			t.Fatalf("undispatched %s changed unrelated state: %+v", kind, g.Tasks[0])
		}
	}
	g := Goal{Tasks: []Task{initial}}
	restoreUndispatchedTask(&g, Action{Kind: Assign, TaskID: "missing"})
	if !reflect.DeepEqual(g.Tasks[0], initial) {
		t.Fatal("missing task changed another task")
	}
}
