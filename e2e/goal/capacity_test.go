// SPDX-License-Identifier: Apache-2.0
package goal_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

func TestCoreReplansQueuedAssignmentAfterHostChanges(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-capacity-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"capacity-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := &repairWorld{root: t.TempDir()}
	host := func() work.Node {
		return work.Node{ID: "capacity-main", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 1, CPULimit: 1, AvailableMemoryMB: 1024, MemoryLimitMB: 1024, AvailableDiskMB: 1024, DiskLimitMB: 1024, Workers: map[string]bool{"stub": true}}
	}
	var old work.Action
	for _, restart := range []bool{false, true} {
		observed := make(chan work.State, 1)
		cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
			Driver: func(e *work.Engine) (work.Driver, error) { w.engine = e; return &capacityWorld{w}, nil },
			MainHost: func(context.Context) (work.Node, error) {
				n := host()
				n.Yielding = !restart
				return n, nil
			},
			Ready: func(ctx context.Context, e *work.Engine, person, agent string) error {
				agent, err := fixtureWorkOwner(ctx, root)
				if err != nil {
					return err
				}
				if restart {
					return nil
				}
				initial := host()
				initial.ObservedAt-- // Distinct from later reports even within one second.
				if err := e.NodeChanged(ctx, initial); err != nil {
					return err
				}
				g := work.Goal{ID: "runtime-repair", Person: person, Agent: agent, Summary: "produce the library", Done: "library artifact is healthy", Scope: work.GoalScope{Repositories: []string{"fixture"}, Includes: "library artifact"}, NoServiceReason: "library", Tasks: []work.Task{repairTask("repair")}, Criteria: []work.Criterion{{ID: "artifact", Statement: "artifact is healthy", Check: work.CriterionCheck{Kind: work.ReadEvidence, Method: "artifact.read", Target: "fixture"}}}}
				if err := e.Admit(ctx, g); err != nil {
					return err
				}
				if err := e.Reconcile(ctx); err != nil {
					return err
				}
				s, err := e.Snapshot(ctx)
				if err != nil {
					return err
				}
				if len(s.Actions) != 1 || s.Actions[0].State != "queued" {
					t.Errorf("assignment was not queued: %+v", s)
				}
				old = s.Actions[0]
				// Run refreshes the host after this queue commit, before claiming.
				return nil
			},
			Changed: func(_ context.Context, s work.State) error {
				g := s.Goals[0]
				ready := g.State == work.GoalDone || g.State == work.GoalBlocked
				if !restart {
					ready = ready || len(g.Waits) == 1 && g.Waits[0].Reason == "host_capacity"
					for _, a := range s.Actions {
						ready = ready || a.State == "claimed" || a.State == "running"
					}
				}
				if ready {
					select {
					case observed <- s:
					default:
					}
				}
				return nil
			},
		}}
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		done := make(chan error, 1)
		go func() { done <- boot.Run(ctx, cfg) }()
		var s work.State
		select {
		case s = <-observed:
		case err := <-done:
			cancel()
			t.Fatalf("Core stopped: %v", err)
		case <-ctx.Done():
			cancel()
			<-done
			t.Fatal("Core did not reach the expected capacity state")
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		g := s.Goals[0]
		if !restart {
			if g.State != work.GoalWaiting || g.Tasks[0].Stage != work.TaskReady || g.Tasks[0].Attempt != 0 || len(s.Actions) != 1 || s.Actions[0].State != "finished" {
				t.Fatalf("Core dispatched instead of releasing an ineligible award: %+v", s)
			}
			if len(w.calls) != 0 {
				t.Fatalf("worker ran on yielding host: %+v", w.calls)
			}
			continue
		}
		if g.State != work.GoalDone || len(g.Checks) != 4 || !g.Verification.Passed || !g.Tasks[0].QA.Passed {
			t.Fatalf("restarted Core did not finish with evidence: %+v", g)
		}
		assignments := 0
		for _, a := range w.calls {
			if a.Kind == work.Assign {
				assignments++
				if a.ID == old.ID || a.Attempt != old.Attempt {
					t.Fatalf("reused retired award or consumed attempt: %+v", a)
				}
			}
		}
		if assignments != 1 {
			t.Fatalf("worker executions: %d", assignments)
		}
		data, err := os.ReadFile(filepath.Join(w.root, "merged-repair"))
		if err != nil || string(data) != "healthy" {
			t.Fatalf("missing worker result: %q %v", data, err)
		}
	}
}

// Reuse the subprocess worker and recorded merge/QA checks. The library has
// no deployment: verification reads the actual landed artifact instead.
type capacityWorld struct{ *repairWorld }

func (w *capacityWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	if a.Kind != work.Verify && !(a.Kind == work.CheckCriterion && (a.Check.Method == "artifact.read" || a.Check.Method == "runtime.probe")) {
		return w.repairWorld.Execute(ctx, a)
	}
	data, err := os.ReadFile(filepath.Join(w.root, a.Revision))
	if err != nil {
		return work.Observation{}, err
	}
	passed := string(data) == "healthy"
	proof := work.Proof{Revision: a.Revision, Ref: filepath.Join(w.root, a.Revision), Passed: passed}
	out := work.Observation{Status: work.OutcomeSucceeded, Proof: proof}
	if a.Kind == work.CheckCriterion {
		out.Criterion = &work.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: proof.Ref, Passed: passed}
	}
	return out, nil
}
