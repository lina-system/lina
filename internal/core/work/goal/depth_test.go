// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// Each task shares the preceding tasks' ancestors. Walking every path grows
// exponentially, although the graph has fewer than two edges per task.
func sharedAncestorTasks(count int) []Task {
	tasks := make([]Task, count)
	for i := range tasks {
		task := readyTask(fmt.Sprintf("task-%03d", i))
		for j := max(0, i-2); j < i; j++ {
			task.DependsOn = append(task.DependsOn, tasks[j].ID)
		}
		if i < count-1 {
			task.Stage = TaskDone
			task.Merge = Proof{Revision: "merged-" + task.ID, Ref: "merge", Passed: true}
			task.QA = Proof{Revision: task.Merge.Revision, Ref: "qa", Passed: true}
		}
		tasks[i] = task
	}
	return tasks
}

func TestDeriveSharedAncestorDepths(t *testing.T) {
	tasks := sharedAncestorTasks(128)
	deep := tasks[len(tasks)-1]
	peer := readyTask("a-peer")
	peer.DependsOn = slices.Clone(deep.DependsOn)
	shallow := readyTask("z-shallow")
	shallow.DependsOn = []string{tasks[0].ID}
	tasks = append(tasks, peer, shallow)
	// Task storage order need not be topological or match scheduling order.
	slices.Reverse(tasks)
	state := State{Goals: []Goal{oneGoal(tasks...)}, Nodes: []Node{readyNode("n")}}
	plan, err := Derive(state, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{shallow.ID, peer.ID, deep.ID}
	if len(plan.Actions) != len(want) || len(plan.Waits) != 0 {
		t.Fatalf("shared ancestors prevented ready work: %+v", plan)
	}
	for i, action := range plan.Actions {
		if action.Kind != Assign || action.TaskID != want[i] {
			t.Fatalf("wrong depth or tie ordering: %+v", plan.Actions)
		}
	}
}

func TestDeriveDepthsBelongToGoalAndSnapshot(t *testing.T) {
	tasks := sharedAncestorTasks(4)
	deep := tasks[len(tasks)-1]
	older := oneGoal(tasks...)
	older.ID, older.CreatedAt = "a-older", 1
	newer := oneGoal(readyTask(deep.ID)) // Same ID, different dependency graph.
	newer.ID, newer.CreatedAt = "z-newer", 2
	newer.Tasks[0].Repository = "other-repo"
	newer.Scope.Repositories = []string{"other-repo"}
	newer.Criteria[0].Check.Target = "other-repo"
	state := State{Goals: []Goal{older, newer}, Nodes: []Node{readyNode("n")}}
	assertOrder := func(want []string) {
		t.Helper()
		plan, err := Derive(state, time.Unix(100, 0))
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Actions) != len(want) {
			t.Fatalf("missing assignments: %+v", plan)
		}
		for i, action := range plan.Actions {
			if action.Kind != Assign || action.GoalID != want[i] || action.TaskID != deep.ID {
				t.Fatalf("wrong per-goal depth ordering: %+v", plan.Actions)
			}
		}
	}
	assertOrder([]string{newer.ID, older.ID})
	// A new snapshot removes the older goal's dependencies; age now breaks
	// the depth tie. No depth from the prior derivation may survive.
	state.Goals[0].Tasks[len(tasks)-1].DependsOn = nil
	assertOrder([]string{older.ID, newer.ID})
}

func BenchmarkDeriveSharedAncestors(b *testing.B) {
	for _, count := range []int{26, 30, 34} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			state := State{Goals: []Goal{oneGoal(sharedAncestorTasks(count)...)}, Nodes: []Node{readyNode("n")}}
			now := time.Unix(100, 0)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				plan, err := Derive(state, now)
				if err != nil || len(plan.Actions) != 1 || plan.Actions[0].Kind != Assign {
					b.Fatalf("derive: %+v, %v", plan, err)
				}
			}
		})
	}
}
