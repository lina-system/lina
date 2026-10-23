// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/records"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func TestCodeGoalHostRequiresAntigravity(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(fmt.Sprint(installed), func(t *testing.T) {
			r, _, _, _ := managementFixture(t)
			r.activities = lifecycle.NewRegistry(nil)
			shell, err := exec.LookPath("sh")
			if err != nil {
				t.Fatal(err)
			}
			cfg := dispatch.Config{DB: r.db, ClaudeBinary: shell}
			if installed {
				cfg.Binary = shell
			}
			r.workerDispatch, err = dispatch.Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer r.workerDispatch.Close(context.Background())
			connections := codeConnections{
				Limits:       goal.Resources{CPU: 1, MemoryMB: 1024, DiskMB: 1024},
				HostReport:   &codeCommand{Path: shell, Args: []string{"-c", `printf '%s' '{"cpu_cores":1,"load_cpu":0,"available_memory_mb":1024,"available_disk_mb":1024}'`}},
				Repositories: map[string]codeRepository{"fixture/project": {Clone: r.root, Base: "dev", DesignRefs: []string{"design.md"}, QA: codeCommand{Path: shell}}},
			}
			raw, _ := json.Marshal(connections)
			if err = os.MkdirAll(filepath.Join(r.root, "system"), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(r.root, "system", "code-work.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			work, err := r.installedCodeWork()
			if err != nil {
				t.Fatal(err)
			}
			host, err := work.MainHost(t.Context())
			if err != nil || !host.Connected || host.Workers["antigravity"] != installed {
				t.Fatalf("Antigravity availability=%v: host=%+v err=%v", installed, host, err)
			}
		})
	}
}

func TestCodeGoalObservesWorkerAfterScopeCorrection(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	repository, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	e, err := goal.NewEngine(goal.Config{DB: r.db, Records: workDefinitions{repository}, Epoch: r.epoch})
	if err != nil {
		t.Fatal(err)
	}
	g := goal.Goal{ID: "g", Person: r.person, Agent: agent, Conversation: "conversation", Turn: "turn", Summary: "Update libraries", Done: "Library checks pass", NoServiceReason: "Libraries", Scope: goal.GoalScope{Repositories: []string{"old", "kept"}, Includes: "libraries"}, Criteria: []goal.Criterion{{ID: "checks", Statement: "Checks pass", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "test", Target: "kept"}}}, Tasks: []goal.Task{{ID: "old-task", Repository: "old", Base: "dev", Worker: "antigravity", Done: "Update old library", Area: "library/*", Stage: goal.TaskReady, Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}}}}
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), goal.Node{ID: r.main, Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 1, AvailableMemoryMB: 1024, AvailableDiskMB: 1024, CPULimit: 1, MemoryLimitMB: 1024, DiskLimitMB: 1024, Workers: map[string]bool{"antigravity": true}}); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err := e.Snapshot(t.Context())
	if err != nil || len(s.Actions) != 1 || s.Actions[0].Kind != goal.Assign {
		t.Fatalf("missing worker action: %+v %v", s, err)
	}
	a, err := e.Claim(t.Context(), s.Actions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), a.ID, r.epoch, goal.Observation{Status: goal.OutcomeUnknown}); err != nil {
		t.Fatal(err)
	}
	g = s.Goals[0]
	definitions := workDefinitions{repository}
	definition, revision, err := definitions.Read(t.Context(), g.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	corrected := bytes.Replace(definition.Data, []byte(`"repositories":["old","kept"]`), []byte(`"repositories":["kept"]`), 1)
	if bytes.Equal(corrected, definition.Data) {
		t.Fatal("scope correction did not change the definition")
	}
	_, err = definitions.Commit(t.Context(), goal.DefinitionChange{ID: definition.ID, Base: revision, Path: definition.Path, Data: corrected}, g.Person, g.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.RefreshDefinitions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.workerDispatch, err = dispatch.Open(t.Context(), dispatch.Config{DB: r.db})
	if err != nil {
		t.Fatal(err)
	}
	defer r.workerDispatch.Close(context.Background())
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(g.Turn+"\x00"+a.ID)))
	record := dispatch.Record{ID: id, Origin: goalOrigin(g), Goal: true, Announced: true, State: "finished", Code: "ready_for_review", PR: &antigravity.PullRequest{Head: "observed-head"}}
	raw, _ := json.Marshal(record)
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", id, g.Turn, "old", "finished", raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d := &codeGoalDriver{runtime: r, engine: e, connections: codeConnections{Repositories: map[string]codeRepository{"old": {Clone: r.root, Base: "dev"}}}}
	if err = d.Authorize(t.Context(), a); err == nil {
		t.Fatal("scope correction authorized another worker execution")
	}
	for _, registered := range []bool{true, false} {
		if !registered {
			delete(d.connections.Repositories, "old")
		}
		o, err := d.Observe(t.Context(), a)
		if err != nil || o.Status != goal.OutcomeSucceeded || !o.EffectsSettled || o.Head != record.PR.Head || o.Handle != id {
			t.Fatalf("retained worker could not settle (registered=%v): %+v %v", registered, o, err)
		}
	}
	o, err := d.Observe(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Report(t.Context(), a.ID, r.epoch, o); err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range s.Actions {
		if action.ObserveActionID != a.ID {
			continue
		}
		claimed, err := e.Claim(t.Context(), action.ID)
		if err != nil {
			t.Fatal(err)
		}
		source := claimed
		source.ID = claimed.ObserveActionID
		o, err := d.Observe(t.Context(), source)
		if err != nil {
			t.Fatal(err)
		}
		if err = e.Report(t.Context(), claimed.ID, r.epoch, o); err != nil {
			t.Fatal(err)
		}
	}
	if err = e.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	s, err = e.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	planned := false
	for _, action := range s.Actions {
		if action.ID == a.ID && action.State != "finished" {
			t.Fatal("settled worker kept its reservation")
		}
		planned = planned || action.Kind == goal.PlanGoal && action.State == "queued"
	}
	if !planned {
		t.Fatalf("reconciliation did not release the corrected plan: %+v", s.Actions)
	}
	r.person = "another-person"
	if _, err = d.Observe(t.Context(), a); err == nil {
		t.Fatal("foreign person observed retained work")
	}
}
