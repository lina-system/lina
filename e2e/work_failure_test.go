// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

type replacementWorkDriver struct {
	engine      *work.Engine
	output      string
	mu          sync.Mutex
	assignments map[string]int
	observed    []string
}

func (d *replacementWorkDriver) Authorize(context.Context, work.Action) error { return nil }
func (d *replacementWorkDriver) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	if a.Kind == work.InspectPR {
		// A malformed integration receipt makes Engine.Run fail. This exercises
		// boot's work-failure shutdown after the independent worker delivered.
		return work.Observation{Status: work.OutcomeRunning}, nil
	}
	if a.Kind != work.Assign {
		return work.Observation{}, fmt.Errorf("unexpected action %s", a.Kind)
	}
	d.mu.Lock()
	d.assignments[a.TaskID]++
	d.mu.Unlock()
	if a.TaskID == "a" {
		if err := d.engine.Signal(ctx, work.Signal{ID: "worker-progress", ActionID: a.ID}); err != nil {
			return work.Observation{}, err
		}
		return work.Observation{Status: work.OutcomeRunning, Handle: "worker-a"}, nil
	}
	// Only this isolated worker writes the result; the test reads it back after
	// Core dispatches, accepts its receipt and begins inspection.
	cmd := exec.CommandContext(ctx, "sh", "-c", `printf 'independent result\n' > "$1"`, "worker", d.output)
	if out, err := cmd.CombinedOutput(); err != nil {
		return work.Observation{}, fmt.Errorf("worker: %w: %s", err, out)
	}
	return work.Observation{Status: work.OutcomeSucceeded, Head: "head-b", EffectsSettled: true}, nil
}
func (d *replacementWorkDriver) Observe(_ context.Context, a work.Action) (work.Observation, error) {
	d.mu.Lock()
	d.observed = append(d.observed, a.ID)
	d.mu.Unlock()
	return work.Observation{Status: work.OutcomeRunning, Handle: "worker-a"}, nil
}

func TestM2WorkReplacementAndFailureCleanup(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%t", replacement), func(t *testing.T) {
			root, err := os.MkdirTemp("", "lina-work-cleanup-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(root) })
			if err := os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"main-work-fixture"}`), 0600); err != nil {
				t.Fatal(err)
			}
			driver := &replacementWorkDriver{output: filepath.Join(root, "result.txt"), assignments: map[string]int{}}
			var original string
			var delivered bool
			node := work.Node{ID: "main-work-fixture", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 2, CPULimit: 2, AvailableMemoryMB: 8000, MemoryLimitMB: 8000, AvailableDiskMB: 8000, DiskLimitMB: 8000, Workers: map[string]bool{"stub": true}}
			cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/core.sock"), Work: &boot.WorkConfig{
				Driver: func(e *work.Engine) (work.Driver, error) { driver.engine = e; return driver, nil },
				Ready: func(ctx context.Context, e *work.Engine, person, agent string) error {
					agent, err := fixtureWorkOwner(ctx, root)
					if err != nil {
						return err
					}
					ids := []string{"b"}
					if replacement {
						ids = []string{"a", "b"}
					}
					var tasks []work.Task
					for _, id := range ids {
						tasks = append(tasks, work.Task{ID: id, Done: "result is checked", Repository: "fixture", Base: "dev", Area: id, Worker: "stub", Resources: work.Resources{CPU: 2, MemoryMB: 1000, DiskMB: 1000}, Stage: work.TaskReady})
					}
					if err := e.NodeChanged(ctx, node); err != nil {
						return err
					}
					return e.Admit(ctx, work.Goal{ID: "g", Person: person, Agent: agent, Summary: "produce independent results", Done: "results are checked", NoServiceReason: "local files", Scope: work.GoalScope{Repositories: []string{"fixture"}, Includes: "fixture results"}, Tasks: tasks, Criteria: []work.Criterion{{ID: "result", Statement: "result is readable", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "read", Target: "fixture"}}}})
				},
				Changed: func(ctx context.Context, s work.State) error {
					for _, g := range s.Goals {
						for _, task := range g.Tasks {
							if task.ID == "b" && task.Stage == work.TaskDelivered {
								delivered = true
							}
						}
					}
					for _, a := range s.Actions {
						if a.TaskID == "a" && a.ObserveActionID != "" && original == "" {
							original = a.ObserveActionID
							// Capacity is released only after the stale receipt has
							// been replaced, so the ordering is deterministic.
							node.CPUCores, node.CPULimit, node.LoadCPU = 4, 4, 2
							node.ObservedActions = []string{original}
							return driver.engine.NodeChanged(ctx, node)
						}
					}
					return nil
				},
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			err = boot.Run(ctx, cfg)
			if ctx.Err() != nil {
				t.Fatalf("Core did not dispatch the independent task: %v", ctx.Err())
			}
			if !errors.Is(err, work.ErrInvalid) {
				t.Fatalf("lost integration failure: %v", err)
			}
			if !delivered {
				t.Error("independent result was not accepted before inspection")
			}
			if body, err := os.ReadFile(driver.output); err != nil || string(body) != "independent result\n" {
				t.Fatalf("worker result: %q %v", body, err)
			}
			if _, err := driver.engine.Snapshot(t.Context()); !errors.Is(err, canon.ErrClosed) {
				t.Errorf("Core returned while canon remains open: %v", err)
			}
			driver.mu.Lock()
			defer driver.mu.Unlock()
			if driver.assignments["b"] != 1 {
				t.Errorf("independent worker dispatched %d times", driver.assignments["b"])
			}
			if replacement {
				if driver.assignments["a"] != 1 || original == "" || len(driver.observed) == 0 {
					t.Fatalf("worker replaced or not observed: %+v %v", driver.assignments, driver.observed)
				}
				for _, id := range driver.observed {
					if id != original {
						t.Errorf("observed %s instead of original %s", id, original)
					}
				}
			}
		})
	}
}
