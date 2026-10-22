// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"fmt"
	"testing"
	"time"
)

func TestDeriveWideSharedAncestorDAG(t *testing.T) {
	var tasks []Task
	var parents []string
	// 128 completed ancestors encode over 4^31 paths. Ranking the four
	// leaves must visit the graph, not enumerate all paths through it.
	for level := range 32 {
		var layer []string
		for branch := range 4 {
			id := fmt.Sprintf("ancestor-%02d-%d", level, branch)
			task := readyTask(id)
			task.DependsOn = append([]string(nil), parents...)
			task.Stage = TaskDone
			task.Merge = Proof{Revision: "merge-" + id, Ref: "merge", Passed: true}
			task.QA = task.Merge
			tasks = append(tasks, task)
			layer = append(layer, id)
		}
		parents = layer
	}
	for branch := range 4 {
		task := readyTask(fmt.Sprintf("leaf-%d", branch))
		task.DependsOn = append([]string(nil), parents...)
		tasks = append(tasks, task)
	}
	// This lexical-last task has no ancestors and must rank before the leaves.
	tasks = append(tasks, readyTask("zz-independent"))
	p, err := Derive(State{Goals: []Goal{oneGoal(tasks...)}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 4 || p.Actions[0].TaskID != "zz-independent" {
		t.Fatalf("dependency depth ranking changed: %+v", p)
	}
	for i, a := range p.Actions[1:] {
		if a.Kind != Assign || a.TaskID != fmt.Sprintf("leaf-%d", i) {
			t.Fatalf("shared ancestors prevented ready work: %+v", p)
		}
	}
}
