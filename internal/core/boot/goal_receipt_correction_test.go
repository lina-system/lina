// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ghclient"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/review"
	"github.com/thisisjun786/lina/internal/core/work"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func TestCodeGoalInspectsReplacementWorkerReceipt(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	repository, err := records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	definitions := workDefinitions{repository}
	e, err := goal.NewEngine(goal.Config{DB: r.db, Records: definitions, Epoch: r.epoch})
	if err != nil {
		t.Fatal(err)
	}
	r.workerDispatch, err = dispatch.Open(t.Context(), dispatch.Config{DB: r.db})
	if err != nil {
		t.Fatal(err)
	}
	defer r.workerDispatch.Close(context.Background())
	clone := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base")
	git("remote", "add", "origin", "https://github.com/fixture/project.git")
	baseline := git("rev-parse", "HEAD")
	task := goal.Task{ID: "same-id", Repository: "fixture/project", Base: "dev", Worker: "antigravity", Done: "old instructions", Area: "src/*", Stage: goal.TaskReady, Resources: goal.Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}}
	g := goal.Goal{ID: "g", Person: r.person, Agent: agent, Conversation: "conversation", Turn: "turn", Summary: "Update library", Done: "Checks pass", NoServiceReason: "Library", Scope: goal.GoalScope{Repositories: []string{task.Repository}, Includes: "library"}, Tasks: []goal.Task{task}, Criteria: []goal.Criterion{{ID: "check", Statement: "Checks pass", Check: goal.CriterionCheck{Kind: goal.RunEvidence, Method: "test", Target: task.Repository}}}}
	if err = e.Admit(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	if err = e.NodeChanged(t.Context(), goal.Node{ID: r.main, Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 1, CPULimit: 1, AvailableMemoryMB: 1024, MemoryLimitMB: 1024, AvailableDiskMB: 1024, DiskLimitMB: 1024, Workers: map[string]bool{"antigravity": true}}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() goal.State {
		t.Helper()
		s, err := e.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	claim := func(kind goal.ActionKind) goal.Action {
		t.Helper()
		if err := e.Reconcile(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, a := range snapshot().Actions {
			if a.Kind == kind && a.State == "queued" {
				a, err := e.Claim(t.Context(), a.ID)
				if err != nil {
					t.Fatal(err)
				}
				return a
			}
		}
		t.Fatalf("missing %s", kind)
		return goal.Action{}
	}
	d := &codeGoalDriver{runtime: r, engine: e, connections: codeConnections{Repositories: map[string]codeRepository{task.Repository: {Clone: clone, Base: task.Base}}}}
	complete := func(a goal.Action, head string) dispatch.Record {
		t.Helper()
		// The synthetic worker delivers through the persisted dispatch boundary;
		// Core observes it via the production driver and accepts its actual action.
		id := fmt.Sprintf("%x", sha256.Sum256([]byte(g.Turn+"\x00"+a.ID)))
		record := dispatch.Record{ID: id, Origin: goalOrigin(g), Goal: true, Announced: true, State: "finished", Code: "ready_for_review", Definition: dispatch.Definition{Repository: clone, Base: "dev", Branch: "lina/" + a.ID}, Protected: map[string]string{"refs/heads/dev": baseline}, PR: &antigravity.PullRequest{Head: head, Number: 2, URL: "https://github.com/fixture/project/pull/2"}}
		raw, _ := json.Marshal(record)
		if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
			_, err := tx.ExecContext(t.Context(), "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", id, g.Turn, clone, "finished", raw)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		o, err := d.Observe(t.Context(), a)
		if err != nil {
			t.Fatal(err)
		}
		if err = e.Report(t.Context(), a.ID, a.Epoch, o); err != nil {
			t.Fatal(err)
		}
		return record
	}
	correct := func(instructions string) {
		t.Helper()
		current := snapshot().Goals[0]
		record, rev, err := definitions.Read(t.Context(), current.RecordID)
		if err != nil {
			t.Fatal(err)
		}
		data := append(record.Data, []byte("\n"+instructions+"\n")...)
		if _, err = definitions.Commit(t.Context(), goal.DefinitionChange{ID: record.ID, Path: record.Path, Base: rev, Data: data}, g.Person, g.Agent); err != nil {
			t.Fatal(err)
		}
		manager := claim(goal.PlanGoal)
		task.Done = instructions
		if err = e.Report(t.Context(), manager.ID, manager.Epoch, goal.Observation{Status: goal.OutcomeSucceeded, Tasks: []goal.Task{task}}); err != nil {
			t.Fatal(err)
		}
	}
	old := claim(goal.Assign)
	complete(old, strings.Repeat("a", 40))
	correct("revised instructions")
	replacement := claim(goal.Assign)
	delivered := complete(replacement, strings.Repeat("b", 40))
	// Exercise both action orders, independent of the journal's hashed IDs.
	s := snapshot()
	current := s.Goals[0]
	slices.SortStableFunc(s.Actions, func(a, b goal.Action) int {
		if a.ID == old.ID {
			return -1
		}
		if b.ID == old.ID {
			return 1
		}
		return 0
	})
	selected, err := r.goalTaskReceipt(t.Context(), s, current, current.Tasks[0])
	if err != nil || selected.ID != delivered.ID {
		t.Fatalf("replacement PR inspection selected obsolete receipt: old attempt=%d new attempt=%d selected=%s want=%s err=%v", old.Attempt, replacement.Attempt, selected.ID, delivered.ID, err)
	}
	if replacement.Attempt <= old.Attempt {
		t.Fatalf("replacement reused executed attempt %d", replacement.Attempt)
	}
	var accepted goal.Action
	for _, a := range s.Actions {
		if a.ID == replacement.ID {
			accepted = a
		}
	}
	for _, excluded := range []string{"superseded", "unaccepted", "unfenced"} {
		t.Run(excluded, func(t *testing.T) {
			decoy := accepted
			decoy.ID = old.ID
			switch excluded {
			case "superseded":
				decoy.Superseded = true
			case "unaccepted":
				decoy.State = "running"
			case "unfenced":
				decoy.InputVersion = nil
			}
			probe := s
			probe.Actions = []goal.Action{decoy, accepted}
			record, err := r.goalTaskReceipt(t.Context(), probe, current, current.Tasks[0])
			if err != nil || record.ID != delivered.ID {
				t.Fatalf("selected %s worker history: %s %v", excluded, record.ID, err)
			}
		})
	}
	// A fresh observation accepts the original effect without dispatching it
	// again. Lookup must follow ObserveActionID, not the obsolete source token.
	observed := accepted
	observed.ID, observed.ObserveActionID = "accepted-read", accepted.ID
	accepted.Superseded = true
	probe := s
	probe.Actions = []goal.Action{accepted, observed}
	if record, err := r.goalTaskReceipt(t.Context(), probe, current, current.Tasks[0]); err != nil || record.ID != delivered.ID {
		t.Fatalf("lost reobserved worker receipt: %s %v", record.ID, err)
	}

	pr := antigravity.PullRequest{Number: 2, URL: delivered.PR.URL, State: "OPEN", Branch: delivered.Definition.Branch, Base: "dev", Head: delivered.PR.Head, Checks: []antigravity.Check{{Type: "CheckRun", Status: "COMPLETED", Conclusion: "SUCCESS"}}, Reviews: []antigravity.Review{{State: "APPROVED"}}}
	pr.Reviews[0].Commit.OID = pr.Head
	raw, _ := json.Marshal(pr)
	if err = os.WriteFile(filepath.Join(r.root, "pr.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	gh := filepath.Join(r.root, "gh-fixture")
	script := `#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
case "$1" in
pr) test "$3" = "$EXPECTED_BRANCH" || exit 9; cat "$PR_FIXTURE" ;;
api) printf '[[]]\n' ;;
*) exit 10 ;;
esac
`
	if err = os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	r.workerGitHub, err = ghclient.New(t.Context(), ghclient.Config{Path: gh, Leaser: lifecycle.NewRegistry(nil), Env: append(os.Environ(), "EXPECTED_BRANCH="+pr.Branch, "PR_FIXTURE="+filepath.Join(r.root, "pr.json"))})
	if err != nil {
		t.Fatal(err)
	}
	defer r.workerGitHub.Close(context.Background())
	// Seed a previously accepted head through the work receipt and review
	// boundaries; an unbound legacy "verified" string is not merge evidence.
	assignment := codingAssignment{ID: delivered.ID, Manager: "manager", Owner: g.Agent, Origin: goalOrigin(g), Verdict: "verified", VerdictHead: pr.Head}
	r.work = work.New(r.db)
	workTask := work.Task{ID: delivered.ID, PersonID: g.Person, AgentID: g.Agent, ConversationID: g.Conversation, Title: "Accepted replacement", Done: task.Done, ArtifactRoots: []string{clone}, RequiresApply: true}
	if err = r.work.Create(t.Context(), workTask); err != nil {
		t.Fatal(err)
	}
	run := work.Run{ID: delivered.ID + ":run", TaskID: delivered.ID, Generation: 1, Worker: "fixture", ThreadID: "thread", TurnID: "turn", NodeID: r.main}
	if err = r.work.Start(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	artifact := filepath.Join(clone, "result.txt")
	hash := strings.Repeat("a", 64)
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: delivered.ID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now, Manifest: []work.Artifact{{Path: artifact, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: "result", TaskID: delivered.ID, NodeID: r.main, Path: artifact, SHA256: hash, ObservedAt: now}}}
	receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
	receipt.EventID = work.ReceiptID(receipt)
	if err = r.work.Submit(t.Context(), run.ID, receipt); err != nil {
		t.Fatal(err)
	}
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		return work.FinishIn(t.Context(), tx, delivered.ID, run.ID, "completed", pr.Head)
	}); err != nil {
		t.Fatal(err)
	}
	judgment := review.Record{Key: review.Key{Repository: "fixture/project", Number: pr.Number, Head: pr.Head}, Task: delivered.ID, Receipt: receipt.EventID, Generation: 1}
	if err = review.New(r.db).Claim(t.Context(), judgment); err != nil {
		t.Fatal(err)
	}
	judgment.State = "complete"
	judgment.Verdict = "verified"
	judgment.Evidence = "Accepted replacement matches the definition."
	assignment.VerdictReceipt = receipt.EventID
	raw, _ = json.Marshal(assignment)
	if err = r.db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "INSERT INTO coding_managers(id,person,owner,unit,conversation) VALUES(?,?,?,?,?)", "manager", g.Person, g.Agent, clone, g.Conversation); err != nil {
			return err
		}
		if err := review.FinishIn(t.Context(), tx, judgment); err != nil {
			return err
		}
		if err := work.ConformanceIn(t.Context(), tx, judgment.Task, judgment.Receipt, judgment.Head, judgment.Verdict, judgment.Evidence); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "INSERT INTO coding_assignments(id,manager,record) VALUES(?,?,?)", assignment.ID, assignment.Manager, raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inspect := func() {
		t.Helper()
		a := claim(goal.InspectPR)
		o, err := d.Execute(t.Context(), a)
		if err != nil || o.PR.Head != delivered.PR.Head || !o.PR.Verified.Passed {
			t.Fatalf("Core did not inspect replacement PR: %+v %v", o, err)
		}
		if err = e.Report(t.Context(), a.ID, a.Epoch, o); err != nil {
			t.Fatal(err)
		}
	}
	inspect()
	// An unrelated definition revision retains this accepted result and receipt.
	correct("revised instructions")
	if err = e.Signal(t.Context(), goal.Signal{ID: "inspect-retained-result", GoalID: g.ID, TaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	inspect()
}
