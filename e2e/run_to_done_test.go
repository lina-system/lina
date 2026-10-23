// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/thisisjun786/lina/kit/proc"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

func TestRunToDoneStubWorker(t *testing.T) {
	if os.Getenv("LINA_RUN_TO_DONE_STUB") != "1" {
		return
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

type codeWorld struct {
	mu                       sync.Mutex
	heads, merges            map[string]string
	qa                       map[string]bool
	deployed                 string
	calls                    []work.Action
	active, maximum, initial int
	killed, conflicted       bool
	firstPair                chan struct{}
	root, url                string
}

func (w *codeWorld) Authorize(_ context.Context, a work.Action) error {
	// Only this fixture's private service is granted. Real public deploys must
	// receive their own argument-bound approval from the installed authority.
	if a.GoalID != "code-goal" {
		return work.ErrInvalid
	}
	return nil
}
func (w *codeWorld) Observe(_ context.Context, a work.Action) (work.Observation, error) {
	return work.Observation{}, fmt.Errorf("unexpected uncertainty for fixture action %s", a.ID)
}
func (w *codeWorld) output(id string, value any) string {
	raw, _ := json.Marshal(value)
	path := filepath.Join(w.root, id+".json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		return ""
	}
	return path
}
func (w *codeWorld) probe(ctx context.Context, revision string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var got struct {
		Revision string
		Healthy  bool
	}
	if err = json.NewDecoder(res.Body).Decode(&got); err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK || got.Revision != revision || !got.Healthy {
		return fmt.Errorf("service revision is not the deployed result")
	}
	return nil
}
func (w *codeWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	w.mu.Lock()
	w.calls = append(w.calls, a)
	w.mu.Unlock()
	out := work.Observation{Status: work.OutcomeSucceeded}
	switch a.Kind {
	case work.ContinueWorker, work.Assign, work.Resume, work.RepairConflict, work.RepairQA:
		w.mu.Lock()
		w.active++
		if w.active > w.maximum {
			w.maximum = w.active
		}
		first := a.Kind == work.Assign && (a.TaskID == "a" || a.TaskID == "b")
		if first {
			w.initial++
			if w.initial == 2 {
				close(w.firstPair)
			}
		}
		w.mu.Unlock()
		defer func() { w.mu.Lock(); w.active--; w.mu.Unlock() }()
		if first {
			select {
			case <-w.firstPair:
			case <-ctx.Done():
				return out, ctx.Err()
			}
		}
		if a.TaskID == "a" && a.Attempt == 1 {
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunToDoneStubWorker$")
			cmd.Env = []string{"LINA_RUN_TO_DONE_STUB=1"}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return out, err
			}
			input, err := cmd.StdinPipe()
			if err != nil {
				return out, err
			}
			defer input.Close()
			if err = cmd.Start(); err != nil {
				return out, err
			}
			ready, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || ready != "ready\n" {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return out, fmt.Errorf("stub startup: %q %v", ready, err)
			}
			if err = cmd.Process.Kill(); err != nil {
				_ = cmd.Wait()
				return out, err
			}
			if err = cmd.Wait(); err == nil {
				return out, fmt.Errorf("worker was not killed")
			}
			w.mu.Lock()
			w.killed = true
			w.mu.Unlock()
			// The stopped stub held only its private pipe and produced no files or
			// external calls. Its process exit and absent receipt settle that attempt.
			return work.Observation{Status: work.OutcomeFailed, EffectsSettled: true, Code: "worker_dead"}, nil
		}
		head := fmt.Sprintf("head-%s-%d", a.TaskID, a.Attempt)
		w.mu.Lock()
		w.heads[a.TaskID] = head
		w.mu.Unlock()
		out.Head = head
		out.EffectsSettled = true
	case work.InspectPR:
		w.mu.Lock()
		head := w.heads[a.TaskID]
		w.mu.Unlock()
		if head == "" {
			return out, fmt.Errorf("missing worker artifact")
		}
		out.PR = work.PRState{Head: head, Mergeable: true, Checks: work.Proof{Revision: head, Ref: w.output("ci-"+head, true), Passed: true}, Review: work.Proof{Revision: head, Ref: w.output("review-"+head, true), Passed: true}, Verified: work.Proof{Revision: head, Ref: w.output("manager-"+head, true), Passed: true}}
	case work.Merge:
		w.mu.Lock()
		if a.TaskID == "d" && !w.conflicted {
			w.conflicted = true
			w.mu.Unlock()
			return work.Observation{Status: work.OutcomeConflict}, nil
		}
		if w.heads[a.TaskID] != a.Revision {
			w.mu.Unlock()
			return out, fmt.Errorf("merge head moved")
		}
		merged := fmt.Sprintf("merge-%d-%s", len(w.merges)+1, a.TaskID)
		w.merges[a.TaskID] = merged
		w.mu.Unlock()
		out.Head = a.Revision
		out.Proof = work.Proof{Revision: merged, Ref: w.output(merged, map[string]string{"head": a.Revision, "merged": merged}), Passed: true}
	case work.RunQA:
		w.mu.Lock()
		exists := w.merges[a.TaskID] == a.Revision
		w.qa[a.Revision] = exists
		w.mu.Unlock()
		if !exists {
			return out, fmt.Errorf("QA was not pinned to a merge")
		}
		out.Proof = work.Proof{Revision: a.Revision, Ref: w.output("qa-"+a.Revision, map[string]any{"revision": a.Revision, "passed": true}), Passed: true}
	case work.Deploy:
		w.mu.Lock()
		for _, merged := range w.merges {
			if !w.qa[merged] {
				w.mu.Unlock()
				return out, fmt.Errorf("deployment preceded QA")
			}
		}
		w.deployed = a.Revision
		w.mu.Unlock()
		out.Proof = work.Proof{Revision: a.Revision, Ref: w.output("deployment", map[string]string{"revision": a.Revision}), Passed: true}
	case work.Verify:
		if err := w.probe(ctx, a.Revision); err != nil {
			return out, err
		}
		out.Proof = work.Proof{Revision: a.Revision, Ref: w.output("runtime", map[string]any{"revision": a.Revision, "healthy": true}), Passed: true}
	case work.CheckCriterion:
		if a.Check == nil {
			return out, fmt.Errorf("missing criterion check")
		}
		var valid bool
		switch a.Check.Method {
		case "merge.read":
			w.mu.Lock()
			valid = w.merges[a.Check.TaskID] == a.Revision
			w.mu.Unlock()
		case "qa.read":
			w.mu.Lock()
			valid = w.qa[a.Revision]
			w.mu.Unlock()
		case "deploy.read":
			w.mu.Lock()
			valid = w.deployed == a.Revision
			w.mu.Unlock()
		case "runtime.probe", "service.answers":
			valid = w.probe(ctx, a.Revision) == nil
		default:
			return out, fmt.Errorf("unknown check %q", a.Check.Method)
		}
		out.Criterion = &work.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Collected: &work.CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}, Ref: w.output("criterion-"+a.ID, map[string]any{"revision": a.Revision, "passed": valid}), Passed: valid}
	case work.Observe, work.Decide, work.PlanGoal:
		return out, fmt.Errorf("unexpected action %s", a.Kind)
	}
	return out, nil
}

