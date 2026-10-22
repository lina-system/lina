// SPDX-License-Identifier: Apache-2.0
package goal_test

import (
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
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/proc"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

type repairWorld struct {
	mu                  sync.Mutex
	root, url, deployed string
	calls               []work.Action
	engine              *work.Engine
}

func (w *repairWorld) Authorize(_ context.Context, a work.Action) error {
	if a.GoalID != "runtime-repair" {
		return work.ErrInvalid
	}
	return nil // This fixture grants only a private disposable service.
}

func (w *repairWorld) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, fmt.Errorf("unexpected uncertain effect")
}

func repairTask(id string) work.Task {
	return work.Task{ID: id, Done: "service answers correctly", Repository: "fixture", Base: "dev", Area: "service", Worker: "stub", Resources: work.Resources{CPU: 1, MemoryMB: 128, DiskMB: 128}, Stage: work.TaskReady}
}

func (w *repairWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	w.mu.Lock()
	w.calls = append(w.calls, a)
	w.mu.Unlock()
	out := work.Observation{Status: work.OutcomeSucceeded}
	proof := func(revision string, passed bool, data any) (work.Proof, error) {
		raw, err := json.Marshal(data)
		if err != nil {
			return work.Proof{}, err
		}
		path := filepath.Join(w.root, a.ID+".json")
		err = os.WriteFile(path, raw, 0600)
		return work.Proof{Revision: revision, Ref: path, Passed: passed}, err
	}
	switch a.Kind {
	case work.Assign:
		// The stub worker is a real subprocess producing the service artifact.
		value := "broken"
		if a.TaskID == "repair" {
			value = "healthy"
		}
		out.Head = "head-" + a.TaskID
		cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$1" > "$2"`, "worker", value, filepath.Join(w.root, out.Head))
		if data, err := cmd.CombinedOutput(); err != nil {
			return out, fmt.Errorf("worker: %w: %s", err, data)
		}
		out.EffectsSettled = true
	case work.InspectPR:
		if _, err := os.ReadFile(filepath.Join(w.root, a.Revision)); err != nil {
			return out, err
		}
		p, err := proof(a.Revision, true, map[string]string{"head": a.Revision, "review": "verified", "checks": "passed"})
		if err != nil {
			return out, err
		}
		out.PR = work.PRState{Head: a.Revision, Checks: p, Review: p, Verified: p, Mergeable: true}
	case work.Merge:
		data, err := os.ReadFile(filepath.Join(w.root, a.Revision))
		if err != nil {
			return out, err
		}
		revision := "merged-" + a.TaskID
		if err = os.WriteFile(filepath.Join(w.root, revision), data, 0600); err != nil {
			return out, err
		}
		out.Head = a.Revision
		out.Proof, err = proof(revision, true, map[string]string{"head": a.Revision, "merged": revision})
		if err != nil {
			return out, err
		}
	case work.RunQA:
		data, err := os.ReadFile(filepath.Join(w.root, a.Revision))
		if err != nil || len(data) == 0 {
			return out, fmt.Errorf("missing merged artifact: %v", err)
		}
		// This structural QA deliberately misses the service's semantic defect.
		out.Proof, err = proof(a.Revision, true, string(data))
		if err != nil {
			return out, err
		}
	case work.Deploy:
		if _, err := os.ReadFile(filepath.Join(w.root, a.Revision)); err != nil {
			return out, err
		}
		w.mu.Lock()
		w.deployed = a.Revision
		w.mu.Unlock()
		var err error
		out.Proof, err = proof(a.Revision, true, a.Revision)
		if err != nil {
			return out, err
		}
	case work.Verify:
		status, body, err := w.probe(ctx, a.Revision)
		if err != nil {
			return out, err
		}
		passed := status == http.StatusOK && body == "healthy"
		out.Proof, err = proof(a.Revision, passed, map[string]any{"status": status, "body": body})
		if err != nil {
			return out, err
		}
		if !passed {
			out.Status = work.OutcomeFailed
		}
	case work.PlanGoal:
		s, err := w.engine.Snapshot(ctx)
		if err != nil {
			return out, err
		}
		g := s.Goals[0]
		if a.FailureKey == "" || a.Revision != "merged-initial" || g.Verification.Passed || g.Verification.Revision != a.Revision || g.Scope.Includes != "repair the fixture service" {
			return out, fmt.Errorf("manager did not receive scoped failure: %+v / %+v", a, g)
		}
		data, err := os.ReadFile(g.Verification.Ref)
		if err != nil || !strings.Contains(string(data), "broken") {
			return out, fmt.Errorf("missing failed probe output: %v", err)
		}
		repair := repairTask("repair")
		repair.DependsOn = []string{"initial"}
		out.Tasks = []work.Task{repair}
	case work.CheckCriterion:
		if a.Check == nil {
			return out, work.ErrInvalid
		}
		passed := false
		switch a.Check.Method {
		case "merge.read", "qa.read":
			data, err := os.ReadFile(filepath.Join(w.root, a.Revision))
			passed = err == nil && len(data) > 0
		case "deploy.read":
			w.mu.Lock()
			passed = w.deployed == a.Revision
			w.mu.Unlock()
		case "runtime.probe", "service.answers":
			status, body, err := w.probe(ctx, a.Revision)
			passed = err == nil && status == http.StatusOK && body == "healthy"
		default:
			return out, fmt.Errorf("unexpected check: %s", a.Check.Method)
		}
		p, err := proof(a.Revision, passed, map[string]any{"method": a.Check.Method, "passed": passed})
		if err != nil {
			return out, err
		}
		out.Criterion = &work.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Ref: p.Ref, Passed: passed}
	case work.Resume, work.RepairConflict, work.RepairQA, work.Observe, work.Decide, work.ContinueWorker:
		return out, fmt.Errorf("unexpected action: %s", a.Kind)
	default:
		return out, work.ErrInvalid
	}
	return out, nil
}

