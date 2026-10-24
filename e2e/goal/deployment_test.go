// SPDX-License-Identifier: Apache-2.0
package goal_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

func TestCoreRejectsAmbiguousServiceRepository(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-deploy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"deploy-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	attempted := make(chan error, 1)
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(*work.Engine) (work.Driver, error) { return &repairWorld{}, nil },
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			agent, err := fixtureWorkOwner(ctx, root)
			if err != nil {
				return err
			}
			service, docs := repairTask("service"), repairTask("docs")
			docs.Repository = "docs"
			err = e.Admit(ctx, work.Goal{ID: "ambiguous-service", Person: person, Agent: agent, Summary: "update service and docs", Done: "service answers", Scope: work.GoalScope{Repositories: []string{"fixture", "docs"}, Includes: "service and docs"}, ServiceTarget: "private-service", Tasks: []work.Task{service, docs}, Criteria: []work.Criterion{{ID: "answers", Statement: "service answers", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "service.answers", Target: "private-service"}}}})
			attempted <- err
			return nil
		},
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case err := <-attempted:
		if !errors.Is(err, work.ErrInvalid) {
			t.Fatalf("Core admitted service without its repository binding: %v", err)
		}
	case err := <-done:
		done <- err
		t.Fatalf("Core stopped before admission: %v", err)
	case <-ctx.Done():
		t.Fatal("Core did not reach admission")
	}
}

type repositoryWorld struct {
	repairWorld
	failures chan error
}

func (w *repositoryWorld) Authorize(_ context.Context, a work.Action) error {
	if a.GoalID != "repository-service" {
		return work.ErrInvalid
	}
	return nil // Only this disposable private service is granted.
}

func (w *repositoryWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	if a.Kind == work.Assign {
		w.mu.Lock()
		w.calls = append(w.calls, a)
		w.mu.Unlock()
		value := "healthy"
		if a.TaskID == "docs" {
			value = "documentation"
		}
		head := "head-" + a.TaskID
		cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$1" > "$2"`, "worker", value, filepath.Join(w.root, head))
		if data, err := cmd.CombinedOutput(); err != nil {
			return work.Observation{}, fmt.Errorf("worker: %w: %s", err, data)
		}
		return work.Observation{Status: work.OutcomeSucceeded, Head: head, EffectsSettled: true}, nil
	}
	if a.Kind == work.Deploy || a.Kind == work.Verify {
		if a.Revision != "merged-service" {
			err := fmt.Errorf("%s selected foreign revision %s", a.Kind, a.Revision)
			w.failures <- err
			return work.Observation{Status: work.OutcomeNeedsPerson}, nil
		}
	}
	return w.repairWorld.Execute(ctx, a)
}

func TestCoreDeploysServiceRepositoryAfterAnotherRepositoryMerges(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-repositories-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"repository-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := &repositoryWorld{repairWorld: repairWorld{root: t.TempDir()}, failures: make(chan error, 1)}
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
	t.Cleanup(service.Close)
	w.url = service.URL
	terminal := make(chan work.Goal, 1)
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(e *work.Engine) (work.Driver, error) { w.engine = e; return w, nil },
		MainHost: func(context.Context) (work.Node, error) {
			return work.Node{ID: "repository-main", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 2, CPULimit: 2, AvailableMemoryMB: 1024, MemoryLimitMB: 1024, AvailableDiskMB: 1024, DiskLimitMB: 1024, Workers: map[string]bool{"stub": true}}, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			agent, err := fixtureWorkOwner(ctx, root)
			if err != nil {
				return err
			}
			service, docs := repairTask("service"), repairTask("docs")
			docs.Repository, docs.DependsOn = "docs", []string{"service"}
			g := work.Goal{ID: "repository-service", Person: person, Agent: agent, Summary: "update service and documentation", Done: "service returns healthy", Scope: work.GoalScope{Repositories: []string{"fixture", "docs"}, Includes: "service and documentation"}, ServiceTarget: w.url, Tasks: []work.Task{service, docs}, Criteria: []work.Criterion{{ID: "answers", Statement: "service returns healthy", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "service.answers", Target: w.url}}}}
			// Additive wire input lets the same scenario reproduce on the old schema.
			if err := json.Unmarshal([]byte(`{"service_repository":"fixture"}`), &g); err != nil {
				return err
			}
			return e.Admit(ctx, g)
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
	case err = <-w.failures:
		t.Fatal(err)
	case err = <-done:
		done <- nil
		t.Fatalf("Core stopped: %v", err)
	case <-ctx.Done():
		t.Fatal("Core did not finish the multi-repository goal")
	}
	if g.State != work.GoalDone || g.TargetRevision != "merged-service" || g.Deployment.Revision != "merged-service" || g.Verification.Revision != "merged-service" || len(g.Checks) != 7 {
		t.Fatalf("goal lacks service completion evidence: %+v", g)
	}
	for _, check := range g.Checks {
		want := "merged-service"
		if check.Target == "docs" {
			want = "merged-docs"
		}
		if !check.Passed || check.Revision != want {
			t.Fatalf("criterion used the wrong repository: %+v", check)
		}
		if _, err = os.ReadFile(check.Ref); err != nil {
			t.Fatal(err)
		}
	}
	status, body, err := w.probe(ctx, "merged-service")
	if err != nil || status != http.StatusOK || body != "healthy" {
		t.Fatalf("running service mismatch: %d %q %v", status, body, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var merges []string
	counts := map[work.ActionKind]int{}
	for _, a := range w.calls {
		counts[a.Kind]++
		if a.Kind == work.Merge {
			merges = append(merges, a.TaskID)
		}
	}
	if !slices.Equal(merges, []string{"service", "docs"}) || counts[work.Assign] != 2 || counts[work.RunQA] != 2 || counts[work.Deploy] != 1 || counts[work.Verify] != 1 {
		t.Fatalf("missing or repeated lifecycle stages: merges=%v counts=%v", merges, counts)
	}
}
