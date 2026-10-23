// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

// Stub worker and GitHub effects produce files and receipts. Verification uses
// the installed driver's real QA-file reader, including its target binding.
type libraryCorrectionDriver struct {
	engine *work.Engine
	checks *codeGoalDriver
	task   work.Task
	mu     sync.Mutex
	calls  []work.Action
}

func (d *libraryCorrectionDriver) Authorize(context.Context, work.Action) error { return nil }
func (d *libraryCorrectionDriver) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, fmt.Errorf("unexpected effect replay")
}
func (d *libraryCorrectionDriver) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	d.mu.Lock()
	d.calls = append(d.calls, a)
	d.mu.Unlock()
	o := work.Observation{Status: work.OutcomeSucceeded, EffectsSettled: true}
	proof := func(revision, ref string) work.Proof { return work.Proof{Revision: revision, Ref: ref, Passed: true} }
	switch a.Kind {
	case work.Assign:
		err := os.WriteFile(filepath.Join(d.checks.runtime.root, a.TaskID+".result"), []byte(a.TaskID), 0600)
		o.Head = "head-" + a.TaskID
		return o, err
	case work.InspectPR:
		p := proof(a.Revision, "fixture-pr")
		o.PR = work.PRState{Head: a.Revision, Checks: p, Review: p, Verified: p, Mergeable: true}
	case work.Merge:
		o.Head, o.Proof = a.Revision, proof("merge-"+a.TaskID, "merge:"+a.TaskID)
	case work.RunQA:
		data, err := os.ReadFile(filepath.Join(d.checks.runtime.root, a.TaskID+".result"))
		if err != nil || string(data) != a.TaskID {
			return o, fmt.Errorf("missing worker result: %v", err)
		}
		command := d.checks.connections.Repositories["repo"].QA
		path := d.checks.evidencePath(a, command)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return o, err
		}
		passed := true
		raw, err := json.Marshal(codeCheckRecord{Command: commandKey(command), ExitOK: true, Output: codeCheckOutput{Revision: a.Revision, Target: "repo", Passed: &passed, Evidence: "Read worker result"}})
		if err != nil {
			return o, err
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			return o, err
		}
		o.Proof, err = d.checks.readProof(path, command, "repo", a.Revision)
		return o, err
	case work.PlanGoal:
		o.Tasks = []work.Task{d.task}
	case work.Verify, work.CheckCriterion:
		s, err := d.engine.Snapshot(ctx)
		if err != nil {
			return o, err
		}
		g := s.Goals[0]
		if a.Kind == work.Verify || a.Check.Method == "runtime.probe" {
			o, err = d.checks.libraryProof(ctx, s, g, a)
		} else {
			// Re-read the retained worker artifact for the fixture's GitHub and
			// custom criterion boundaries; QA reads production-format evidence.
			data, readErr := os.ReadFile(filepath.Join(d.checks.runtime.root, "a.result"))
			if readErr != nil || string(data) != "a" {
				return o, fmt.Errorf("lost retained result: %v", readErr)
			}
			o.Proof = proof(a.Revision, g.Tasks[0].QA.Ref)
		}
		if err != nil {
			return o, err
		}
		if a.Kind == work.CheckCriterion {
			o.Criterion = &work.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: o.Proof.Ref, Passed: o.Proof.Passed}
		}
	case work.Resume, work.RepairConflict, work.RepairQA, work.ContinueWorker, work.Deploy, work.Observe, work.Decide:
		return o, fmt.Errorf("unexpected action: %s", a.Kind)
	}
	return o, nil
}

