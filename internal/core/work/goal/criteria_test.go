// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func finishedGoal() Goal {
	task := readyTask("a")
	task.Stage = TaskDone
	task.Merge = Proof{Revision: "merged", Ref: "merge", Passed: true}
	task.QA = Proof{Revision: "merged", Ref: "qa", Passed: true}
	g := oneGoal(task)
	g.TargetRevision = "merged"
	g.Verification = Proof{Revision: "merged", Ref: "probe", Passed: true}
	g.Revision = 1
	g.CheckRound = 1
	return g
}
func TestCodeGoalNeedsOneCheckPerCriterionAndScope(t *testing.T) {
	t.Parallel()
	g := oneGoal(readyTask("a"))
	g.Criteria = nil
	if validateGoal(g) == nil {
		t.Fatal("goal has no checked criteria")
	}
	g = oneGoal(readyTask("a"))
	g.Criteria[0].Check = CriterionCheck{}
	if validateGoal(g) == nil {
		t.Fatal("criterion has no check")
	}
	g = oneGoal(readyTask("a"))
	g.Scope.Repositories = []string{"another-repo"}
	if validateGoal(g) == nil {
		t.Fatal("task escaped goal repository scope")
	}
}
func TestCodeGoalRereadsAllCriteriaBeforeClose(t *testing.T) {
	t.Parallel()
	g := finishedGoal()
	p, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Close) != 0 || len(p.Actions) != 1 || p.Actions[0].Kind != CheckCriterion {
		t.Fatalf("closed from cached lifecycle receipts: %+v", p)
	}
	for _, c := range goalCriteria(g) {
		g.Checks = append(g.Checks, CriterionEvidence{Criterion: c.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: c.Check.Target, Revision: criterionRevision(g, c), Kind: c.Check.Kind, Collected: &CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}, Ref: "core-read:" + c.ID, Passed: true})
	}
	p, err = Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Close) != 1 {
		t.Fatalf("current collected evidence did not close: %+v", p)
	}
	g.Checks[0].Round--
	p, _ = Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if len(p.Close) != 0 {
		t.Fatal("old checking round closed goal")
	}
}
func TestCriterionObservationIsPinnedToGoalAndTarget(t *testing.T) {
	t.Parallel()
	_, db := testJournal(t)
	e, _ := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	g := finishedGoal()
	if err := e.prepareDefinition(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if err := e.journal.Update(t.Context(), Transition{ID: "fixture", Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, _ := e.Snapshot(t.Context())
	a, err := e.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	o := Observation{Status: OutcomeSucceeded, Criterion: &CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: "old", Kind: a.Check.Kind, Collected: &CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}, Ref: "read", Passed: true}}
	if err = e.Report(t.Context(), a.ID, 1, o); err == nil {
		t.Fatal("accepted another revision's evidence")
	}
	o.Criterion.Revision = a.Revision
	if err = e.Report(t.Context(), a.ID, 1, o); err != nil {
		t.Fatal(err)
	}
	s, _ = e.Snapshot(t.Context())
	if len(s.Goals[0].Checks) != 1 {
		t.Fatal("missing criterion receipt")
	}
}

func TestFailedCriterionAsksManagerOnceInsteadOfRepeatingCheck(t *testing.T) {
	t.Parallel()
	g := finishedGoal()
	c := goalCriteria(g)[0]
	g.Checks = []CriterionEvidence{{Criterion: c.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: c.Check.Target, Revision: criterionRevision(g, c), Kind: c.Check.Kind, Collected: &CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}, Ref: "failed-check-output", Passed: false}}
	p, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Kind != PlanGoal {
		t.Fatalf("failed evidence repeated without judgment: %+v", p)
	}
}

