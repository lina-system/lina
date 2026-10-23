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
				if task.Attempt != 2 {
					t.Fatalf("changed task lost its attempt identity: %+v", task)
				}
				task.Attempt = 0
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
			if err = e.Report(t.Context(), old.ID, old.Epoch, Observation{Status: OutcomeFailed}); err != nil {
				t.Fatal(err)
			}
			read := revisionAction(t, e, "a", Assign)
			if read.ObserveActionID != old.ID {
				t.Fatal("correction would replay the effect")
			}
			read, err = e.Claim(t.Context(), read.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Report(t.Context(), read.ID, read.Epoch, Observation{Status: OutcomeUnknown}); err != nil {
				t.Fatal(err)
			}
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, a := range revisionSnapshot(t, e).Actions {
				if a.Kind == PlanGoal && a.State == "queued" {
					t.Fatal("planned over uncertain effect")
				}
				if a.ID == read.ID && !reserves(a) {
					t.Fatal("lost reservation")
				}
			}
			cfg := e.cfg
			cfg.Epoch++
			e, err = NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			o := Observation{Status: OutcomeSucceeded, Head: "old-head", EffectsSettled: true}
			if err = e.Report(t.Context(), read.ID, cfg.Epoch, o); err != nil {
				t.Fatal(err)
			}
			replacement := readyTask(replacementID)
			replacement.Done, replacement.Area = "new instructions", "a"
			revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}})
			revisionEdit(t, e)
			revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}})
			current := revisionAction(t, e, replacementID, Assign)
			wantAttempt := 1
			if replacementID == old.TaskID {
				wantAttempt = old.Attempt + 1
			}
			if current.ID == old.ID || current.Attempt != wantAttempt || current.ObserveActionID != "" {
				t.Fatalf("replacement reused old claim: %+v", current)
			}
			// A delayed report after ID reuse is history, never a new read of
			// that old worker into the current task.
			if err = e.Report(t.Context(), read.ID, cfg.Epoch, Observation{Status: OutcomeSucceeded, Head: "late-head", EffectsSettled: true}); err != nil {
				t.Fatal(err)
			}
			if err = e.Reconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			s := revisionSnapshot(t, e)
			if len(s.Goals[0].Tasks) != 1 || s.Goals[0].Tasks[0].Head != "" {
				t.Fatal("obsolete result contaminated replacement")
			}
			for _, a := range s.Actions {
				if a.State != "finished" && a.ObserveActionID != "" {
					t.Fatal("retired history was read into new task")
				}
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
	if err = e.Report(t.Context(), old.ID, old.Epoch, Observation{Status: OutcomeSucceeded, Head: "old", EffectsSettled: true}); err != nil {
		t.Fatal(err)
	}
	observeRevisedWorker(t, e, old, Observation{Status: OutcomeSucceeded, Head: "old", EffectsSettled: true})
	revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded})
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
	if err = e.Report(t.Context(), old.ID, old.Epoch, Observation{Status: OutcomeFailed}); err != nil {
		t.Fatal(err)
	}
	read := revisionAction(t, e, "old", Merge)
	if read.ObserveActionID != old.ID {
		t.Fatal("retired merge would execute again")
	}
	read, err = e.Claim(t.Context(), read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), read.ID, read.Epoch, Observation{Status: OutcomeUnknown}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, a := range revisionSnapshot(t, e).Actions {
		if (a.Kind == Merge || a.Kind == PlanGoal) && a.State == "queued" {
			t.Fatal("released retired merge slot or planned before outcome")
		}
	}
	o := Observation{Status: OutcomeSucceeded, Head: "head-old", Proof: Proof{Revision: "merge-old", Ref: "old-merge", Passed: true}}
	if err = e.Report(t.Context(), read.ID, read.Epoch, o); err != nil {
		t.Fatal(err)
	}
	revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{readyTask("next")}})
	g := revisionSnapshot(t, e).Goals[0]
	if len(g.Tasks) != 1 || len(g.Evidence) == 0 {
		t.Fatal("correction lost retained merge evidence")
	}
	_ = revisionAction(t, e, "next", Merge)
}

func TestCorrectionPlanKeepsAcceptedLateReceiptTerminal(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	old := revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "old-head", EffectsSettled: true})
	revisionEdit(t, e)
	manager, err := e.Claim(t.Context(), revisionAction(t, e, "", PlanGoal).ID)
	if err != nil {
		t.Fatal(err)
	}
	// Dev's central fence preserves an already accepted receipt. It cannot
	// create a new read while the corrected plan is being interpreted.
	if err = e.Report(t.Context(), old.ID, old.Epoch, Observation{Status: OutcomeSucceeded, Head: "late-head", EffectsSettled: true}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := revisionSnapshot(t, e)
	if before.Goals[0].Tasks[0].Head != "old-head" {
		t.Fatal("late receipt replaced accepted head")
	}
	for _, a := range before.Actions {
		if a.ObserveActionID != "" || a.Kind == Assign && a.State != "finished" {
			t.Fatal("accepted receipt reopened while planning")
		}
	}
	replacement := readyTask("a")
	replacement.Done = "changed"
	if err = e.Report(t.Context(), manager.ID, manager.Epoch, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}}); err != nil {
		t.Fatal(err)
	}
	g := revisionSnapshot(t, e).Goals[0]
	if g.DefinitionPending || g.Tasks[0].Done != replacement.Done || g.Tasks[0].Head != "" {
		t.Fatal("corrected plan inherited obsolete evidence")
	}
}

func TestCompletedCorrectionHistoryCannotReopenRetiredMerge(t *testing.T) {
	e := revisionEngine(t, readyTask("a"))
	revisionReport(t, e, "a", Assign, Observation{Status: OutcomeSucceeded, Head: "old-head", EffectsSettled: true})
	revisionReport(t, e, "a", InspectPR, revisionPR("old-head"))
	old := revisionReport(t, e, "a", Merge, Observation{Status: OutcomeSucceeded, Head: "old-head", Proof: Proof{Revision: "merged", Ref: "merge", Passed: true}})
	revisionEdit(t, e)
	replacement := readyTask("a")
	replacement.Done = "changed instructions"
	revisionReport(t, e, "", PlanGoal, Observation{Status: OutcomeSucceeded, Tasks: []Task{replacement}})
	// A delayed progress observation must not reopen an effect already read
	// to settlement and retired before this task ID was reused.
	for _, out := range []Observation{{Status: OutcomeRunning, Handle: "old-merge"}, {Status: OutcomeFailed}} {
		if err := e.Report(t.Context(), old.ID, old.Epoch, out); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, a := range revisionSnapshot(t, e).Actions {
		if a.ID == old.ID && a.State != "finished" {
			t.Fatalf("settled merge reopened: %+v", a)
		}
	}
	_ = revisionAction(t, e, "a", Assign)
}