func TestCoreLibraryCorrectionRetiresLatestMerge(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-library-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"library-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	task := func(id string) work.Task {
		return work.Task{ID: id, Done: "Produce a result", Repository: "repo", Base: "dev", Area: id, Worker: "stub", Stage: work.TaskReady, Resources: work.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}}
	}
	d := &libraryCorrectionDriver{task: task("a"), checks: &codeGoalDriver{runtime: &conversationRuntime{root: root}, connections: codeConnections{Repositories: map[string]codeRepository{"repo": {QA: codeCommand{Path: "/fixture/qa"}}}}}}
	terminal := make(chan work.Goal, 8)
	var retained work.Task
	var originalEvidence []work.Proof
	cfg := Config{StateRoot: root, Socket: filepath.Join(root, "client.sock"), Work: &WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) { d.engine = e; return d, nil },
		Ready: func(ctx context.Context, e *work.Engine, person, agent string) error {
			g := work.Goal{ID: "library", Person: person, Agent: agent, Summary: "Produce two results", Done: "Results pass QA", NoServiceReason: "library", Scope: work.GoalScope{Repositories: []string{"repo"}, Includes: "results"}, Tasks: []work.Task{task("a"), task("b")}, Criteria: []work.Criterion{{ID: "result", Statement: "Result exists", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "fixture.read", Target: "repo"}}}}
			if err := e.Admit(ctx, g); err != nil {
				return err
			}
			if err := e.NodeChanged(ctx, work.Node{ID: "library-fixture", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 2, CPULimit: 2, AvailableMemoryMB: 100, MemoryLimitMB: 100, AvailableDiskMB: 100, DiskLimitMB: 100, Workers: map[string]bool{"stub": true}}); err != nil {
				return err
			}
			// Serial ingress fixes the merge order without racing Core's runner.
			for _, id := range []string{"a", "b"} {
				for _, kind := range []work.ActionKind{work.Assign, work.InspectPR, work.Merge, work.RunQA} {
					if err := e.Reconcile(ctx); err != nil {
						return err
					}
					s, err := e.Snapshot(ctx)
					if err != nil {
						return err
					}
					found := false
					for _, a := range s.Actions {
						if a.TaskID != id || a.Kind != kind || a.State != "queued" {
							continue
						}
						found = true
						a, err = e.Claim(ctx, a.ID)
						if err != nil {
							return err
						}
						o, err := d.Execute(ctx, a)
						if err != nil {
							return err
						}
						if err = e.Report(ctx, a.ID, a.Epoch, o); err != nil {
							return err
						}
						break
					}
					if !found {
						return fmt.Errorf("missing %s for %s", kind, id)
					}
				}
			}
			s, err := e.Snapshot(ctx)
			if err != nil {
				return err
			}
			g = s.Goals[0]
			retained, originalEvidence = g.Tasks[0], g.Evidence
			_, err = e.Revise(ctx, g.ID, g.RecordRevision, person, agent, work.Definition{Outcome: "Keep only the first result", Done: g.Done, Scope: g.Scope, Criteria: g.Criteria[:1]})
			return err
		},
		Changed: func(_ context.Context, s work.State) error {
			for _, g := range s.Goals {
				if g.State == work.GoalDone || g.State == work.GoalBlocked {
					select {
					case terminal <- g:
					default:
					}
				}
			}
			return nil
		},
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	run := func() func() {
		child, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- Run(child, cfg) }()
		var once sync.Once
		join := func() {
			once.Do(func() {
				stop()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
		}
		t.Cleanup(join)
		return join
	}
	stop := run()
	var g work.Goal
	select {
	case g = <-terminal:
	case <-ctx.Done():
		t.Fatal("Core did not settle corrected library goal")
	}
	stop()
	if g.State != work.GoalDone || g.TargetRevision != "merge-a" || g.Verification.Ref != retained.QA.Ref {
		t.Fatalf("corrected library blocked or used retired proof: state=%s target=%s verification=%+v", g.State, g.TargetRevision, g.Verification)
	}
	if len(g.Tasks) != 1 || !reflect.DeepEqual(g.Tasks[0], retained) || len(g.Checks) != 4 {
		t.Fatalf("retained work changed or criteria were not checked: %+v", g)
	}
	if !reflect.DeepEqual(g.Evidence[:len(originalEvidence)], originalEvidence) {
		t.Fatal("retired evidence erased")
	}
	if data, err := os.ReadFile(filepath.Join(root, "b.result")); err != nil || string(data) != "b" {
		t.Fatal("retired artifact lost", err)
	}
	for _, a := range d.calls[8:] {
		if a.Kind != work.PlanGoal && a.Kind != work.Verify && a.Kind != work.CheckCriterion {
			t.Fatalf("unchanged work reran: %s", a.Kind)
		}
	}
	// Evidence is re-read, never accepted only because the retained task is done.
	if err := os.Remove(retained.QA.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := d.checks.libraryProof(t.Context(), work.State{}, g, work.Action{Revision: g.TargetRevision}); err == nil {
		t.Fatal("missing QA evidence accepted")
	}
	before := len(d.calls)
	cfg.Work.Ready = func(ctx context.Context, e *work.Engine, _, _ string) error {
		s, err := e.Snapshot(ctx)
		if err != nil {
			return err
		}
		if len(s.Goals) != 1 || s.Goals[0].State != work.GoalDone || s.Goals[0].TargetRevision != "merge-a" {
			return fmt.Errorf("restart lost corrected target")
		}
		terminal <- s.Goals[0]
		return nil
	}
	for len(terminal) > 0 {
		<-terminal
	}
	stop = run()
	select {
	case <-terminal:
	case <-ctx.Done():
		t.Fatal("Core restart did not restore goal")
	}
	stop()
	if len(d.calls) != before {
		t.Fatal("restart repeated completed work")
	}
}
