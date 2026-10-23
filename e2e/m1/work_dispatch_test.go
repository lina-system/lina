// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

func TestM2DispatchedWorkArea(t *testing.T) {
	t.Parallel()
	testAntigravityThroughInstalledCore(t, "../m2/work-dispatch.json", false)
}

// Seed the durable boundary from before shared task-branch assignment: the dev
// manager approved the original definition, but the conversation tool receipt
// has not committed. The installed Core must admit the identical call and then
// execute it, retaining the old manager arguments throughout delivery.
func seedLegacyDispatch(t *testing.T, h *harness, owner, repo, what, done string) func() {
	t.Helper()
	db, close := installedAcceptanceStore(t, h)
	defer close()
	var person, agent string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT r.person_id,f.agent_id FROM agent_first_run f JOIN agent_revisions r ON r.agent_id=f.agent_id AND r.revision=1").Scan(&person, &agent)
	}); err != nil {
		t.Fatal(err)
	}
	conversation := "main:" + agent
	turn := fmt.Sprintf("turn:input:%x", sha256.Sum256([]byte(person+"\x00"+conversation+"\x00delegate")))
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(turn+"\x00worker-start")))
	manager := "legacy-manager"
	origin := dispatch.Origin{Person: person, Agent: agent, Conversation: conversation, Turn: turn}
	definition := dispatch.Definition{Repository: repo, Base: "dev", Branch: "task-branch", What: what, Why: "Prove real worker execution", Scope: []string{"result.txt"}, NotTouch: []string{"design.md"}, Done: done, DesignRefs: []string{"design.md#result"}, WriteArea: []string{"result.txt"}}
	// This is the persisted codingAssignment wire format, including its original
	// unsuffixed arguments. Never seed a current TaskDefinition as the old record.
	assignment := mustJSON(t, map[string]any{"ID": id, "Manager": manager, "Owner": owner, "Conversation": "manager:" + manager, "Origin": origin, "Tool": "dispatch_work", "Target": repo, "Arguments": definition, "Delegated": true, "Verdict": ""})
	record := dispatch.Record{ID: id, Origin: origin, Definition: definition, Owner: owner, Manager: "manager:" + manager, State: "queued"}
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(h.ctx, "INSERT INTO coding_managers(id,person,owner,unit,conversation) VALUES(?,?,?,?,?)", manager, person, owner, repo, record.Manager); err != nil {
			return err
		}
		if _, err := tx.ExecContext(h.ctx, "INSERT INTO coding_assignments(id,manager,record) VALUES(?,?,?)", id, manager, assignment); err != nil {
			return err
		}
		for _, task := range []string{id, "coding:" + id} {
			if _, err := tx.ExecContext(h.ctx, "INSERT INTO authority_delegations(task_id,person,source,source_task,receiver,limits) VALUES(?,?,?,?,?,?)", task, person, agent, turn, owner, []byte(`{}`)); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(h.ctx, "INSERT INTO worker_dispatch(id,turn_id,repository,state,record) VALUES(?,?,?,?,?)", id, turn, repo, record.State, mustJSON(t, record))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return func() {
		db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close(context.Background())
		var raw []byte
		var committed int
		if err = db.Read(h.ctx, func(tx *canon.Tx) error {
			if err := tx.QueryRowContext(h.ctx, "SELECT record FROM coding_assignments WHERE id=?", id).Scan(&raw); err != nil {
				return err
			}
			return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM turn_calls WHERE turn_id=? AND json_extract(result,'$.code')='ok' AND json_extract(result,'$.output.id')=?", turn, id).Scan(&committed)
		}); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Arguments                    dispatch.Definition
			Owner, Conversation, Verdict string
		}
		if err = json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got.Arguments, definition) || got.Owner != owner || got.Conversation != record.Manager || got.Verdict != "verified" || committed != 1 {
			t.Fatalf("legacy manager definition/receipt changed: %+v committed=%d err=%v", got, committed, err)
		}
	}
}

func assertDispatchedWorkArea(t *testing.T, h *harness, fixture, source, workdir, base string) {
	t.Helper()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if filepath.Dir(workdir) != filepath.Join(h.root, "work", "tasks") {
		t.Fatalf("dispatched worker bypassed work/tasks: %s", workdir)
	}
	id := filepath.Base(workdir)
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	service := work.New(db)
	card, err := service.Get(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if card.Task.Workspace == nil || card.Task.Workspace.Path != workdir || card.Task.Workspace.Spec.Base != base || card.Active || card.Receipt == nil || card.Run == nil || card.TurnStatus != "completed" {
		t.Fatalf("dispatch did not record the allocation and terminal receipt: %+v", card)
	}
	w := card.Task.Workspace
	clone := filepath.Join(h.root, "work", "clones", w.Spec.RepositoryID)
	if git(workdir, "rev-parse", "--path-format=absolute", "--git-common-dir") != clone || git(clone, "rev-parse", "refs/lina/base/"+id) != base {
		t.Fatal("worker did not use the pinned independent parent clone")
	}
	if git(workdir, "remote", "get-url", "origin") != "https://github.com/fixture/project.git" || git(workdir, "status", "--porcelain") != "" {
		t.Fatal("worker lost its publishing remote or observation dirtied its result")
	}
	if strings.Count(git(source, "worktree", "list", "--porcelain"), "worktree ") != 1 || git(source, "status", "--porcelain") != "" {
		t.Fatal("Core changed the source checkout")
	}
	if git(workdir, "branch", "--show-current") != w.Spec.Branch || !strings.Contains(w.Spec.Branch, id) {
		t.Fatal("worker did not create its assigned task branch")
	}
	var allocation struct {
		Base, Branch        string
		Detached, Protected bool
	}
	raw, err := os.ReadFile(filepath.Join(fixture, "allocation.json"))
	if err != nil || json.Unmarshal(raw, &allocation) != nil || allocation.Base != base || !allocation.Detached || !allocation.Protected || allocation.Branch != w.Spec.Branch {
		t.Fatalf("worker allocation/protected-ref observations: %s %v", raw, err)
	}
	for _, a := range card.Receipt.Manifest {
		body, err := os.ReadFile(a.Path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(body)) != a.SHA256 {
			t.Fatal("receipt lost observed artifact", err)
		}
	}
	if err = work.ValidateWorkerReceipt(card, *card.Receipt); err != nil {
		t.Fatal(err)
	}
	missing := *card.Receipt
	missing.Provenance = nil
	if work.ValidateWorkerReceipt(card, missing) == nil {
		t.Fatal("dispatched receipt accepted without provenance")
	}
	ledger, err := service.Ledger(h.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := work.Project(ledger)
	if err != nil || !reflect.DeepEqual(card, projected) {
		t.Fatal("dispatched live card differs from ledger replay", err)
	}
}
