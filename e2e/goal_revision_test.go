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

type revisionWorld struct {
	*codeWorld
	authorizationChecks, approvalSignals int
	granted                              bool
	unstartedExecuted, siblingRevised    chan struct{}
	receipts                             map[string]work.Observation
	observations                         int
}

func (w *revisionWorld) Authorize(ctx context.Context, a work.Action) error {
	if err := w.codeWorld.Authorize(ctx, a); err != nil {
		return err
	}
	if a.Kind == work.Deploy {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.authorizationChecks++
		if !w.granted {
			return work.ErrNeedsPerson
		}
	}
	return nil
}

func (w *revisionWorld) Observe(_ context.Context, a work.Action) (work.Observation, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Reopen the fixture's completed operation receipt, just as the installed
	// driver reads a worker result after a goal revision. Never repeat the effect.
	o, ok := w.receipts[a.ID]
	if !ok {
		return work.Observation{}, fmt.Errorf("missing operation receipt: %s", a.ID)
	}
	w.observations++
	return o, nil
}

func (w *revisionWorld) Execute(ctx context.Context, a work.Action) (o work.Observation, err error) {
	defer func() {
		if err == nil {
			w.mu.Lock()
			w.receipts[a.ID] = o
			w.mu.Unlock()
		}
	}()
	if a.Kind == work.PlanGoal {
		if a.TaskID == "right" {
			select {
			case <-w.unstartedExecuted:
			case <-ctx.Done():
				return work.Observation{}, ctx.Err()
			}
		}
		w.mu.Lock()
		w.calls = append(w.calls, a)
		w.mu.Unlock()
		repair := revisionTask("repair-" + a.TaskID)
		repair.Repairs, repair.DependsOn = a.TaskID, []string{a.TaskID}
		return work.Observation{Status: work.OutcomeSucceeded, Tasks: []work.Task{repair}}, nil
	}
	o, err = w.codeWorld.Execute(ctx, a)
	if err == nil && a.Kind == work.Assign && a.TaskID == "unstarted" {
		// Hold this completed worker receipt across the sibling's revision.
		// Core must observe its artifact under the new inputs, without rerunning it.
		close(w.unstartedExecuted)
		select {
		case <-w.siblingRevised:
		case <-ctx.Done():
			return work.Observation{}, ctx.Err()
		}
	}
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
	w := &revisionWorld{codeWorld: &codeWorld{heads: map[string]string{}, merges: map[string]string{}, qa: map[string]bool{}, root: root}, unstartedExecuted: make(chan struct{}), siblingRevised: make(chan struct{}), receipts: map[string]work.Observation{}}
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
			receiptedRight := w.heads["right"]
			w.heads["right"] = "changed-right-head"
			if err := e.Signal(ctx, work.Signal{ID: "right-head-changed", GoalID: g.ID, TaskID: "right"}); err != nil {
				return err
			}
			for _, id := range []string{"left", "right"} {
				if id == "right" {
					if err := w.finish(ctx, e, id, work.InspectPR); err != nil {
						return err
					}
					s, err := e.Snapshot(ctx)
					if err != nil {
						return err
					}
					right := s.Goals[0].Tasks[1]
					if right.Head != receiptedRight || right.PR.Head != "changed-right-head" {
						return fmt.Errorf("inspection changed receipted head: %+v", right)
					}
					if err := e.Reconcile(ctx); err != nil {
						return err
					}
					s, err = e.Snapshot(ctx)
					if err != nil {
						return err
					}
					for _, a := range s.Actions {
						if a.TaskID == "right" && a.Kind == work.Merge && a.State == "queued" {
							return fmt.Errorf("unreceipted head became mergeable")
						}
					}
					// The PR is restored to the worker's receipted artifact; fresh
					// proofs of that exact head are required before continuing.
					w.heads["right"] = receiptedRight
					if err := e.Signal(ctx, work.Signal{ID: "right-head-restored", GoalID: g.ID, TaskID: "right"}); err != nil {
						return err
					}
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
		Changed: func(ctx context.Context, s work.State) error {
			for _, g := range s.Goals {
				if len(g.Tasks) == 5 {
					select {
					case <-w.siblingRevised:
					default:
						close(w.siblingRevised)
					}
				}
			}
			for _, a := range s.Actions {
				if a.Kind == work.Deploy && a.State == "awaiting_authorization" {
					w.mu.Lock()
					w.approvalSignals++
					count := w.approvalSignals
					// The first change hint still has no grant. The second models
					// the trusted authority recording approval for this action.
					w.granted = count == 2
					w.mu.Unlock()
					if err := engine.Signal(ctx, work.Signal{ID: fmt.Sprintf("deployment-approval-%d", count), ActionID: a.ID}); err != nil {
						return err
					}
				}
			}
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
		inspect, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		state, err := engine.Snapshot(inspect)
		raw, _ := json.Marshal(state)
		t.Fatalf("revision stranded a sibling action: snapshot=%s error=%v", raw, err)
	}
	if len(g.Tasks) != 5 || len(g.Checks) != 13 || !g.Deployment.Passed || !g.Verification.Passed {
		t.Fatalf("goal closed without repair/lifecycle evidence: %+v", g)
	}
	for _, task := range g.Tasks {
		if task.Stage != work.TaskDone || !task.QA.Passed {
			t.Fatalf("unfinished task: %+v", task)
		}
	}
	if g.Tasks[1].Head != "head-right-1" || g.Tasks[2].Attempt != 1 {
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
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.observations == 0 {
		t.Fatal("the revised goal did not observe the completed sibling effect")
	}
	deployments := 0
	for _, a := range w.calls {
		if a.Kind == work.Deploy {
			deployments++
		}
	}
	if w.authorizationChecks != 3 || w.approvalSignals != 2 || deployments != 1 {
		t.Fatalf("approval did not recheck authority and deploy once: checks=%d signals=%d deploys=%d", w.authorizationChecks, w.approvalSignals, deployments)
	}
}
