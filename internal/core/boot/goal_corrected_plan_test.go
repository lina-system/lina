// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/records"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

type correctedWorkDriver struct {
	engine     *work.Engine
	root       string
	observed   chan work.Action
	originalID string
	task       work.Task
}

func (d *correctedWorkDriver) Authorize(context.Context, work.Action) error { return nil }
func (d *correctedWorkDriver) Observe(_ context.Context, a work.Action) (work.Observation, error) {
	if a.ID != d.originalID {
		return work.Observation{}, fmt.Errorf("lost old definition")
	}
	d.observed <- a
	return work.Observation{Status: work.OutcomeSucceeded, Head: "obsolete-head", EffectsSettled: true}, nil
}
func (d *correctedWorkDriver) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	o := work.Observation{Status: work.OutcomeSucceeded}
	s, err := d.engine.Snapshot(ctx)
	if err != nil {
		return o, err
	}
	g := s.Goals[0]
	switch a.Kind {
	case work.Assign:
		if a.TaskID != "same-id" || g.Tasks[0].Done != "revised instructions" {
			return o, fmt.Errorf("executed obsolete task")
		}
		if err = os.WriteFile(filepath.Join(d.root, "new-result"), []byte(g.Tasks[0].Done), 0600); err != nil {
			return o, err
		}
		o.Head, o.EffectsSettled = "revised-head", true
	case work.InspectPR:
		p := work.Proof{Revision: "revised-head", Ref: "proof", Passed: true}
		o.PR = work.PRState{Head: "revised-head", Checks: p, Review: p, Verified: p, Mergeable: true}
	case work.Merge:
		o.Head = "revised-head"
		o.Proof = work.Proof{Revision: "merged-revision", Ref: "merge", Passed: true}
	case work.RunQA, work.Verify:
		data, err := os.ReadFile(filepath.Join(d.root, "new-result"))
		if err != nil || string(data) != "revised instructions" {
			return o, fmt.Errorf("missing revised result: %v", err)
		}
		o.Proof = work.Proof{Revision: a.Revision, Ref: "result-read", Passed: true}
	case work.CheckCriterion:
		o.Criterion = &work.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: "result-read", Passed: true}
	case work.PlanGoal:
		o.Tasks = []work.Task{d.task}
	case work.Resume, work.RepairConflict, work.RepairQA, work.ContinueWorker, work.Deploy, work.Observe, work.Decide:
		return o, fmt.Errorf("unexpected action %s", a.Kind)
	}
	return o, nil
}

func TestWorkRuntimeCompletesCorrectedPlanAndKeepsRetiredArtifact(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	r.agent = workAgentFixture(t, r)
	repo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r.records = repo
	artifact := filepath.Join(r.root, "old-result")
	if err = os.WriteFile(artifact, []byte("keep old result"), 0600); err != nil {
		t.Fatal(err)
	}
	d := &correctedWorkDriver{root: r.root, observed: make(chan work.Action, 2)}
	closed := make(chan work.Goal, 1)
	cfg := WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) { d.engine = e; return d, nil },
		Ready: func(ctx context.Context, e *work.Engine, person, agent string) error {
			task := work.Task{ID: "same-id", Done: "old instructions", Repository: "repo", Base: "dev", Area: "area", Worker: "stub", Resources: work.Resources{CPU: 1, MemoryMB: 100, DiskMB: 100}, Stage: work.TaskReady}
			g := work.Goal{ID: "g", Person: person, Agent: agent, Summary: "corrected work", Done: "revised file exists", NoServiceReason: "library", Scope: work.GoalScope{Repositories: []string{"repo"}, Includes: "result"}, Tasks: []work.Task{task}, Criteria: []work.Criterion{{ID: "result", Statement: "file exists", Check: work.CriterionCheck{Kind: work.ReadEvidence, Method: "file.read", Target: "result"}}}}
			if err := e.Admit(ctx, g); err != nil {
				return err
			}
			if err := e.NodeChanged(ctx, work.Node{ID: "node", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 2, CPULimit: 2, AvailableMemoryMB: 1000, MemoryLimitMB: 1000, AvailableDiskMB: 1000, DiskLimitMB: 1000, Workers: map[string]bool{"stub": true}}); err != nil {
				return err
			}
			if err := e.Reconcile(ctx); err != nil {
				return err
			}
			s, err := e.Snapshot(ctx)
			if err != nil {
				return err
			}
			old, err := e.Claim(ctx, s.Actions[0].ID)
			if err != nil {
				return err
			}
			g = s.Goals[0]
			record, rev, err := repo.Read(ctx, g.RecordID)
			if err != nil {
				return err
			}
			if _, err = repo.Commit(ctx, records.Change{ID: record.ID, Path: record.Path, Base: rev, Data: append(record.Data, []byte("\nUse revised instructions.\n")...)}, records.Actor{Person: person, Agent: agent}); err != nil {
				return err
			}
			if err = e.Reconcile(ctx); err != nil {
				return err
			}
			s, err = e.Snapshot(ctx)
			if err != nil {
				return err
			}
			task.Done = "revised instructions"
			d.task, d.originalID = task, old.ID
			return e.Signal(ctx, work.Signal{ID: "old-worker-ended", ActionID: old.ID})
		},
		Changed: func(_ context.Context, s work.State) error {
			if len(s.Goals) > 0 && s.Goals[0].State == work.GoalDone {
				select {
				case closed <- s.Goals[0]:
				default:
				}
			}
			return nil
		},
	}
	if err = r.startWork(t.Context(), &cfg); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.workEngine.close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	select {
	case g := <-closed:
		if len(g.Tasks) != 1 || g.Tasks[0].Head != "revised-head" || g.Tasks[0].Attempt != 2 {
			t.Fatalf("wrong corrected result: %+v", g.Tasks)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Core failed to complete corrected plan")
	}
	select {
	case <-d.observed:
	default:
		t.Fatal("Core did not read retired outcome")
	}
	data, err := os.ReadFile(artifact)
	if err != nil || string(data) != "keep old result" {
		t.Fatalf("correction removed old artifact: %q %v", data, err)
	}
}
