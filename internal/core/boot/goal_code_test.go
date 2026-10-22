// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
