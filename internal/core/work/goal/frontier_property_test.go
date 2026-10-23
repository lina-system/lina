// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"
)

// The oracle does not call Derive, Slots, stepLanded or taskDone. Graphs have
// integer CPU requests and ample memory/disk, so a separate accounting model
// can check the complete same-pass award rather than only checking safety.
func TestFrontierGeneratedGraphsAndEventOrders(t *testing.T) {
	for seed := int64(0); seed < 100; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			tasks := make([]Task, 6+rng.Intn(15))
			depth := map[string]int{}
			for i := range tasks {
				x := readyTask(fmt.Sprintf("s%02d", i))
				x.Resources.CPU = float64(1 + rng.Intn(3))
				x.Area = x.ID
				switch i % 3 {
				case 1:
					x.Kind = BackgroundStep
					x.Background = &BackgroundBrief{Goal: "Read", Scope: "catalog", NotTouch: "files", Done: "read", Result: "notes", Checks: []BackgroundCheck{{Name: "read", Kind: "claimed"}}}
				case 2:
					x.Kind = WaitStep
					x.Wait = &StepWait{At: 200}
					x.Resources = Resources{}
					x.Area, x.Base, x.Worker = "", "", ""
				}
				for j := 0; j < i; j++ {
					if rng.Intn(5) == 0 {
						x.DependsOn = append(x.DependsOn, tasks[j].ID)
						depth[x.ID] = max(depth[x.ID], depth[tasks[j].ID]+1)
					}
				}
				tasks[i] = x
			}
			var steps []Step
			for _, x := range tasks {
				steps = append(steps, Step{ID: x.ID, Name: "Prepare output", Kind: x.Kind, Done: x.Done, Repository: x.Repository, Base: x.Base, Area: x.Area, Worker: x.Worker, Resources: x.Resources, DependsOn: x.DependsOn, Background: x.Background, Wait: x.Wait})
			}
			if err := validateSteps(oneGoal(tasks...), steps); err != nil {
				t.Fatalf("generated graph was inadmissible: %v", err)
			}
			holder := readyTask("external")
			holder.Area = tasks[0].Area
			holder.Stage = TaskBuilding
			otherGoal := oneGoal(holder)
			otherGoal.ID = "other"
			state := State{Goals: []Goal{oneGoal(tasks...), otherGoal}, Nodes: []Node{readyNode("a"), readyNode("b")}, Actions: []Action{{ID: "external", GoalID: "other", TaskID: "external", Kind: Assign, NodeID: "a", Resources: holder.Resources, State: "running"}}}
			for event := 0; event < 50; event++ {
				for i := range state.Nodes {
					state.Nodes[i].LoadCPU = float64(rng.Intn(5))
					state.Nodes[i].QuotaReset = map[string]int64{"stub": 0}
					if rng.Intn(4) == 0 {
						state.Nodes[i].QuotaReset["stub"] = 200
					}
				}
				before := string(encode(state))
				p, err := Derive(state, time.Unix(100, 0))
				if err != nil {
					t.Fatal(err)
				}
				if before != string(encode(state)) {
					t.Fatal("derivation mutated the ledger")
				}
				again, err := Derive(state, time.Unix(100, 0))
				if err != nil || !reflect.DeepEqual(p, again) {
					t.Fatalf("unstable repeated derivation: %v", err)
				}
				permuted := state
				permuted.Goals = append([]Goal(nil), state.Goals...)
				permuted.Goals[0].Tasks = append([]Task(nil), tasks...)
				rng.Shuffle(len(tasks), func(i, j int) {
					permuted.Goals[0].Tasks[i], permuted.Goals[0].Tasks[j] = permuted.Goals[0].Tasks[j], permuted.Goals[0].Tasks[i]
				})
				permuted.Goals[0], permuted.Goals[1] = permuted.Goals[1], permuted.Goals[0]
				permuted.Nodes = []Node{state.Nodes[1], state.Nodes[0]}
				other, err := Derive(permuted, time.Unix(100, 0))
				if err != nil || !reflect.DeepEqual(p, other) {
					t.Fatalf("storage order changed derivation: %v", err)
				}
				landed := map[string]bool{}
				held := map[string]bool{holder.Area: true}
				for _, x := range tasks {
					landed[x.ID] = x.Stage == TaskDone && ((x.Kind == "" && x.Merge.Passed && x.Merge.Ref != "") || (x.Kind != "" && x.Result.Passed && x.Result.Ref != ""))
					if x.Stage == TaskBuilding || x.Stage == TaskNeedsPerson {
						if x.Kind != WaitStep {
							held[x.Area] = true
						}
					}
				}
				var frontier []Task
				for _, x := range tasks {
					if x.Stage != TaskReady {
						continue
					}
					ok := true
					for _, id := range x.DependsOn {
						ok = ok && landed[id]
					}
					if ok {
						frontier = append(frontier, x)
					}
				}
				sort.Slice(frontier, func(i, j int) bool {
					if depth[frontier[i].ID] != depth[frontier[j].ID] {
						return depth[frontier[i].ID] < depth[frontier[j].ID]
					}
					return frontier[i].ID < frontier[j].ID
				})
				var expectedFrontier []FrontierStep
				for _, x := range frontier {
					kind := x.Kind
					if kind == "" {
						kind = TaskStep
					}
					expectedFrontier = append(expectedFrontier, FrontierStep{"g", x.ID, kind})
				}
				if !reflect.DeepEqual(expectedFrontier, p.Frontier) {
					t.Fatalf("event %d frontier: want %+v got %+v", event, expectedFrontier, p.Frontier)
				}
				free := map[string]int{}
				for _, n := range state.Nodes {
					free[n.ID] = 8 - int(n.LoadCPU)
				}
				for _, a := range state.Actions {
					if a.State != "finished" && a.StepKind != WaitStep {
						free[a.NodeID] -= int(a.Resources.CPU)
					}
				}
				var expected []string
				for _, x := range frontier {
					if x.Kind == WaitStep {
						expected = append(expected, x.ID)
						continue
					}
					if held[x.Area] {
						continue
					}
					best := ""
					score := -1
					for _, n := range state.Nodes {
						if n.QuotaReset["stub"] > 100 {
							continue
						}
						slots := free[n.ID] / int(x.Resources.CPU)
						if slots > 0 && slots > score {
							best = n.ID
							score = slots
						}
					}
					if best == "" {
						continue
					}
					expected = append(expected, x.ID)
					free[best] -= int(x.Resources.CPU)
					held[x.Area] = true
				}
				var actual []string
				for _, a := range p.Actions {
					if a.Kind != Assign {
						continue
					}
					actual = append(actual, a.TaskID)
					state.Actions = append(state.Actions, a)
					for i := range tasks {
						if tasks[i].ID == a.TaskID {
							tasks[i].Stage = TaskBuilding
						}
					}
				}
				if !reflect.DeepEqual(expected, actual) {
					t.Fatalf("event %d incomplete or unsafe pass: want %v got %v", event, expected, actual)
				}
				// Events land random subsets in arbitrary order, including duplicate
				// observations and invalid proofs that must not release dependents.
				for _, i := range rng.Perm(len(tasks)) {
					x := &tasks[i]
					if x.Stage != TaskBuilding || rng.Intn(3) != 0 {
						continue
					}
					x.Stage = TaskDone
					proof := Proof{Revision: x.ID, Ref: "observed", Passed: true}
					if rng.Intn(5) == 0 {
						proof.Passed = false
					}
					if x.Kind == "" {
						x.Merge = proof
						x.QA = proof
					} else {
						x.Result = proof
					}
					for j := range state.Actions {
						if state.Actions[j].TaskID == x.ID {
							state.Actions[j].State = "finished"
						}
					}
				}
				// Failed terminal observations remain blocked instead of becoming
				// impossible TaskDone code states that invoke unrelated QA work.
				for i := range tasks {
					x := &tasks[i]
					if x.Stage == TaskDone && ((x.Kind == "" && !x.Merge.Passed) || (x.Kind != "" && !x.Result.Passed)) {
						x.Stage = TaskNeedsPerson
					}
				}
				state.Goals[0].Tasks = tasks
			}
		})
	}
}
