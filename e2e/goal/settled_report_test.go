// SPDX-License-Identifier: Apache-2.0
package goal_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

type settledWorld struct {
	capacityWorld
	release chan struct{}
}

func (w *settledWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	if a.Kind != work.Assign || a.TaskID != "independent" {
		return w.capacityWorld.Execute(ctx, a)
	}
	select {
	case <-w.release:
	case <-ctx.Done():
		return work.Observation{}, ctx.Err()
	}
	w.mu.Lock()
	w.calls = append(w.calls, a)
	w.mu.Unlock()
	head := "head-" + a.TaskID
	cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' healthy > "$1"`, "worker", filepath.Join(w.root, head))
	if output, err := cmd.CombinedOutput(); err != nil {
		return work.Observation{}, fmt.Errorf("worker: %w: %s", err, output)
	}
	return work.Observation{Status: work.OutcomeSucceeded, Head: head, EffectsSettled: true}, nil
}

func TestCoreKeepsSettledTaskAfterLateWorkerReport(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-settled-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"settled-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := &settledWorld{capacityWorld: capacityWorld{&repairWorld{root: t.TempDir()}}, release: make(chan struct{})}
	terminal := make(chan work.Goal, 1)
	failures := make(chan error, 1)
	var worker work.Action
	var landed work.Task
	injected, checked := false, false
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) { w.engine = e; return w, nil },
		MainHost: func(context.Context) (work.Node, error) {
			return work.Node{ID: "settled-main", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 2, CPULimit: 2, AvailableMemoryMB: 1024, MemoryLimitMB: 1024, AvailableDiskMB: 1024, DiskLimitMB: 1024, Workers: map[string]bool{"stub": true}}, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, agent string) error {
			agent, err := fixtureWorkOwner(ctx, root)
			if err != nil {
				return err
			}
			a, b := repairTask("repair"), repairTask("independent")
			a.Area, b.Area = a.ID, b.ID
			return e.Admit(ctx, work.Goal{ID: "runtime-repair", Person: person, Agent: agent, Summary: "produce two library artifacts", Done: "both artifacts are healthy", Scope: work.GoalScope{Repositories: []string{"fixture"}, Includes: "library artifacts"}, NoServiceReason: "library", Tasks: []work.Task{a, b}, Criteria: []work.Criterion{{ID: "artifact", Statement: "artifact is healthy", Check: work.CriterionCheck{Kind: work.ReadEvidence, Method: "artifact.read", Target: "fixture"}}}})
		},
		Changed: func(ctx context.Context, s work.State) error {
			if len(s.Goals) == 0 {
				return nil
			}
			g := s.Goals[0]
			if !injected && g.Tasks[0].Stage == work.TaskDone {
				landed = g.Tasks[0]
				for _, a := range s.Actions {
					if a.TaskID == landed.ID && a.Kind == work.Assign && a.State == "finished" {
						worker = a
					}
				}
				if worker.ID == "" || g.State == work.GoalDone {
					return fmt.Errorf("missing settled assignment with independent work still open")
				}
				if err := w.engine.Signal(ctx, work.Signal{ID: "late-task-change", GoalID: g.ID, TaskID: landed.ID}); err != nil {
					return err
				}
				late := work.Observation{Status: work.OutcomeSucceeded, Head: landed.Head, Handle: "delayed-receipt", EffectsSettled: true}
				if err := w.engine.Report(ctx, worker.ID, worker.Epoch, late); err != nil {
					return err
				}
				injected = true
				return nil
			}
			if injected {
				for _, a := range s.Actions {
					if a.ObserveActionID == worker.ID || a.ID == worker.ID && (a.State != "finished" || a.Superseded) {
						err := fmt.Errorf("Core reopened settled worker after late report: state=%s superseded=%t replacement=%s", a.State, a.Superseded, a.ObserveActionID)
						select {
						case failures <- err:
						default:
						}
						return err
					}
				}
				got := g.Tasks[0]
				if got.Stage != work.TaskDone || got.Head != landed.Head || got.Merge != landed.Merge || got.QA != landed.QA {
					return fmt.Errorf("late report changed landed task: %+v", got)
				}
				if !checked {
					checked = true
					close(w.release)
				}
			}
			if g.State == work.GoalDone || g.State == work.GoalBlocked {
				select {
				case terminal <- g:
				default:
				}
			}
			return nil
		},
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !t.Failed() {
			t.Errorf("Core shutdown: %v", err)
		}
	})
	var g work.Goal
	select {
	case g = <-terminal:
	case err := <-failures:
		t.Fatal(err)
	case err := <-done:
		done <- nil
		t.Fatalf("Core stopped before completion: %v", err)
	case <-ctx.Done():
		t.Fatal("Core did not complete independent work after the late report")
	}
	if g.State != work.GoalDone || !g.Verification.Passed || len(g.Checks) != 6 {
		t.Fatalf("goal lacks completion evidence: %+v", g)
	}
	for _, task := range g.Tasks {
		data, err := os.ReadFile(filepath.Join(w.root, task.ResultRevision))
		if task.Stage != work.TaskDone || !task.QA.Passed || err != nil || string(data) != "healthy" {
			t.Fatalf("missing verified worker result: %+v / %q / %v", task, data, err)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	counts := map[work.ActionKind]int{}
	for _, a := range w.calls {
		counts[a.Kind]++
	}
	if counts[work.Assign] != 2 || counts[work.Merge] != 2 || counts[work.RunQA] != 2 {
		t.Fatalf("missing or repeated work: %v", counts)
	}
}
