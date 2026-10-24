// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

// A research goal whose first run lands partial gets one follow-up scoped to
// the gap. When that follow-up cannot answer, the question is marked open and
// the rubric counts it. An unresolved citation keeps the goal open until it
// resolves. Stub runs stand in for workers; nothing here waits on a clock.
func TestM2ResearchPartialFollowUp(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-research-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"main-work-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	world := &researchWorld{held: make(chan struct{}, 1), closed: make(chan work.Goal, 1)}
	var engine *work.Engine
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) {
			engine = e
			world.engine = e
			return world, nil
		},
		MainHost: func(context.Context) (work.Node, error) {
			return work.Node{ID: "main-work-fixture", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 4, CPULimit: 4, AvailableMemoryMB: 8000, MemoryLimitMB: 8000, AvailableDiskMB: 8000, DiskLimitMB: 8000, Workers: map[string]bool{"background": true}}, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			agent, err := researchOwner(ctx, root)
			if err != nil {
				return err
			}
			task := work.Task{ID: "notes", Kind: work.BackgroundStep, Done: "Each question is answered or marked open", Repository: "fixture-repo", Worker: "background", Resources: work.Resources{CPU: 1, MemoryMB: 100, DiskMB: 100}, Stage: work.TaskReady, Background: &work.BackgroundBrief{Goal: "Answer the questions", Scope: "named sources", NotTouch: "files", Done: "Each question is answered or marked open", Result: "document", Checks: []work.BackgroundCheck{{Name: "sources", Kind: "citations_resolve"}}}}
			return e.Admit(ctx, work.Goal{
				ID: "research-goal", Person: person, Agent: agent, Summary: "the notes answer the questions", Done: "the document is in the materials",
				NoServiceReason: "research notes", Scope: work.GoalScope{Repositories: []string{"fixture-repo"}, Includes: "the named sources"},
				Tasks: []work.Task{task},
				Criteria: []work.Criterion{
					{ID: "questions", Statement: "Each question is answered or marked open", Check: work.CriterionCheck{Kind: work.JudgmentEvidence, Method: "research.rubric", Target: "document", Rubric: "answered or marked open with a reason", TaskID: "notes"}},
					{ID: "citations", Statement: "Every cited passage resolves", Check: work.CriterionCheck{Kind: work.CitationEvidence, Method: "research.citations", Target: "document", TaskID: "notes"}},
				},
			})
		},
		Changed: func(_ context.Context, s work.State) error {
			for _, g := range s.Goals {
				if g.ID != "research-goal" {
					continue
				}
				if g.State == work.GoalDone {
					select {
					case world.closed <- g:
					default:
					}
				}
				for _, w := range g.Waits {
					if w.Reason == "citation" {
						select {
						case world.held <- struct{}{}:
						default:
						}
					}
				}
			}
			return nil
		},
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	select {
	case <-world.held:
	case err = <-done:
		t.Fatalf("Core stopped before the citation hold: %v", err)
	case <-time.After(45 * time.Second):
		t.Fatal("citation criterion did not stay open")
	}
	world.mu.Lock()
	assigns := world.assigns
	world.mu.Unlock()
	if assigns != 2 {
		t.Fatalf("follow-up runs=%d", assigns)
	}
	snap, err := engine.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var goal work.Goal
	for _, g := range snap.Goals {
		if g.ID == "research-goal" {
			goal = g
		}
	}
	if goal.State == work.GoalDone || len(goal.Tasks) != 1 || goal.Tasks[0].Stage != work.TaskDone || len(goal.Tasks[0].ResearchOpen) != 1 || goal.Tasks[0].ResearchOpen[0].Reason == "" || goal.Tasks[0].ResearchFollowUp == nil || goal.Tasks[0].ResearchFollowUp.Scope != "the fee" {
		t.Fatalf("partial result did not stay open with one follow-up: %+v", goal)
	}
	world.mu.Lock()
	world.resolved = true
	world.mu.Unlock()
	if err = engine.TargetChanged(t.Context(), "citation-fixed", "document"); err != nil {
		t.Fatal(err)
	}
	select {
	case goal = <-world.closed:
	case err = <-done:
		t.Fatalf("Core stopped before the citation resolved: %v", err)
	case <-time.After(45 * time.Second):
		t.Fatal("resolved citation did not close the goal")
	}
	var citationPassed bool
	for _, check := range goal.Checks {
		if check.Kind == work.CitationEvidence && check.Passed {
			citationPassed = true
		}
		if check.Kind == work.JudgmentEvidence && !check.Passed {
			t.Fatalf("rubric did not count the open question: %+v", check)
		}
	}
	if goal.State != work.GoalDone || !citationPassed {
		t.Fatalf("goal did not close on the resolved citation: %+v", goal)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

type researchWorld struct {
	mu       sync.Mutex
	engine   *work.Engine
	assigns  int
	resolved bool
	held     chan struct{}
	closed   chan work.Goal
}

func (w *researchWorld) Authorize(context.Context, work.Action) error { return nil }
func (w *researchWorld) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, fmt.Errorf("research fixture observed an uncertain effect")
}
func (w *researchWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	switch a.Kind {
	case work.Resume, work.InspectPR, work.Merge, work.RunQA, work.RepairConflict, work.RepairQA, work.Deploy, work.Observe, work.Decide, work.PlanGoal, work.ContinueWorker:
		return work.Observation{}, fmt.Errorf("unexpected action %s", a.Kind)
	case work.Assign:
		s, err := w.engine.Snapshot(ctx)
		if err != nil {
			return work.Observation{}, err
		}
		var task work.Task
		for _, g := range s.Goals {
			for _, item := range g.Tasks {
				if g.ID == a.GoalID && item.ID == a.TaskID {
					task = item
				}
			}
		}
		w.mu.Lock()
		w.assigns++
		n := w.assigns
		w.mu.Unlock()
		if task.ResearchFollowUp == nil {
			if n != 1 {
				return work.Observation{}, fmt.Errorf("first run repeated")
			}
			return work.Observation{Status: work.OutcomePartial, EffectsSettled: true, Handle: "run-1", Proof: work.Proof{Revision: "mat:1", Ref: "mat"}, Partial: &work.ResearchPartial{RunID: "run-1", Missing: []string{"the fee"}, MaterialID: "mat", MaterialRevision: 1}}, nil
		}
		if task.ResearchFollowUp.Scope != "the fee" || task.ResearchFollowUp.FollowOf != "run-1" || n != 2 {
			return work.Observation{}, fmt.Errorf("follow-up scope=%q run=%d", task.ResearchFollowUp.Scope, n)
		}
		return work.Observation{Status: work.OutcomePartial, EffectsSettled: true, Handle: "run-2", Proof: work.Proof{Revision: "mat:2", Ref: "mat"}, Partial: &work.ResearchPartial{RunID: "run-2", Missing: []string{"the fee"}, Open: []work.ResearchOpen{{Question: "the fee", Reason: "The sources do not state it."}}, MaterialID: "mat", MaterialRevision: 2}}, nil
	case work.Verify:
		return work.Observation{Status: work.OutcomeSucceeded, Proof: work.Proof{Revision: a.Revision, Ref: "steps", Passed: true}}, nil
	case work.CheckCriterion:
		if a.Check == nil {
			return work.Observation{}, fmt.Errorf("missing check")
		}
		w.mu.Lock()
		resolved := w.resolved
		w.mu.Unlock()
		passed := true
		collected := &work.CollectedEvidence{Reads: []work.EvidenceRead{{Target: a.Check.Target, Revision: a.Revision, Content: "passage"}}, Output: "read"}
		switch a.Check.Method {
		case "research.citations":
			passed = resolved
			if passed {
				collected.Output = "citation resolves"
			} else {
				collected.Output = "unresolved citation"
			}
		case "research.rubric":
			collected.Rubric = a.Check.Rubric
			collected.Judgment = "the question is marked open with its reason"
		case "runtime.probe":
			collected = &work.CollectedEvidence{Output: "runtime read"}
		default:
			return work.Observation{}, fmt.Errorf("unknown check %s", a.Check.Method)
		}
		return work.Observation{Status: work.OutcomeSucceeded, Proof: work.Proof{Revision: a.Revision, Ref: "read:" + a.CriterionID, Passed: passed}, Criterion: &work.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: "read:" + a.CriterionID, Passed: passed, Collected: collected}}, nil
	default:
		return work.Observation{}, fmt.Errorf("unexpected action %s", a.Kind)
	}
}

func researchOwner(ctx context.Context, root string) (string, error) {
	db, err := canon.OpenSQLiteReader(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	var id string
	err = db.QueryRowContext(ctx, "SELECT agent_id FROM agent_starters WHERE preset='dev'").Scan(&id)
	return id, err
}