func TestM2RunToDoneCodeGoal(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-work-e2e-")
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
	proofs := filepath.Join(root, "proofs")
	if err = os.Mkdir(proofs, 0700); err != nil {
		t.Fatal(err)
	}
	world := &codeWorld{heads: map[string]string{}, merges: map[string]string{}, qa: map[string]bool{}, firstPair: make(chan struct{}), root: proofs}
	service := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		world.mu.Lock()
		deployed := world.deployed
		world.mu.Unlock()
		_ = json.NewEncoder(rw).Encode(map[string]any{"Revision": deployed, "Healthy": deployed != ""})
	}))
	defer service.Close()
	world.url = service.URL
	closed := make(chan work.Goal, 1)
	var engine *work.Engine
	admissions := 0
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) { engine = e; return world, nil },
		MainHost: func(context.Context) (work.Node, error) {
			return work.Node{ID: "main-work-fixture", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 4, CPULimit: 4, AvailableMemoryMB: 8000, MemoryLimitMB: 8000, AvailableDiskMB: 8000, DiskLimitMB: 8000, Workers: map[string]bool{"stub": true}}, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			agent, err := fixtureWorkOwner(ctx, root)
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
			tasks := []work.Task{}
			for _, id := range []string{"a", "b", "c", "d", "e"} {
				tasks = append(tasks, work.Task{ID: id, Done: "change is merged and QA passes", Repository: "fixture-repo", Base: "dev", Area: id, Worker: "stub", Resources: work.Resources{CPU: 2, MemoryMB: 1000, DiskMB: 1000}, Stage: work.TaskReady})
			}
			tasks[2].DependsOn = []string{"a"}
			tasks[3].DependsOn = []string{"b"}
			tasks[4].DependsOn = []string{"c", "d"}
			admissions++
			return e.Admit(ctx, work.Goal{ID: "code-goal", Person: person, Agent: agent, Summary: "the service runs the requested change", Done: "private service answers at the merged revision", Scope: work.GoalScope{Repositories: []string{"fixture-repo"}, Includes: "five code tasks"}, ServiceTarget: "private-service", Tasks: tasks, Criteria: []work.Criterion{{ID: "answers", Statement: "the service answers correctly", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "service.answers", Target: "private-service"}}}})
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
	run := func() (context.CancelFunc, <-chan error) {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- boot.Run(ctx, cfg) }()
		return cancel, done
	}
	cancel, done := run()
	t.Cleanup(cancel)
	var goal work.Goal
	select {
	case goal = <-closed:
	case err = <-done:
		t.Fatalf("Core stopped: %v", err)
	case <-time.After(45 * time.Second):
		cancel()
		<-done
		t.Fatal("code goal did not finish")
	}
	summary, err := engine.Summary(t.Context(), goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range goal.Tasks {
		if task.Stage != work.TaskDone || !task.Merge.Passed || !task.QA.Passed || !strings.Contains(summary, task.Merge.Revision) {
			t.Fatalf("missing task evidence: %+v / %s", task, summary)
		}
	}
	if !goal.Deployment.Passed || !goal.Verification.Passed || len(goal.Checks) != 13 {
		t.Fatalf("closed without the full criterion evidence: %+v", goal)
	}
	for _, proof := range goal.Checks {
		if !proof.Passed {
			t.Fatal("failed criterion closed")
		}
		if _, err = os.Stat(proof.Ref); err != nil {
			t.Fatalf("check output not retained: %v", err)
		}
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	world.mu.Lock()
	calls := len(world.calls)
	killed, conflicted, maximum := world.killed, world.conflicted, world.maximum
	world.mu.Unlock()
	if !killed || !conflicted || maximum != 2 {
		t.Fatalf("recovery/capacity: killed=%v conflict=%v max=%d", killed, conflicted, maximum)
	}
	// A completed goal survives a new Core epoch without repeating any effect or
	// needing another person message. The same durable record feeds the summary.
	for len(closed) > 0 {
		<-closed
	}
	cancel, done = run()
	t.Cleanup(cancel)
	select {
	case <-closed:
	case err = <-done:
		t.Fatalf("restart stopped: %v", err)
	case <-time.After(30 * time.Second):
		cancel()
		<-done
		t.Fatal("restart lost the completed goal")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	world.mu.Lock()
	defer world.mu.Unlock()
	if len(world.calls) != calls || admissions != 1 {
		t.Fatalf("restart repeated work/person admission: calls %d -> %d, admissions %d", calls, len(world.calls), admissions)
	}
}

// Select the installed dispatch-enabled starter for trusted goal ingress.
func fixtureWorkOwner(ctx context.Context, root string) (string, error) {
	db, err := canon.OpenSQLiteReader(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		return "", err
	}
	defer db.Close()
	var id string
	err = db.QueryRowContext(ctx, "SELECT agent_id FROM agent_starters WHERE preset='dev'").Scan(&id)
	return id, err
}