func (w *repairWorld) probe(ctx context.Context, revision string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url, nil)
	if err != nil {
		return 0, "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if res.Header.Get("X-Revision") != revision {
		return 0, "", fmt.Errorf("wrong deployed revision")
	}
	return res.StatusCode, string(body), err
}

func TestCoreRepairsFailedRuntimeVerification(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"probe-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := &repairWorld{root: t.TempDir()}
	service := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		revision := w.deployed
		w.mu.Unlock()
		data, err := os.ReadFile(filepath.Join(w.root, revision))
		rw.Header().Set("X-Revision", revision)
		if err != nil || string(data) != "healthy" {
			rw.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = rw.Write(data)
	}))
	defer service.Close()
	w.url = service.URL
	terminal := make(chan work.Goal, 1)
	admissions := 0
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) { w.engine = e; return w, nil },
		MainHost: func(context.Context) (work.Node, error) {
			return work.Node{ID: "probe-main", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 2, CPULimit: 2, AvailableMemoryMB: 1024, MemoryLimitMB: 1024, AvailableDiskMB: 1024, DiskLimitMB: 1024, Workers: map[string]bool{"stub": true}}, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, agent string) error {
			admissions++
			return e.Admit(ctx, work.Goal{ID: "runtime-repair", Person: person, Agent: agent, Summary: "repair the service", Done: "service returns healthy", Scope: work.GoalScope{Repositories: []string{"fixture"}, Includes: "repair the fixture service"}, ServiceTarget: w.url, Tasks: []work.Task{repairTask("initial")}, Criteria: []work.Criterion{{ID: "answers", Statement: "service returns healthy", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "service.answers", Target: w.url}}}})
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
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Core shutdown: %v", err)
		}
	})
	var g work.Goal
	select {
	case g = <-terminal:
	case err = <-done:
		done <- err
		t.Fatalf("Core stopped: %v", err)
	case <-ctx.Done():
		t.Fatal("Core did not finish the repair")
	}
	if g.State != work.GoalDone {
		t.Fatalf("Core asked the person before a repair assessment: %+v", g)
	}
	if len(g.Tasks) != 2 || g.TargetRevision != "merged-repair" || !g.Verification.Passed || !g.Deployment.Passed || len(g.Checks) != 7 {
		t.Fatalf("repair lacks completion evidence: %+v", g)
	}
	for _, check := range g.Checks {
		if !check.Passed {
			t.Fatalf("failed criterion: %+v", check)
		}
		if _, err := os.ReadFile(check.Ref); err != nil {
			t.Fatal(err)
		}
	}
	failures := 0
	for _, p := range g.Evidence {
		if !p.Passed {
			failures++
			if p.Revision != "merged-initial" {
				t.Fatalf("wrong failed revision: %+v", p)
			}
			if _, err = os.ReadFile(p.Ref); err != nil {
				t.Fatal(err)
			}
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	counts := map[work.ActionKind]int{}
	for _, a := range w.calls {
		counts[a.Kind]++
	}
	if admissions != 1 || failures != 1 || counts[work.PlanGoal] != 1 || counts[work.Assign] != 2 || counts[work.Deploy] != 2 || counts[work.Verify] != 2 {
		t.Fatalf("repair repeated or bypassed stages: admissions=%d failures=%d calls=%v", admissions, failures, counts)
	}
}
