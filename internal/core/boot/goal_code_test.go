// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/records"
	"os"
	"path/filepath"
	"testing"
	"time"

	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func TestCodeGoalConnectionsRequirePrivateOwnerFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "system"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodeConnections(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "system", "code-work.json")
	if err := os.WriteFile(path, []byte(`{"repositories":{}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodeConnections(root); err == nil {
		t.Fatal("publicly readable configuration accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodeConnections(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"repositories":{},"driver":"model-program"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodeConnections(root); err == nil {
		t.Fatal("unknown configuration accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "untrusted")
	if err := os.WriteFile(target, []byte(`{"repositories":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readCodeConnections(root); err == nil {
		t.Fatal("configuration symlink accepted")
	}
}
func TestCodeGoalProofRequiresCurrentTargetCommandAndOutput(t *testing.T) {
	d := &codeGoalDriver{runtime: &conversationRuntime{root: t.TempDir()}}
	command := codeCommand{Path: "/trusted/check"}
	a := goal.Action{ID: "action", Revision: "merged"}
	path := d.evidencePath(a, command)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	passed := true
	record := codeCheckRecord{Command: commandKey(command), Output: codeCheckOutput{Revision: a.Revision, Target: "repo", Passed: &passed, Evidence: "Read the merged file"}, ExitOK: true}
	write := func() {
		raw, _ := json.Marshal(record)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if p, err := d.readProof(path, command, "repo", a.Revision); err != nil || !p.Passed {
		t.Fatalf("valid evidence refused: %+v %v", p, err)
	}
	for _, tc := range []struct {
		command          codeCommand
		target, revision string
	}{{codeCommand{Path: "/other/check"}, "repo", a.Revision}, {command, "other", a.Revision}, {command, "repo", "new-head"}} {
		if _, err := d.readProof(path, tc.command, tc.target, tc.revision); err == nil {
			t.Fatal("stale evidence accepted")
		}
	}
	record.ExitOK = false
	write()
	if p, err := d.readProof(path, command, "repo", a.Revision); err != nil || p.Passed {
		t.Fatal("failed check accepted")
	}
	record.ExitOK = true
	record.Output.Evidence = ""
	write()
	if _, err := d.readProof(path, command, "repo", a.Revision); err == nil {
		t.Fatal("exit status without evidence accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if o, err := d.readCheck(a, command, "repo"); err != nil || o.Status != goal.OutcomeUnknown {
		t.Fatal("lost check repeated or reported as success")
	}
}
func TestCodeGoalWorkerObservationDoesNotPromoteUncertainClaims(t *testing.T) {
	record := dispatch.Record{ID: "worker", Code: "ready_for_review", PR: &antigravity.PullRequest{Head: "head"}, State: "unknown"}
	if o := goalWorkerObservation(record); o.Status != goal.OutcomeUnknown || o.EffectsSettled {
		t.Fatal("unknown worker accepted")
	}
	record.State = "running"
	if o := goalWorkerObservation(record); o.Status != goal.OutcomeRunning {
		t.Fatal("running worker accepted")
	}
	record.State = "finished"
	if o := goalWorkerObservation(record); o.Status != goal.OutcomeSucceeded || !o.EffectsSettled || o.Head != "head" {
		t.Fatal("observed delivery lost")
	}
	record.PR = nil
	if o := goalWorkerObservation(record); o.Status == goal.OutcomeSucceeded {
		t.Fatal("worker claim without observed PR accepted")
	}
}

func TestCodeGoalDeployObservationRetainsOriginalConnection(t *testing.T) {
	d := &codeGoalDriver{runtime: &conversationRuntime{root: t.TempDir()}}
	a := goal.Action{ID: "deploy", Revision: "merged"}
	repo := codeRepository{Clone: "/repo", Service: &codeService{Target: "private", Deploy: codeCommand{Path: "/deploy"}, Probe: codeCommand{Path: "/probe"}}}
	if err := d.deploymentIntent(a, repo, true); err != nil {
		t.Fatal(err)
	}
	if err := d.deploymentIntent(a, repo, false); err != nil {
		t.Fatal(err)
	}
	repo.Service.Probe.Path = "/other-host-probe"
	if err := d.deploymentIntent(a, repo, false); err == nil {
		t.Fatal("changed service settled an uncertain deployment")
	}
	if err := d.deploymentIntent(a, repo, true); err == nil {
		t.Fatal("changed configuration replaced the original intent")
	}
}

func TestCodeGoalLibraryProofNamesCurrentMerge(t *testing.T) {
	command := codeCommand{Path: "/check"}
	d := &codeGoalDriver{runtime: &conversationRuntime{root: t.TempDir()}, connections: codeConnections{Repositories: map[string]codeRepository{"repo": {QA: command}}}}
	g := goal.Goal{}
	passed := true
	for _, revision := range []string{"current", "earlier"} {
		a := goal.Action{ID: revision, Revision: revision}
		path := d.evidencePath(a, command)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(codeCheckRecord{Command: commandKey(command), ExitOK: true, Output: codeCheckOutput{Revision: revision, Target: "repo", Passed: &passed, Evidence: "Observed files"}})
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		g.Tasks = append(g.Tasks, goal.Task{Repository: "repo", QA: goal.Proof{Revision: revision, Ref: path, Passed: true}})
	}
	result, err := d.libraryProof(t.Context(), goal.State{}, g, goal.Action{Revision: "current"})
	if err != nil || result.Proof.Ref != g.Tasks[0].QA.Ref {
		t.Fatalf("current revision names another merge's evidence: %+v %v", result, err)
	}
}

func TestCodeGoalRoutesTasksAndChecksWithinServiceScope(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	agent := workAgentFixture(t, r)
	r.agent = agent
	r.conversation = "main:" + agent
	recordsRepo, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer recordsRepo.Close()
	r.records = recordsRepo
	cfg := WorkConfig{Driver: func(*goal.Engine) (goal.Driver, error) { return parkedWorkDriver{}, nil }}
	if err = r.startWork(t.Context(), &cfg); err != nil {
		t.Fatal(err)
	}
	defer r.workEngine.close(t.Context())
	task := goal.Task{ID: "docs", Repository: "docs", Base: "dev", Worker: "antigravity", Done: "Document the service", Area: "docs/*", Stage: goal.TaskReady, Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}}
	g := goal.Goal{ID: "scope", Person: r.person, Agent: agent, Conversation: r.conversation, Turn: "turn:scope", Summary: "Ship the service and docs", Done: "Service and docs work", Scope: goal.GoalScope{Repositories: []string{"code", "docs"}, Includes: "service and docs"}, ServiceTarget: "service", ServiceRepository: "code", Tasks: []goal.Task{task}, Criteria: []goal.Criterion{{ID: "docs", Statement: "Documentation checked", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "docs", Target: "docs"}}}}
	if err = r.workEngine.engine.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	d := &codeGoalDriver{runtime: r, engine: r.workEngine.engine, connections: codeConnections{Repositories: map[string]codeRepository{"code": {Clone: "/code", Base: "dev", Service: &codeService{Target: "service"}}, "docs": {Clone: "/docs", Base: "dev"}, "outside": {Clone: "/outside"}}}}
	for _, tc := range []struct {
		action goal.Action
		want   string
	}{{goal.Action{GoalID: g.ID, TaskID: task.ID, Kind: goal.Assign}, "/docs"}, {goal.Action{GoalID: g.ID, Kind: goal.CheckCriterion, Check: &goal.CriterionCheck{Method: "docs", Target: "docs"}}, "/docs"}, {goal.Action{GoalID: g.ID, Kind: goal.Deploy}, "/code"}} {
		_, _, _, selected, err := d.inputs(t.Context(), tc.action)
		if err != nil || selected.Clone != tc.want {
			t.Fatalf("incorrect registered connection: %+v %v", selected, err)
		}
	}
	if _, _, _, _, err := d.inputs(t.Context(), goal.Action{GoalID: g.ID, Kind: goal.CheckCriterion, Check: &goal.CriterionCheck{Target: "outside"}}); err == nil {
		t.Fatal("registered but out-of-scope target accepted")
	}
}

func TestCodeHostReportCannotSupplyIdentityReadinessOrLimits(t *testing.T) {
	now := time.Now()
	raw := []byte(`{"cpu_cores":1,"load_cpu":0,"available_memory_mb":512,"available_disk_mb":2048}`)
	limits := goal.Resources{CPU: 1, MemoryMB: 256, DiskMB: 1024}
	node, err := decodeCodeHost(raw, "installed-main", limits, map[string]bool{"antigravity": false}, now)
	if err != nil {
		t.Fatal(err)
	}
	if node.ID != "installed-main" || node.Workers["antigravity"] || node.CPULimit != 1 || node.MemoryLimitMB != 256 || node.DiskLimitMB != 1024 || node.ObservedAt != now.Unix() {
		t.Fatalf("report replaced Core facts: %+v", node)
	}
	for _, extra := range []string{`,"id":"foreign"`, `,"workers":{"antigravity":true}`, `,"cpu_limit":100`, `,"observed_at":99999999999`} {
		forged := append(append([]byte{}, raw[:len(raw)-1]...), []byte(extra+"}")...)
		if _, err := decodeCodeHost(forged, "installed-main", limits, nil, now); err == nil {
			t.Fatalf("report replaced installed field: %s", extra)
		}
	}
	for _, raw := range []string{`{"cpu_cores":0,"load_cpu":0,"available_memory_mb":512,"available_disk_mb":2048}`, `{"cpu_cores":1,"load_cpu":-1,"available_memory_mb":512,"available_disk_mb":2048}`, `{"cpu_cores":1,"load_cpu":0}`, `{} {}`} {
		if _, err := decodeCodeHost([]byte(raw), "installed-main", limits, nil, now); err == nil {
			t.Fatal("invalid resource observation accepted")
		}
	}
}
