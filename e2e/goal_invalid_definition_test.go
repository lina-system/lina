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
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/records"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

type invalidDefinitionWorld struct {
	*codeWorld
	observations int
}

func (w *invalidDefinitionWorld) Observe(_ context.Context, a work.Action) (work.Observation, error) {
	if a.GoalID != "invalid-goal" || a.Kind != work.Assign {
		return work.Observation{}, fmt.Errorf("unexpected observation: %s", a.ID)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.observations++
	return work.Observation{Status: work.OutcomeSucceeded, Head: "retained-head", EffectsSettled: true}, nil
}

// Commit through the canonical records API while the disposable Core is down.
// The next real boot must encounter the correction before starting its runner.
func invalidateSavedGoal(t *testing.T, root string, g work.Goal) {
	t.Helper()
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "canon/canon.sqlite")
	epoch, err := canon.PeekEpoch(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: path, Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repo, err := records.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	r, rev, err := repo.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(r.Data), "\n")
	changed := false
	for i, line := range lines {
		if !strings.HasPrefix(line, "goal: ") {
			continue
		}
		var d work.Definition
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "goal: ")), &d); err != nil {
			t.Fatal(err)
		}
		d.Done = ""
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		lines[i], changed = "goal: "+string(raw), true
	}
	if !changed {
		t.Fatal("canonical goal definition missing")
	}
	if _, err := repo.Commit(t.Context(), records.Change{ID: r.ID, Base: rev, Path: r.Path, Data: []byte(strings.Join(lines, "\n"))}, records.Actor{Person: g.Person, Agent: g.Agent}); err != nil {
		t.Fatal(err)
	}
}

func TestM2InvalidGoalDoesNotStopCoreWork(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-invalid-goal-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err := os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"invalid-goal-host"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w := &invalidDefinitionWorld{codeWorld: &codeWorld{heads: map[string]string{}, merges: map[string]string{}, qa: map[string]bool{}, root: root}}
	service := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		_ = json.NewEncoder(rw).Encode(map[string]any{"Revision": w.deployed, "Healthy": w.deployed != ""})
	}))
	defer service.Close()
	w.url = service.URL
	host := func(context.Context) (work.Node, error) {
		return work.Node{ID: "invalid-goal-host", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 4, CPULimit: 4, AvailableMemoryMB: 8000, MemoryLimitMB: 8000, AvailableDiskMB: 8000, DiskLimitMB: 8000, Workers: map[string]bool{"stub": true}}, nil
	}
	phase := 0
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver: func(*work.Engine) (work.Driver, error) { return w, nil }, MainHost: host,
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			agent, err := fixtureWorkOwner(ctx, root)
			if err != nil {
				return err
			}
			if phase == 2 {
				return nil
			}
			g := work.Goal{ID: "code-goal", Person: person, Agent: agent, Summary: "complete the change", Done: "service answers", ServiceTarget: "private-service", Scope: work.GoalScope{Repositories: []string{"fixture-repo"}, Includes: "one change"}, Tasks: []work.Task{revisionTask("healthy")}, Criteria: []work.Criterion{{ID: "answers", Statement: "service answers", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "service.answers", Target: "private-service"}}}}
			if phase == 0 {
				g.ID, g.Tasks[0].ID, g.Tasks[0].Area = "invalid-goal", "outstanding", "outstanding"
			}
			if err := e.Admit(ctx, g); err != nil {
				return err
			}
			if phase != 0 {
				return nil
			}
			n, _ := host(ctx)
			if err := e.NodeChanged(ctx, n); err != nil {
				return err
			}
			if err := e.Reconcile(ctx); err != nil {
				return err
			}
			s, err := e.Snapshot(ctx)
			if err != nil || len(s.Actions) != 1 {
				return fmt.Errorf("seed assignment: %v", err)
			}
			_, err = e.Claim(ctx, s.Actions[0].ID)
			return err
		},
	}}
	runUntil := func(ready func(work.State) bool) work.State {
		t.Helper()
		seen := make(chan work.State, 1)
		cfg.Work.Changed = func(_ context.Context, s work.State) error {
			if ready(s) {
				select {
				case seen <- s:
				default:
				}
			}
			return nil
		}
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
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
		select {
		case s := <-seen:
			return s
		case err := <-done:
			joined = true
			t.Fatalf("Core stopped: %v", err)
		case <-ctx.Done():
			t.Fatal("Core did not finish unrelated work and outstanding observation")
		}
		return work.State{}
	}
	seed := runUntil(func(s work.State) bool { return len(s.Goals) == 1 })
	invalidateSavedGoal(t, root, seed.Goals[0])
	finished := func(s work.State) bool {
		if len(s.Goals) != 2 {
			return false
		}
		for _, g := range s.Goals {
			if g.ID == "invalid-goal" {
				if g.State != work.GoalBlocked || g.AttentionID == "" || !g.Tasks[0].EffectsSettled || g.Tasks[0].Head != "retained-head" {
					return false
				}
			} else if g.State != work.GoalDone || !g.Verification.Passed || len(g.Checks) != 5 {
				return false
			}
		}
		return true
	}
	phase = 1
	result := runUntil(finished)
	if result.Goals[0].ID != "code-goal" {
		t.Fatal("missing unrelated goal result")
	}
	for _, check := range result.Goals[0].Checks {
		if !check.Passed {
			t.Fatal("unrelated goal closed without evidence")
		}
		if _, err := os.Stat(check.Ref); err != nil {
			t.Fatal(err)
		}
	}
	w.mu.Lock()
	calls, observations := len(w.calls), w.observations
	w.mu.Unlock()
	if observations != 2 {
		t.Fatalf("old effect was not read with a fresh revision fence: %d", observations)
	}
	phase = 2
	runUntil(finished)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.calls) != calls || w.observations != observations {
		t.Fatal("restart repeated a finished effect")
	}
}
