// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"reflect"
	"testing"
	"time"
)

func TestCorrectedPlanReplacesObsoleteRunnableWork(t *testing.T) {
	for _, id := range []string{"a", "replacement"} {
		t.Run(id, func(t *testing.T) {
			g := oneGoal(readyTask("a"))
			g.RecordRevision, g.Revision, g.DefinitionPending = "corrected", 2, true
			replacement := readyTask(id)
			replacement.Done = "the corrected result is verified"
			a := Action{Kind: PlanGoal, GoalRevision: g.Revision, RecordRevision: g.RecordRevision}
			if err := applySuccess(&g, nil, a, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}}, 100); err != nil {
				t.Fatalf("corrected task could not replace its old definition: %v", err)
			}
			if len(g.Tasks) != 1 || g.Tasks[0].ID != id || g.Tasks[0].Done != replacement.Done {
				t.Fatalf("obsolete task survived correction: %+v", g.Tasks)
			}
			p, err := Derive(State{Goals: []Goal{g}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Actions) != 1 || p.Actions[0].TaskID != id || p.Actions[0].Kind != Assign {
				t.Fatalf("correction dispatched an obsolete plan: %+v", p)
			}
		})
	}
}

func TestCorrectedPlanPreservesOnlyUnaffectedResults(t *testing.T) {
	for _, field := range []string{"instructions", "repository", "base", "area", "deps"} {
		t.Run(field, func(t *testing.T) {
			a, b, c, keep := readyTask("a"), readyTask("b"), readyTask("c"), readyTask("keep")
			b.DependsOn, c.DependsOn = []string{"a"}, []string{"b"}
			g := oneGoal(a, b, c, keep)
			for i := range g.Tasks {
				task := &g.Tasks[i]
				task.Stage, task.Attempt, task.Head = TaskDone, 2, "head-"+task.ID
				task.Merge = Proof{Revision: "merge-" + task.ID, Ref: "merge", Passed: true}
				task.QA = task.Merge
			}
			before := g.Tasks[3]
			g.Scope.Repositories = []string{"repo", "new-repo"}
			g.RecordRevision, g.Revision, g.DefinitionPending = "corrected", 2, true
			switch field {
			case "instructions":
				a.Done = "corrected instructions"
			case "repository":
				a.Repository = "new-repo"
			case "base":
				a.Base = "release"
			case "area":
				a.Area = "new-area"
			case "deps":
				a.DependsOn = []string{"keep"}
			}
			if err := applySuccess(&g, nil, Action{Kind: PlanGoal, RecordRevision: "corrected"}, Observation{Tasks: []Task{a, b, c, keep}}, 100); err != nil {
				t.Fatal(err)
			}
			for _, task := range g.Tasks[:3] {
				if !freshTask(task) {
					t.Fatalf("changed task or transitive dependent retained obsolete result: %+v", task)
				}
			}
			if !reflect.DeepEqual(g.Tasks[3], before) {
				t.Fatalf("unaffected result changed: %+v", g.Tasks[3])
			}
		})
	}
}

