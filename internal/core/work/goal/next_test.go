// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func readyNode(id string) Node {
	return Node{ID: id, Connected: true, ObservedAt: 100, CPUCores: 8, CPULimit: 8, AvailableMemoryMB: 16000, MemoryLimitMB: 16000, AvailableDiskMB: 16000, DiskLimitMB: 16000, Workers: map[string]bool{"stub": true}}
}
func readyTask(id string) Task {
	return Task{ID: id, Done: "merged and QA verified", Repository: "repo", Base: "dev", Area: id, Worker: "stub", Resources: Resources{CPU: 2, MemoryMB: 1000, DiskMB: 1000}, Stage: TaskReady}
}
func oneGoal(tasks ...Task) Goal {
	return Goal{Scope: GoalScope{Repositories: []string{"repo"}, Includes: "repository changes"}, Criteria: []Criterion{{ID: "outcome", Statement: "all changes run", Check: CriterionCheck{Kind: RunEvidence, Method: "acceptance", Target: "repo"}}}, ID: "g", Person: "person", Agent: "agent", Summary: "complete project", Done: "all changes run", NoServiceReason: "library", Tasks: tasks}
}

func TestDeriveFillsLoadSizedSlotsAndReleasesDependenciesAtMerge(t *testing.T) {
	t.Parallel()
	a, b, c := readyTask("a"), readyTask("b"), readyTask("c")
	c.DependsOn = []string{"a"}
	n := readyNode("n")
	n.LoadCPU = 4
	state := State{Goals: []Goal{oneGoal(a, b, c)}, Nodes: []Node{n}}
	plan, err := Derive(state, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 2 || plan.Actions[0].TaskID != "a" || plan.Actions[1].TaskID != "b" {
		t.Fatalf("did not fill two available slots: %+v", plan)
	}
	if len(plan.Waits) != 1 || plan.Waits[0].Reason != "dependency" || plan.Waits[0].Until <= 100 {
		t.Fatalf("unnamed dependency wait: %+v", plan.Waits)
	}
	a.Stage = TaskMerged
	a.Merge = Proof{Revision: "merge-a", Ref: "merge-receipt", Passed: true}
	state.Goals[0].Tasks = []Task{a, c}
	plan, err = Derive(state, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	found := map[ActionKind]bool{}
	for _, a := range plan.Actions {
		found[a.Kind] = true
	}
	if !found[RunQA] || !found[Assign] {
		t.Fatalf("QA held the dependent or was omitted: %+v", plan)
	}
}

func TestDeriveNeverMergesWithoutExactHeadProof(t *testing.T) {
	t.Parallel()
	task := readyTask("a")
	task.Stage = TaskDelivered
	task.Head = "head"
	task.PR = PRState{Head: "head", Checks: Proof{Revision: "head", Ref: "ci", Passed: true}, Review: Proof{Revision: "head", Ref: "review", Passed: true}, Verified: Proof{Revision: "head", Ref: "manager", Passed: true}, Mergeable: true}
	for _, bad := range []string{"ci", "review", "manager", "p1", "conflict", "none"} {
		t.Run(bad, func(t *testing.T) {
			x := task
			switch bad {
			case "ci":
				x.PR.Checks.Revision = "old"
			case "review":
				x.PR.Review.Ref = ""
			case "manager":
				x.PR.Verified.Passed = false
			case "p1":
				x.PR.OpenP0P1 = 1
			case "conflict":
				x.PR.Mergeable = false
			}
			p, err := Derive(State{Goals: []Goal{oneGoal(x)}}, time.Unix(100, 0))
			if err != nil {
				t.Fatal(err)
			}
			merge := false
			for _, a := range p.Actions {
				merge = merge || a.Kind == Merge
			}
			if merge != (bad == "none") {
				t.Fatalf("unsafe merge=%v: %+v", merge, p)
			}
		})
	}
}

func TestDeriveRequiresQADeployAndVerificationBeforeClose(t *testing.T) {
	t.Parallel()
	task := readyTask("a")
	task.Stage = TaskDone
	task.Merge = Proof{Revision: "merged", Ref: "merge", Passed: true}
	task.QA = Proof{Revision: "merged", Ref: "qa", Passed: true}
	g := oneGoal(task)
	g.NoServiceReason = ""
	g.ServiceTarget = "service"
	g.TargetRevision = "merged"
	s := State{Goals: []Goal{g}}
	p, err := Derive(s, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Kind != Deploy {
		t.Fatalf("missing deploy: %+v", p)
	}
	s.Goals[0].Deployment = Proof{Revision: "merged", Ref: "deployment", Passed: true}
	p, _ = Derive(s, time.Unix(100, 0))
	if len(p.Actions) != 1 || p.Actions[0].Kind != Verify {
		t.Fatalf("deployment mistaken for done: %+v", p)
	}
	s.Goals[0].Verification = Proof{Revision: "old", Ref: "probe", Passed: true}
	p, _ = Derive(s, time.Unix(100, 0))
	if len(p.Close) != 0 {
		t.Fatal("stale runtime evidence closed goal")
	}
	s.Goals[0].Verification.Revision = "merged"
	s.Goals[0].Revision = 1
	s.Goals[0].CheckRound = 1
	for _, c := range goalCriteria(s.Goals[0]) {
		s.Goals[0].Checks = append(s.Goals[0].Checks, CriterionEvidence{Criterion: c.ID, GoalRevision: 1, Round: 1, Target: c.Check.Target, Revision: criterionRevision(s.Goals[0], c), Kind: c.Check.Kind, Collected: &CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}, Ref: "read", Passed: true})
	}
	p, _ = Derive(s, time.Unix(100, 0))
	if len(p.Close) != 1 || p.Close[0] != "g" {
		t.Fatalf("goal did not close with evidence: %+v", p)
	}
	s.Goals[0].Tasks[0].QA.Ref = ""
	p, _ = Derive(s, time.Unix(100, 0))
	if len(p.Close) != 0 {
		t.Fatal("missing QA evidence closed goal")
	}
}

func TestCapacityCountsReservationsWithoutDoubleCountingObservedWork(t *testing.T) {
	t.Parallel()
	n := readyNode("n")
	n.LoadCPU = 2
	n.ObservedActions = []string{"running"}
	reservations := []Action{{ID: "running", NodeID: "n", Kind: Assign, Resources: Resources{CPU: 2, MemoryMB: 1000, DiskMB: 1000}}, {ID: "queued", NodeID: "n", Kind: Assign, Resources: Resources{CPU: 2, MemoryMB: 1000, DiskMB: 1000}}}
	req := Resources{CPU: 2, MemoryMB: 1000, DiskMB: 1000}
	if got := Slots(n, reservations, req, time.Unix(100, 0)); got != 2 {
		t.Fatalf("slots=%d want 2", got)
	}
	n.Yielding = true
	if Slots(n, reservations, req, time.Unix(100, 0)) != 0 {
		t.Fatal("yielding Node accepted work")
	}
	n.Yielding = false
	if Slots(n, reservations, req, time.Unix(1000, 0)) != 0 {
		t.Fatal("stale Node accepted work")
	}
}

func TestDeriveDoesNotResumeAnUnreconciledDeadWorker(t *testing.T) {
	t.Parallel()
	task := readyTask("a")
	task.Stage = TaskWorkerLost
	task.Attempt = 1
	p, err := Derive(State{Goals: []Goal{oneGoal(task)}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 0 || len(p.Waits) != 1 || p.Waits[0].Reason != "unknown_effect" {
		t.Fatalf("redispatched without settling prior effects: %+v", p)
	}
}

func TestDeriveSerializesMergesIntoTheSameBase(t *testing.T) {
	t.Parallel()
	a, b := readyTask("a"), readyTask("b")
	for _, task := range []*Task{&a, &b} {
		task.Stage = TaskDelivered
		task.Head = "head-" + task.ID
		task.PR = PRState{Head: task.Head, Checks: Proof{Revision: task.Head, Ref: "ci", Passed: true}, Review: Proof{Revision: task.Head, Ref: "review", Passed: true}, Verified: Proof{Revision: task.Head, Ref: "manager", Passed: true}, Mergeable: true}
	}
	p, err := Derive(State{Goals: []Goal{oneGoal(a, b)}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Kind != Merge || len(p.Waits) != 1 || p.Waits[0].Reason != "merge_slot" {
		t.Fatalf("concurrent merges into the same base: %+v", p)
	}
}

func TestDeriveRejectsCyclesAndMissingDoneConditions(t *testing.T) {
	t.Parallel()
	a, b := readyTask("a"), readyTask("b")
	a.DependsOn = []string{"b"}
	b.DependsOn = []string{"a"}
	for _, g := range []Goal{oneGoal(a, b), oneGoal(Task{ID: "missing"})} {
		if _, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0)); err == nil {
			t.Fatal("invalid goal was scheduled")
		}
	}
}

func TestCapacityUsesDiskAndGoalDeadlineBeforeAge(t *testing.T) {
	t.Parallel()
	older := oneGoal(readyTask("old"))
	older.ID = "a-older"
	older.CreatedAt = 1
	due := oneGoal(readyTask("due"))
	due.ID = "z-due"
	due.CreatedAt = 90
	due.Deadline = 200
	n := readyNode("n")
	n.CPULimit = 2
	n.CPUCores = 2
	p, err := Derive(State{Goals: []Goal{older, due}, Nodes: []Node{n}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].GoalID != "z-due" {
		t.Fatalf("deadline goal was withheld: %+v", p)
	}
	n.AvailableDiskMB = 0
	p, err = Derive(State{Goals: []Goal{due}, Nodes: []Node{n}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 0 {
		t.Fatalf("full disk accepted assignment: %+v", p)
	}
}

func TestCapacityPrefersDependencyDepthAcrossGoalsAndPersonalNodesLast(t *testing.T) {
	t.Parallel()
	root := readyTask("root")
	root.Stage = TaskDone
	root.Merge = Proof{Revision: "r", Ref: "m", Passed: true}
	root.QA = Proof{Revision: "r", Ref: "qa", Passed: true}
	deep := readyTask("deep")
	deep.DependsOn = []string{"root"}
	old := oneGoal(root, deep)
	old.ID = "a-old"
	old.CreatedAt = 1
	shallow := oneGoal(readyTask("shallow"))
	shallow.ID = "z-new"
	shallow.CreatedAt = 90
	n := readyNode("worker")
	n.CPUCores = 2
	n.CPULimit = 2
	personal := readyNode("personal")
	personal.ForPerson = true
	p, err := Derive(State{Goals: []Goal{old, shallow}, Nodes: []Node{n, personal}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 2 || p.Actions[0].TaskID != "shallow" || p.Actions[0].NodeID != "worker" || p.Actions[1].TaskID != "deep" {
		t.Fatalf("wrong ordering/headroom preference: %+v", p.Actions)
	}
}