func TestRepositoryCriterionUsesItsOwnMergedRevision(t *testing.T) {
	t.Parallel()
	g := Goal{Revision: 1, CheckRound: 1, Scope: GoalScope{Repositories: []string{"code", "docs"}}, ServiceTarget: "service", ServiceRepository: "code", TargetRevision: "code-merge", MergedRevisions: map[string]string{"code": "code-merge", "docs": "docs-merge"}}
	c := Criterion{ID: "docs", Check: CriterionCheck{Kind: RunEvidence, Method: "docs.check", Target: "docs"}}
	if revision := criterionRevision(g, c); revision != "docs-merge" {
		t.Fatalf("documentation check received foreign revision: %s", revision)
	}
	evidence := CriterionEvidence{Criterion: c.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: "docs", Revision: "docs-merge", Kind: RunEvidence, Collected: &CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}, Ref: "docs-output", Passed: true}
	if !currentEvidence(g, c, evidence) {
		t.Fatal("target repository evidence rejected")
	}
	evidence.Revision = "code-merge"
	if currentEvidence(g, c, evidence) {
		t.Fatal("foreign repository evidence accepted")
	}
	delete(g.MergedRevisions, "docs")
	if revision := criterionRevision(g, c); revision != "" {
		t.Fatalf("missing docs merge borrowed %s", revision)
	}
}

func TestTaskOutcomeCheckBindsToRepairedResultRevision(t *testing.T) {
	t.Parallel()
	g := finishedGoal()
	g.Tasks[0].Stage = TaskQAFailed
	g.Tasks[0].QA.Passed = false
	g.Verification = Proof{}
	repair := readyTask("repair")
	repair.Repairs = "a"
	repair.DependsOn = []string{"a"}
	if err := applySuccess(&g, &g.Tasks[0], Action{Kind: PlanGoal, TaskID: "a"}, Observation{Tasks: []Task{repair}}, 100); err != nil {
		t.Fatal(err)
	}
	repaired := &g.Tasks[1]
	repaired.Stage = TaskMerged
	repaired.Merge = Proof{Revision: "repair-merge", Ref: "merge-2", Passed: true}
	g.TargetRevision = "repair-merge"
	if err := applySuccess(&g, repaired, Action{Kind: RunQA, Revision: "repair-merge"}, Observation{Proof: Proof{Revision: "repair-merge", Ref: "qa-2", Passed: true}}, 100); err != nil {
		t.Fatal(err)
	}
	if g.Tasks[0].Merge.Revision != "merged" || g.Tasks[0].ResultRevision != "repair-merge" {
		t.Fatalf("repair did not keep the historical merge and current result: %+v", g.Tasks[0])
	}
	outcome := Criterion{ID: "task-outcome", Statement: "the repaired task meets its outcome", Check: CriterionCheck{Kind: RunEvidence, Method: "acceptance", Target: "repo", TaskID: "a"}}
	g.Criteria = append(g.Criteria, outcome)
	if got := criterionRevision(g, outcome); got != "repair-merge" {
		t.Fatalf("task outcome check used superseded revision %s", got)
	}
	merge := Criterion{Check: CriterionCheck{Method: "merge.read", Target: "repo", TaskID: "a"}}
	if got := criterionRevision(g, merge); got != "merged" {
		t.Fatalf("merge check lost its historical revision: %s", got)
	}
	qa := Criterion{Check: CriterionCheck{Method: "qa.read", Target: "repo", TaskID: "a"}}
	if got := criterionRevision(g, qa); got != "repair-merge" {
		t.Fatalf("QA check left the repaired result: %s", got)
	}
	stale := CriterionEvidence{Criterion: outcome.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: outcome.Check.Target, Revision: "merged", Kind: outcome.Check.Kind, Collected: &CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}, Ref: "old-output", Passed: true}
	if currentEvidence(g, outcome, stale) {
		t.Fatal("superseded merge met the task outcome")
	}
	stale.Revision = "repair-merge"
	if !currentEvidence(g, outcome, stale) {
		t.Fatal("repaired result did not meet the task outcome")
	}
	for _, c := range goalCriteria(g) {
		if c.ID == outcome.ID {
			break
		}
		g.Checks = append(g.Checks, CriterionEvidence{Criterion: c.ID, GoalRevision: g.Revision, Round: g.CheckRound, Target: c.Check.Target, Revision: criterionRevision(g, c), Kind: c.Check.Kind, Collected: &CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}, Ref: "core-read:" + c.ID, Passed: true})
	}
	g.Verification = Proof{Revision: g.TargetRevision, Ref: "probe", Passed: true}
	p, err := Derive(State{Goals: []Goal{g}}, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) != 1 || p.Actions[0].Kind != CheckCriterion || p.Actions[0].CriterionID != outcome.ID || p.Actions[0].Revision != "repair-merge" {
		t.Fatalf("outcome check was not dispatched against the repaired result: %+v", p.Actions)
	}
}
