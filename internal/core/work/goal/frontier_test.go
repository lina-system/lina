// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestFrontierWaitDoesNotConsumeWorkerCapacity(t *testing.T) {
	wait := readyTask("a")
	wait.Area, wait.Base, wait.Worker = "", "", ""
	wait.Resources = Resources{}
	// Decode the wire shape so this regression also runs before kind support.
	if err := json.Unmarshal([]byte(`{"kind":"wait","wait":{"at":200}}`), &wait); err != nil {
		t.Fatal(err)
	}
	n := readyNode("n")
	n.CPULimit = 2
	p, err := Derive(State{Goals: []Goal{oneGoal(wait, readyTask("b"))}, Nodes: []Node{n}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range p.Actions {
		ids = append(ids, a.TaskID)
	}
	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("ready wait consumed the worker slot: actions=%v waits=%+v", ids, p.Waits)
	}
}

func TestFrontierBackgroundLandingReleasesDependent(t *testing.T) {
	a, b := readyTask("a"), readyTask("b")
	a.Stage = TaskDone
	if err := json.Unmarshal([]byte(`{"kind":"background","background":{"Goal":"Read","Scope":"catalog","NotTouch":"files","Done":"Read complete","Result":"notes","Checks":[{"Name":"read","Kind":"claimed"}]},"result":{"revision":"material:1","ref":"material","passed":true}}`), &a); err != nil {
		t.Fatal(err)
	}
	b.DependsOn = []string{"a"}
	p, err := Derive(State{Goals: []Goal{oneGoal(a, b)}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range p.Actions {
		if action.TaskID == "b" && action.Kind == Assign {
			return
		}
	}
	t.Fatalf("landed background step did not release dependent: %+v", p)
}

func TestFrontierResultsOnlyGoalHasVerificationRevision(t *testing.T) {
	a, b := readyTask("a"), readyTask("b")
	for _, x := range []*Task{&a, &b} {
		x.Kind = WaitStep
		x.Wait = &StepWait{At: 1}
		x.Area, x.Base, x.Worker = "", "", ""
		x.Resources = Resources{}
		x.Stage = TaskDone
		x.Result = Proof{Revision: x.ID, Ref: "observed", Passed: true}
	}
	g := oneGoal(a, b)
	p, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil || len(p.Actions) != 1 || p.Actions[0].Kind != Verify || p.Actions[0].Revision == "" {
		t.Fatalf("result-only goal stalled without Git: %+v %v", p, err)
	}
	g.Tasks = []Task{b, a}
	again, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil || !reflect.DeepEqual(p, again) {
		t.Fatalf("result revision depends on storage order: %+v %v", again, err)
	}
	g.Tasks[0].Result = Proof{}
	if targetRevision(g) != "" {
		t.Fatal("missing result borrowed a revision")
	}
}
