// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

func TestGoalLivenessStubWorker(t *testing.T) {
	if os.Getenv("LINA_GOAL_LIVENESS_STUB") != "1" {
		return
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// TestGoalLivenessThroughRunningCore is the headless liveness scenario.
// One goal has a ready task, so derivation starts a stub worker and does not
// spend a manager turn. One goal has nothing to derive, so Core gives the
// manager one turn naming the open criteria and then blocks with a plain row.
// A restarted Core keeps that row and does not run either path again.
func TestGoalLivenessThroughRunningCore(t *testing.T) {
	// testing.T.TempDir is 0777 before umask, so it is group-writable and
	// Core refuses it. A private directory is what installation creates.
	root, err := os.MkdirTemp("", "lina-goal-liveness-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"main-work-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	const statement = "the itinerary lists the open questions"
	world := &livenessWorld{heads: map[string]string{}, ready: make(chan struct{}), wake: make(chan struct{}, 1)}
	var engine *work.Engine
	admissions := 0
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) {
			engine = e
			return world, nil
		},
		MainHost: func(context.Context) (work.Node, error) {
			return work.Node{ID: "main-work-fixture", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 4, CPULimit: 4, AvailableMemoryMB: 8000, MemoryLimitMB: 8000, AvailableDiskMB: 8000, DiskLimitMB: 8000, Workers: map[string]bool{"stub": true}}, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			agent, err := goalLivenessOwner(ctx, root)
			if err != nil {
				return err
			}
			saved, err := e.Snapshot(ctx)
			if err != nil {
				return err
			}
			if len(saved.Goals) > 0 {
				return nil
			}
			admissions++
			goal := work.Goal{Person: person, Agent: agent, Summary: "prepare the trip notes", Done: statement, NoServiceReason: "notes", Scope: work.GoalScope{Repositories: []string{"fixture-repo"}, Includes: "the trip notes"}, Criteria: []work.Criterion{{ID: "itinerary", Statement: statement, Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "acceptance", Target: "fixture-repo"}}}}
			idle := goal
			idle.ID = "liveness-idle"
			if err = e.Admit(ctx, idle); err != nil {
				return err
			}
			ready := goal
			ready.ID = "liveness-work"
			ready.Tasks = []work.Task{{ID: "notes", Done: "notes are written", Repository: "fixture-repo", Base: "dev", Area: "notes", Worker: "stub", Resources: work.Resources{CPU: 1, MemoryMB: 100, DiskMB: 100}, Stage: work.TaskReady}}
			return e.Admit(ctx, ready)
		},
		Changed: func(_ context.Context, s work.State) error { world.note(s); return nil },
	}}
	run := func() (context.CancelFunc, <-chan error) {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- boot.Run(ctx, cfg) }()
		return cancel, done
	}
	cancel, done := run()
	t.Cleanup(cancel)
	select {
	case <-world.ready:
	case err := <-done:
		t.Fatalf("Core stopped: %v", err)
	case <-time.After(45 * time.Second):
		cancel()
		<-done
		t.Fatal("liveness scenario did not settle")
	}
	world.mu.Lock()
	calls := len(world.calls)
	attention := world.idle.AttentionID
	published := world.published
	if world.idle.ManagerTurns != 1 || attention == "" || !strings.Contains(world.idle.BlockedReason, "could not find a way") || !strings.Contains(world.idle.BlockedReason, statement) {
		world.mu.Unlock()
		t.Fatalf("blocked row: %+v", world.idle)
	}
	world.mu.Unlock()
	if engine == nil || admissions != 1 {
		t.Fatalf("core admission: engine %v admissions %d", engine != nil, admissions)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancel, done = run()
	t.Cleanup(cancel)
	deadline := time.After(30 * time.Second)
	for {
		world.mu.Lock()
		seen := world.published > published
		world.mu.Unlock()
		if seen {
			break
		}
		select {
		case <-world.wake:
		case err := <-done:
			t.Fatalf("restart stopped: %v", err)
		case <-deadline:
			cancel()
			<-done
			t.Fatal("restart did not publish the blocked goal")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	world.mu.Lock()
	defer world.mu.Unlock()
	if len(world.calls) != calls || admissions != 1 || world.idle.AttentionID != attention {
		t.Fatalf("restart repeated work or replaced the row: calls %d -> %d, admissions %d, attention %s", calls, len(world.calls), admissions, world.idle.AttentionID)
	}
}

type livenessWorld struct {
	mu        sync.Mutex
	heads     map[string]string
	calls     []work.Action
	idle      work.Goal
	delivered bool
	signaled  bool
	published int
	ready     chan struct{}
	wake      chan struct{}
}

func (w *livenessWorld) Authorize(context.Context, work.Action) error { return nil }
func (w *livenessWorld) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, fmt.Errorf("liveness effect was repeated")
}
func (w *livenessWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	w.mu.Lock()
	w.calls = append(w.calls, a)
	w.mu.Unlock()
	out := work.Observation{Status: work.OutcomeSucceeded}
	switch a.Kind {
	case work.PlanGoal:
		if a.GoalID != "liveness-idle" {
			return out, fmt.Errorf("manager turn for %s", a.GoalID)
		}
		named := false
		for _, criterion := range a.OpenCriteria {
			named = named || criterion == "the itinerary lists the open questions"
		}
		if !named {
			return out, fmt.Errorf("manager turn omitted the open criteria: %v", a.OpenCriteria)
		}
	case work.Assign:
		if a.GoalID != "liveness-work" {
			return out, fmt.Errorf("assignment for %s", a.GoalID)
		}
		if err := w.stub(ctx); err != nil {
			return out, err
		}
		head := "head-" + a.TaskID
		w.mu.Lock()
		w.heads[a.TaskID] = head
		w.mu.Unlock()
		out.Head = head
		out.EffectsSettled = true
	case work.InspectPR:
		w.mu.Lock()
		head := w.heads[a.TaskID]
		w.mu.Unlock()
		if head == "" || a.Revision != head {
			return out, fmt.Errorf("inspection without the worker head")
		}
		out.PR = work.PRState{Head: head, Mergeable: false}
	case work.Resume, work.Merge, work.RunQA, work.RepairConflict, work.RepairQA, work.Deploy, work.Verify, work.Observe, work.Decide, work.CheckCriterion, work.ContinueWorker, work.ReadSource:
		return out, fmt.Errorf("unexpected action %s", a.Kind)
	default:
		return out, fmt.Errorf("unexpected action %s", a.Kind)
	}
	return out, nil
}
func (w *livenessWorld) stub(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGoalLivenessStubWorker$")
	cmd.Env = []string{"LINA_GOAL_LIVENESS_STUB=1"}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "ready\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("stub startup: %q %v", ready, err)
	}
	if err = input.Close(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return cmd.Wait()
}
func (w *livenessWorld) note(s work.State) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.published++
	select {
	case w.wake <- struct{}{}:
	default:
	}
	for _, g := range s.Goals {
		if g.ID == "liveness-idle" && g.State == work.GoalBlocked && g.ManagerTurns == 1 && strings.Contains(g.BlockedReason, "the itinerary lists the open questions") {
			w.idle = g
		}
		if g.ID == "liveness-work" && g.ManagerTurns == 0 {
			for _, task := range g.Tasks {
				if task.ID == "notes" && task.Stage == work.TaskDelivered {
					w.delivered = true
				}
			}
		}
	}
	planned, assigned, inspected, other := 0, 0, 0, 0
	for _, a := range w.calls {
		switch a.Kind {
		case work.PlanGoal:
			planned++
		case work.Assign:
			assigned++
		case work.InspectPR:
			inspected++
		case work.Resume, work.Merge, work.RunQA, work.RepairConflict, work.RepairQA, work.Deploy, work.Verify, work.Observe, work.Decide, work.CheckCriterion, work.ContinueWorker, work.ReadSource:
			other++
		}
	}
	if !w.signaled && other == 0 && w.idle.AttentionID != "" && w.delivered && planned == 1 && assigned == 1 && inspected == 1 {
		w.signaled = true
		close(w.ready)
	}
}

func goalLivenessOwner(ctx context.Context, root string) (string, error) {
	db, err := canon.OpenSQLiteReader(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	var id string
	err = db.QueryRowContext(ctx, "SELECT agent_id FROM agent_starters WHERE preset='dev'").Scan(&id)
	return id, err
}
