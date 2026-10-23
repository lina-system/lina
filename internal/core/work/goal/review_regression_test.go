// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func reviewEngine(t *testing.T, goals ...Goal) *Engine {
	t.Helper()
	_, db := testJournal(t)
	e, err := NewEngine(Config{DB: db, Records: db.Definitions, Epoch: 1, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range goals {
		if g.Revision == 0 {
			g.Revision = 1
		}
		g.State = GoalRunning
		if err = e.prepareDefinition(t.Context(), &g); err != nil {
			t.Fatal(err)
		}
		if err = e.journal.Update(t.Context(), Transition{ID: "fixture:" + g.ID, Kind: "fixture", At: 100}, func(v *View) error { return put(v, "goal", g.ID, g) }); err != nil {
			t.Fatal(err)
		}
	}
	return e
}
func reviewGoal(t *testing.T, e *Engine, id string) Goal {
	t.Helper()
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range s.Goals {
		if g.ID == id {
			return g
		}
	}
	t.Fatal("goal missing", id)
	return Goal{}
}
func reviseReviewGoal(t *testing.T, e *Engine, id string, edit func(*Goal)) {
	t.Helper()
	g := reviewGoal(t, e, id)
	r, rev, err := e.cfg.Records.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	edit(&g)
	if _, err = e.cfg.Records.Commit(t.Context(), DefinitionChange{ID: r.ID, Base: rev, Path: r.Path, Data: definitionBytes(g)}, g.Person, g.Agent); err != nil {
		t.Fatal(err)
	}
	if err = e.RefreshDefinitions(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func reviewAction(t *testing.T, e *Engine, id string, kind ActionKind) Action {
	t.Helper()
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range s.Actions {
		if a.GoalID == id && a.Kind == kind && a.State == "queued" {
			return a
		}
	}
	t.Fatalf("no queued %s for %s: %+v", kind, id, s.Actions)
	return Action{}
}
func claimReviewAction(t *testing.T, e *Engine, id string, kind ActionKind) Action {
	t.Helper()
	a, err := e.Claim(t.Context(), reviewAction(t, e, id, kind).ID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func reviewReconcile(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func assertRetiredReviewAction(t *testing.T, e *Engine, a Action) {
	t.Helper()
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range s.Actions {
		if saved.ID == a.ID {
			if saved.State != "finished" || len(saved.Receipt) == 0 {
				t.Errorf("obsolete observation not retained and retired: %+v", saved)
			}
			return
		}
	}
	t.Fatal("obsolete action history lost")
}

func TestReviewScopeCorrectionReachesManagerAndPreservesOtherGoals(t *testing.T) {
	for _, propose := range []bool{false, true} {
		t.Run(fmt.Sprint(propose), func(t *testing.T) {
			other := oneGoal(readyTask("independent"))
			other.ID = "other"
			e := reviewEngine(t, finishedGoal(), other)
			reviseReviewGoal(t, e, "g", func(g *Goal) { g.Scope.Repositories = []string{"new-repo"}; g.Scope.Includes = "new repository only" })
			if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
				t.Fatal(err)
			}
			reviewReconcile(t, e)
			reviewAction(t, e, "other", Assign)
			a := claimReviewAction(t, e, "g", PlanGoal)
			out := Observation{Status: OutcomeSucceeded}
			if propose {
				task := readyTask("new")
				task.Repository = "new-repo"
				out.Tasks = []Task{task}
			}
			if err := e.Report(t.Context(), a.ID, 1, out); err != nil {
				t.Fatalf("scope planning stopped engine: %v", err)
			}
			reviewReconcile(t, e)
			g := reviewGoal(t, e, "g")
			if propose {
				if g.DefinitionPending || len(g.Tasks) != 1 || g.Tasks[0].ID != "new" || g.Tasks[0].Repository != "new-repo" {
					t.Fatalf("settled removed tasks prevented scoped replanning: %+v", g)
				}
			} else if g.State != GoalBlocked || g.DefinitionPending || len(g.Tasks) != 0 || g.AttentionID == "" {
				t.Fatalf("empty full plan failed to retire tasks and expose the gap: %+v", g)
			}
			restart, err := NewEngine(e.cfg)
			if err != nil {
				t.Fatal(err)
			}
			reviewReconcile(t, restart)
		})
	}
}

func TestReviewStaleCriterionCompletionDoesNotStopRunner(t *testing.T) {
	e := reviewEngine(t, finishedGoal())
	entered, release := make(chan Action, 1), make(chan struct{})
	e.cfg.Driver = &fixtureDriver{run: func(a Action) (Observation, error) {
		entered <- a
		<-release
		return Observation{Status: OutcomeSucceeded, Criterion: &CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: "old-check-output", Passed: true}}, nil
	}}
	if err := e.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	a := <-entered
	reviseReviewGoal(t, e, "g", func(g *Goal) { g.Done = "corrected done condition" })
	close(release)
	e.wg.Wait()
	select {
	case err := <-e.failures:
		t.Errorf("late criterion killed runner: %v", err)
	default:
	}
	assertRetiredReviewAction(t, e, a)
	g := reviewGoal(t, e, "g")
	if !g.DefinitionPending || len(g.Checks) != 0 {
		t.Fatalf("stale criterion counted: %+v", g)
	}
	cfg := e.cfg
	cfg.Epoch++
	cfg.Driver = nil
	restart, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reviewReconcile(t, restart)
	reviewAction(t, restart, "g", PlanGoal)
}

func TestReviewStaleManagerPlanCannotInterpretNewRevision(t *testing.T) {
	for _, withTasks := range []bool{false, true} {
		t.Run(fmt.Sprint(withTasks), func(t *testing.T) {
			e := reviewEngine(t, oneGoal())
			reviewReconcile(t, e)
			a := claimReviewAction(t, e, "g", PlanGoal)
			reviseReviewGoal(t, e, "g", func(g *Goal) { g.Done = "a corrected outcome" })
			out := Observation{Status: OutcomeSucceeded}
			if withTasks {
				out.Tasks = []Task{readyTask("obsolete")}
			}
			if err := e.Report(t.Context(), a.ID, 1, out); err != nil {
				t.Fatal(err)
			}
			g := reviewGoal(t, e, "g")
			if !g.DefinitionPending || len(g.Tasks) != 0 || g.AssessedFailure != "" || g.State == GoalBlocked {
				t.Errorf("old manager response changed corrected goal: %+v", g)
			}
			assertRetiredReviewAction(t, e, a)
			if err := e.Report(t.Context(), a.ID, 1, out); err != nil {
				t.Fatalf("stale redelivery failed: %v", err)
			}
			reviewReconcile(t, e)
			next := reviewAction(t, e, "g", PlanGoal)
			if next.ID == a.ID || next.GoalRevision != g.Revision || next.Revision != g.RecordRevision {
				t.Fatalf("new definition was not sent to manager: %+v", next)
			}
		})
	}
}

func TestReviewOverlappingAreasAreSerialized(t *testing.T) {
	cases := [][2]string{{"src/*", "src/main.go"}, {"internal/core/**", "internal/core/work/**"}, {"src/a*.go", "src/ab?.go"}, {"./src/main.go", "src/main.go"}, {"**/*.go", "pkg/main.go"}, {"src", "src/sub/file.go"}}
	for _, areas := range cases {
		t.Run(areas[0]+"/"+areas[1], func(t *testing.T) {
			a, b := readyTask("a"), readyTask("b")
			a.Area, b.Area = areas[0], areas[1]
			p, err := Derive(State{Goals: []Goal{oneGoal(a, b)}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Actions) != 1 || len(p.Waits) != 1 || p.Waits[0].Reason != "area" {
				t.Fatalf("overlapping areas dispatched concurrently: %+v", p)
			}
			a.Stage = TaskBuilding
			a.NodeID = "n"
			first, second := oneGoal(a), oneGoal(b)
			second.ID = "other"
			p, err = Derive(State{Goals: []Goal{first, second}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Actions) != 0 {
				t.Fatalf("overlap bypassed existing goal lease: %+v", p)
			}
		})
	}
	a, b := readyTask("a"), readyTask("b")
	a.Area = "src/**"
	b.Area = "docs/**"
	p, err := Derive(State{Goals: []Goal{oneGoal(a, b)}, Nodes: []Node{readyNode("n")}}, time.Unix(100, 0))
	if err != nil || len(p.Actions) != 2 {
		t.Fatalf("disjoint areas stopped filling capacity: %+v %v", p, err)
	}
}

func TestReviewPRSignalInvalidatesProofsWithoutQueuedMerge(t *testing.T) {
	a := readyTask("a")
	a.Stage = TaskDelivered
	a.Head = "head"
	a.PR = PRState{Head: "head", Checks: Proof{Revision: "head", Ref: "ci", Passed: true}, Review: Proof{Revision: "head", Ref: "review", Passed: true}, Verified: Proof{Revision: "head", Ref: "manager", Passed: true}, Mergeable: true, ObservedAt: 100}
	e := reviewEngine(t, oneGoal(a))
	if err := e.Signal(t.Context(), Signal{ID: "changed", GoalID: "g", TaskID: "a"}); err != nil {
		t.Fatal(err)
	}
	reviewReconcile(t, e)
	reviewAction(t, e, "g", InspectPR)
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range s.Actions {
		if action.Kind == Merge && action.State != "finished" {
			t.Fatal("merged stale proofs after change signal")
		}
	}
}

func TestReviewRevisionReopensBlockedGoalWithoutAuthorizingEffects(t *testing.T) {
	for _, kind := range []ActionKind{PlanGoal, Assign} {
		t.Run(string(kind), func(t *testing.T) {
			g := oneGoal()
			if kind == Assign {
				g.Tasks = []Task{readyTask("a")}
			}
			e := reviewEngine(t, g)
			if kind == Assign {
				if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
					t.Fatal(err)
				}
			}
			reviewReconcile(t, e)
			a := claimReviewAction(t, e, "g", kind)
			if err := e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeNeedsPerson, EffectsSettled: true}); err != nil {
				t.Fatal(err)
			}
			reviewReconcile(t, e)
			if reviewGoal(t, e, "g").State != GoalBlocked {
				t.Fatal("fixture did not block")
			}
			reviseReviewGoal(t, e, "g", func(g *Goal) { g.Done = "person supplied a corrected definition" })
			cfg := e.cfg
			cfg.Epoch++
			e, _ = NewEngine(cfg)
			reviewReconcile(t, e)
			plan := claimReviewAction(t, e, "g", PlanGoal)
			g = reviewGoal(t, e, "g")
			if g.State == GoalBlocked || g.AttentionID != "" || g.BlockedReason != "" {
				t.Errorf("old attention kept the revision blocked: %+v", g)
			}
			if err := e.Report(t.Context(), plan.ID, cfg.Epoch, Observation{Status: OutcomeSucceeded, Tasks: append(func() []Task {
				if kind == Assign {
					return []Task{readyTask("a")}
				}
				return nil
			}(), readyTask("additional"))}); err != nil {
				t.Fatal(err)
			}
			reviewReconcile(t, e)
			if kind == Assign {
				s, err := e.Snapshot(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				resumed := false
				for _, action := range s.Actions {
					if action.TaskID == "a" && action.Kind == Assign && action.State == "queued" {
						resumed = true
					}
				}
				if !resumed {
					t.Error("denied worker assignment did not return to authorization")
				}
			}
		})
	}
}

type reviewRefusingDriver struct{ *fixtureDriver }

func (d reviewRefusingDriver) Authorize(context.Context, Action) error { return ErrNeedsPerson }

func TestReviewAuthorizationRefusalProvesNoExecution(t *testing.T) {
	e := reviewEngine(t, oneGoal(readyTask("a")))
	if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.cfg.Driver = reviewRefusingDriver{&fixtureDriver{run: func(Action) (Observation, error) { calls++; return Observation{}, nil }}}
	if err := e.step(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.wg.Wait()
	s, err := e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || len(s.Actions) != 1 {
		t.Fatalf("refusal executed work: %d %+v", calls, s.Actions)
	}
	var receipt Observation
	if err = json.Unmarshal(s.Actions[0].Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.EffectsSettled || receipt.Status != OutcomeNeedsPerson {
		t.Fatalf("authorization refusal lost no-execution proof: %+v", receipt)
	}
}

func TestReviewRevisionPreservesUnsettledEffects(t *testing.T) {
	for _, kind := range []ActionKind{Assign, Deploy} {
		t.Run(string(kind), func(t *testing.T) {
			g := oneGoal(readyTask("a"))
			if kind == Deploy {
				g = finishedGoal()
				g.ServiceTarget = "private-service"
				g.NoServiceReason = ""
			}
			e := reviewEngine(t, g)
			if err := e.NodeChanged(t.Context(), readyNode("n")); err != nil {
				t.Fatal(err)
			}
			reviewReconcile(t, e)
			a := claimReviewAction(t, e, "g", kind)
			if err := e.Report(t.Context(), a.ID, 1, Observation{Status: OutcomeNeedsPerson, Handle: "existing-effect", EffectsSettled: false}); err != nil {
				t.Fatal(err)
			}
			reviewReconcile(t, e)
			reviseReviewGoal(t, e, "g", func(g *Goal) { g.Done = "corrected outcome while the prior effect remains uncertain" })
			cfg := e.cfg
			cfg.Epoch++
			e, _ = NewEngine(cfg)
			reviewReconcile(t, e)
			s, err := e.Snapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range s.Actions {
				if action.ID == a.ID && (action.State != "unknown" || action.Deadline > 100) {
					t.Errorf("uncertain effect lost its immediate receipt read: %+v", action)
				}
				if action.Kind == kind && action.State == "queued" {
					t.Error("uncertain effect was redispatched")
				}
			}
			observed, started := 0, 0
			e.cfg.Driver = &fixtureDriver{observe: func(action Action) (Observation, error) {
				if action.ID == a.ID {
					observed++
				}
				return Observation{Status: OutcomeRunning, Handle: "existing-effect"}, nil
			}, run: func(action Action) (Observation, error) {
				if action.Kind == kind {
					started++
				}
				return Observation{Status: OutcomeRunning, Handle: "manager"}, nil
			}}
			if err = e.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			e.wg.Wait()
			if observed != 1 || started != 0 {
				t.Fatalf("restart repeated or lost effect: observed=%d dispatched=%d", observed, started)
			}
		})
	}
}
