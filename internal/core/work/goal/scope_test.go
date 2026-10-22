// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"testing"
	"time"
)

func narrowRepositories(t *testing.T, e *Engine, records Definitions, repository string) {
	t.Helper()
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	g := s.Goals[0]
	g.Scope.Repositories = []string{repository}
	g.Criteria[0].Check.Target = repository
	r, rev, err := records.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = records.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: definitionBytes(g)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	if err = e.RefreshDefinitions(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestScopeCorrectionReplansAndPreservesLandedResults(t *testing.T) {
	_, db := testJournal(t)
	cfg := Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }}
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := finishedGoal()
	g.Scope.Repositories = append(g.Scope.Repositories, "kept")
	kept := readyTask("kept")
	kept.Repository = "kept"
	kept.DependsOn = []string{"a"}
	kept.Stage = TaskDone
	kept.Merge = Proof{Revision: "kept-merge", Ref: "kept-merge-output", Passed: true}
	kept.QA = Proof{Revision: "kept-merge", Ref: "kept-qa-output", Passed: true}
	g.Tasks = append(g.Tasks, kept)
	g.Evidence = []Proof{g.Tasks[0].Merge, kept.Merge, kept.QA}
	if err = e.prepareDefinition(t.Context(), &g); err != nil {
		t.Fatal(err)
	}
	if err = e.journal.Update(t.Context(), Transition{ID: "fixture", Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
		t.Fatal(err)
	}
	narrowRepositories(t, e, db.Definitions, "kept")
	// Startup must accept the correction even though retained historical tasks
	// name the removed repository.
	cfg.Epoch++
	e, err = NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := claimOnlyAction(t, e, PlanGoal)
	repair := readyTask("new-work")
	repair.Repository = "kept"
	if err = e.Report(t.Context(), a.ID, cfg.Epoch, Observation{Status: OutcomeSucceeded, Tasks: []Task{repair}}); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	current := s.Goals[0]
	if current.DefinitionPending || len(current.Tasks) != 2 || len(current.Evidence) != 3 {
		t.Fatalf("correction lost work/evidence: %+v", current)
	}
	if current.Tasks[0].ID != kept.ID || current.Tasks[0].Merge != kept.Merge || current.Tasks[0].QA != kept.QA || len(current.Tasks[0].DependsOn) != 0 {
		t.Fatalf("landed work was not preserved: %+v", current.Tasks)
	}
	if current.Evidence[0] != g.Tasks[0].Merge {
		t.Fatal("removed repository's result evidence was erased")
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	assigned := claimOnlyAction(t, e, Assign)
	if assigned.TaskID != repair.ID {
		t.Fatalf("wrong work resumed: %+v", assigned)
	}
}

func TestScopeCorrectionWaitsForEffectsWithoutBlockingOtherGoals(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("old"))
	g.Scope.Repositories = append(g.Scope.Repositories, "kept")
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	worker := claimOnlyAction(t, e, Assign)
	if err = e.Report(t.Context(), worker.ID, 1, Observation{Status: OutcomeUnknown}); err != nil {
		t.Fatal(err)
	}
	narrowRepositories(t, e, db.Definitions, "kept")
	other := oneGoal(readyTask("independent"))
	other.ID = "other"
	if err = e.Admit(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatalf("correction blocked all goals: %v", err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	independent := false
	for _, a := range s.Actions {
		if a.GoalID == g.ID && a.Kind == PlanGoal {
			t.Fatal("planned over an unsettled effect")
		}
		independent = independent || a.GoalID == other.ID && a.Kind == Assign
		if a.ID == worker.ID && a.State != "unknown" {
			t.Fatal("scope edit settled an unknown worker")
		}
	}
	if !independent {
		t.Fatal("independent goal did not advance")
	}
	if err = e.Report(t.Context(), worker.ID, 1, Observation{Status: OutcomeSucceeded, Head: "old-result", EffectsSettled: true}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plans := 0
	for _, a := range s.Actions {
		if a.GoalID == g.ID && a.Kind == PlanGoal && a.State == "queued" {
			plans++
		}
	}
	if plans != 1 {
		t.Fatalf("settled correction did not reach manager: %+v", s.Actions)
	}
}

func TestCorrectionRestoresUndispatchedWork(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("a"))
	g.Scope.Repositories = append(g.Scope.Repositories, "unused")
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	narrowRepositories(t, e, db.Definitions, "repo")
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	task := s.Goals[0].Tasks[0]
	if task.Stage != TaskReady || task.Attempt != 0 || task.NodeID != "" {
		t.Fatalf("undispatched work stranded: %+v", task)
	}
}

func TestScopeCorrectionReplansDependentsWithoutInputs(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	predecessor, dependent := readyTask("old"), readyTask("dependent")
	dependent.Repository = "kept"
	dependent.DependsOn = []string{predecessor.ID}
	g := oneGoal(predecessor, dependent)
	g.Scope.Repositories = append(g.Scope.Repositories, "kept")
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	narrowRepositories(t, e, db.Definitions, "kept")
	plan := claimOnlyAction(t, e, PlanGoal)
	replacement := readyTask("replacement")
	replacement.Repository = "kept"
	if err = e.Report(t.Context(), plan.ID, 1, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}}); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Goals[0].Tasks) != 1 || s.Goals[0].Tasks[0].ID != replacement.ID {
		t.Fatalf("kept a task whose input never landed: %+v", s.Goals[0].Tasks)
	}
}

func TestAdmissionCannotInjectCorrectionFence(t *testing.T) {
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	g := oneGoal(readyTask("outside"))
	g.Scope.Repositories = []string{"another-repo"}
	g.DefinitionPending = true
	if err = e.Admit(t.Context(), g); err == nil {
		t.Fatal("admission bypassed scope with a supplied correction fence")
	}
}