func TestCorrectedPlanSettlesRetiredClaimWithoutReplaying(t *testing.T) {
	for _, replacementID := range []string{"a", "replacement"} {
		t.Run(replacementID, func(t *testing.T) {
			e := revisionEngine(t, readyTask("a"), readyTask("queued"))
			old, err := e.Claim(t.Context(), revisionAction(t, e, "a", Assign).ID)
			if err != nil {
				t.Fatal(err)
			}
			revisionEdit(t, e)
			replacement := readyTask(replacementID)
			replacement.Done, replacement.Area = "new instructions", "a"
			revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}})
			// A second correction must not rebind the old receipt to the new task.
			revisionEdit(t, e)
			revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}})
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			s := revisionSnapshot(t, e)
			if len(s.Goals[0].Tasks) != 1 || s.Goals[0].Tasks[0].Attempt != 0 {
				t.Fatalf("obsolete tasks or attempts survived: %+v", s.Goals[0].Tasks)
			}
			for _, a := range s.Actions {
				if a.State == "queued" && a.Kind == Assign {
					t.Fatalf("released retired claim's area before outcome: %+v", a)
				}
			}
			// Durable restart reads the original action, without executing it again.
			cfg := e.cfg
			cfg.Epoch++
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = e.Report(t.Context(), old.ID, cfg.Epoch, Observation{Status: OutcomeFailed}); err != nil {
				t.Fatal(err)
			}
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, a := range revisionSnapshot(t, e).Actions {
				if a.ID == old.ID && (a.State == "finished" || !reserves(a)) {
					t.Fatal("uncertain retired effects lost reservations")
				}
			}
			o := Observation{Status: OutcomeSucceeded, Head: "old-head", EffectsSettled: true}
			if err = e.Report(t.Context(), old.ID, cfg.Epoch, o); err != nil {
				t.Fatal(err)
			}
			if err = e.Report(t.Context(), old.ID, cfg.Epoch, o); err != nil {
				t.Fatal(err)
			}
			got := revisionSnapshot(t, e).Goals[0].Tasks[0]
			if got.Head != "" || got.Stage != TaskReady {
				t.Fatalf("old result contaminated replacement: %+v", got)
			}
			current := revisionAction(t, e, replacementID, Assign)
			if current.ID == old.ID || current.Attempt != 1 {
				t.Fatalf("replacement reused retired claim: %+v", current)
			}
		})
	}
}

func TestCorrectedScopeCanReachManager(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	g := revisionSnapshot(t, e).Goals[0]
	r, rev, err := e.cfg.Records.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	g.Scope.Repositories = []string{"new-repo"}
	if _, err = e.cfg.Records.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: definitionBytes(g)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	replacement := readyTask("new")
	replacement.Repository = "new-repo"
	revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}})
	if revisionAction(t, e, "new", Assign).TaskID != "new" {
		t.Fatal("new scope did not progress")
	}
}

func TestEmptyCorrectedPlanRetiresEveryTask(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	old, err := e.Claim(t.Context(), revisionAction(t, e, "a", Assign).ID)
	if err != nil {
		t.Fatal(err)
	}
	revisionEdit(t, e)
	revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded})
	if err = e.Report(t.Context(), old.ID, old.Epoch, Observation{Status: OutcomeSucceeded, Head: "old", EffectsSettled: true}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	g := revisionSnapshot(t, e).Goals[0]
	if len(g.Tasks) != 0 || g.State != GoalBlocked {
		t.Fatalf("empty full list did not retire tasks and expose gap: %+v", g)
	}
}

func TestRetiredMergeKeepsOldRepositorySlotUntilRead(t *testing.T) {
	e := revisionEngine(t, readyTask("old"), readyTask("next"))
	for _, id := range []string{"old", "next"} {
		revisionReport(t, e, id, Assign, Observation{Status: OutcomeSucceeded, Head: "head-" + id, EffectsSettled: true})
		revisionReport(t, e, id, InspectPR, revisionPR("head-"+id))
	}
	old, err := e.Claim(t.Context(), revisionAction(t, e, "old", Merge).ID)
	if err != nil {
		t.Fatal(err)
	}
	revisionEdit(t, e)
	revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("next")}})
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, a := range revisionSnapshot(t, e).Actions {
		if a.Kind == Merge && a.State == "queued" {
			t.Fatal("released merge slot before retired effect settled")
		}
	}
	// A failed request does not prove whether the external merge happened.
	if err = e.Report(t.Context(), old.ID, old.Epoch, Observation{Status: OutcomeFailed}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, a := range revisionSnapshot(t, e).Actions {
		if a.Kind == Merge && a.State == "queued" {
			t.Fatal("uncertain retired merge released its slot")
		}
	}
	if err = e.Report(t.Context(), old.ID, old.Epoch, Observation{Status: OutcomeSucceeded, Head: "head-old", Proof: Proof{Revision: "merge-old", Ref: "old-merge", Passed: true}}); err != nil {
		t.Fatal(err)
	}
	g := revisionSnapshot(t, e).Goals[0]
	if len(g.Tasks) != 1 || g.TargetRevision != "" {
		t.Fatalf("retired merge changed current target: %+v", g)
	}
	_ = revisionAction(t, e, "next", Merge)
}
