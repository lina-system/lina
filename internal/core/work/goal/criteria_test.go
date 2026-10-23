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
