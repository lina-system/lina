// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

func revisionTask(id string) work.Task {
	return work.Task{ID: id, Done: "merged and QA passes", Repository: "fixture-repo", Base: "dev", Area: id, Worker: "stub", Resources: work.Resources{CPU: 2, MemoryMB: 1000, DiskMB: 1000}, Stage: work.TaskReady}
}

type revisionWorld struct{ *codeWorld }

func (w *revisionWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	if a.Kind == work.PlanGoal {
		w.mu.Lock()
		w.calls = append(w.calls, a)
		w.mu.Unlock()
		repair := revisionTask("repair-" + a.TaskID)
		repair.Repairs, repair.DependsOn = a.TaskID, []string{a.TaskID}
		return work.Observation{Status: work.OutcomeSucceeded, Tasks: []work.Task{repair}}, nil
	}
	o, err := w.codeWorld.Execute(ctx, a)
	if err == nil && a.Kind == work.RunQA {
		w.mu.Lock()
		defer w.mu.Unlock()
		switch a.TaskID {
		case "left", "right":
			w.qa[a.Revision] = false
			o.Status, o.Proof.Passed = work.OutcomeFailed, false
			o.Proof.Ref = w.output("failed-"+a.Revision, o.Proof)
		case "repair-left", "repair-right":
			// QA of the repaired tree supersedes the original failure. The
			// failed check file remains available in the goal's evidence.
			original := a.TaskID[len("repair-"):]
			w.qa[w.merges[original]] = true
		}
	}
	return o, err
}

// Establish the interleavings before the runner starts, using only the durable
// engine ingress. There are no sleeps or competing goroutines to win a race.
func (w *revisionWorld) finish(ctx context.Context, e *work.Engine, task string, kind work.ActionKind) error {
	if err := e.Reconcile(ctx); err != nil {
		return err
	}
	s, err := e.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, a := range s.Actions {
		if a.TaskID != task || a.Kind != kind || a.State != "queued" {
			continue
		}
		a, err = e.Claim(ctx, a.ID)
		if err != nil {
			return err
		}
		o, err := w.Execute(ctx, a)
		if err != nil {
			return err
		}
		return e.Report(ctx, a.ID, a.Epoch, o)
	}
	return fmt.Errorf("no runnable %s action for %s", kind, task)
}

func TestM2RunToDoneAfterQueuedSiblingRevision(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-revision-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err := os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"revision-host"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := &revisionWorld{&codeWorld{heads: map[string]string{}, merges: map[string]string{}, qa: map[string]bool{}, root: root}}
	service := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		_ = json.NewEncoder(rw).Encode(map[string]any{"Revision": w.deployed, "Healthy": w.deployed != ""})
	}))
	defer service.Close()
	w.url = service.URL
	closed := make(chan work.Goal, 1)
	var engine *work.Engine
	host := func(context.Context) (work.Node, error) {
		return work.Node{ID: "revision-host", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 4, CPULimit: 4, AvailableMemoryMB: 8000, MemoryLimitMB: 8000, AvailableDiskMB: 8000, DiskLimitMB: 8000, Workers: map[string]bool{"stub": true}}, nil
	}
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver:   func(e *work.Engine) (work.Driver, error) { engine = e; return w, nil },
		MainHost: host,
		Ready: func(ctx context.Context, e *work.Engine, person, agent string) error {
			node, _ := host(ctx)
			if err := e.NodeChanged(ctx, node); err != nil {
				return err
			}
			g := work.Goal{ID: "code-goal", Person: person, Agent: agent, Summary: "repair both changes", Done: "the repaired service answers", Scope: work.GoalScope{Repositories: []string{"fixture-repo"}, Includes: "three changes and their repairs"}, ServiceTarget: "private-service", Tasks: []work.Task{revisionTask("left"), revisionTask("right"), revisionTask("unstarted")}, Criteria: []work.Criterion{{ID: "answers", Statement: "service answers", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "service.answers", Target: "private-service"}}}}
			if err := e.Admit(ctx, g); err != nil {
				return err
			}
			for _, id := range []string{"left", "right"} {
				for _, kind := range []work.ActionKind{work.Assign, work.InspectPR} {
					if err := w.finish(ctx, e, id, kind); err != nil {
						return err
					}
				}
			}
			// Right has passing cached proofs but left owns the merge slot.
			w.heads["right"] = "changed-right-head"
			if err := e.Signal(ctx, work.Signal{ID: "right-head-changed", GoalID: g.ID, TaskID: "right"}); err != nil {
				return err
			}
			for _, id := range []string{"left", "right"} {
				if id == "right" {
					if err := w.finish(ctx, e, id, work.InspectPR); err != nil {
						return err
					}
				}
				for _, kind := range []work.ActionKind{work.Merge, work.RunQA} {
					if err := w.finish(ctx, e, id, kind); err != nil {
						return err
					}
				}
			}
			// Both repair managers and an unrelated worker are queued. The first
			// manager revises the goal; Core must dispatch the others afterwards.
			return w.finish(ctx, e, "left", work.PlanGoal)
		},
		Changed: func(_ context.Context, s work.State) error {
			for _, g := range s.Goals {
				if g.State == work.GoalDone {
					select {
					case closed <- g:
					default:
					}
				}
			}
			return nil
		},
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			if err := <-done; err != nil {
				t.Errorf("Core shutdown: %v", err)
			}
		}
	}()
	var g work.Goal
	select {
	case g = <-closed:
	case err := <-done:
		joined = true
		t.Fatalf("Core stopped before completion: %v", err)
	case <-ctx.Done():
		t.Fatal("revision stranded a sibling action")
	}
	if len(g.Tasks) != 5 || len(g.Checks) != 13 || !g.Deployment.Passed || !g.Verification.Passed {
		t.Fatalf("goal closed without repair/lifecycle evidence: %+v", g)
	}
	for _, task := range g.Tasks {
		if task.Stage != work.TaskDone || !task.QA.Passed {
			t.Fatalf("unfinished task: %+v", task)
		}
	}
	if g.Tasks[1].Head != "changed-right-head" || g.Tasks[2].Attempt != 1 {
		t.Fatal("PR head was not refreshed or cancellation consumed a worker attempt")
	}
	failed := 0
	for _, proof := range g.Evidence {
		if !proof.Passed {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("repair erased QA failures: %d", failed)
	}
	if _, err := engine.Summary(t.Context(), g.ID); err != nil {
		t.Fatal(err)
	}
}
