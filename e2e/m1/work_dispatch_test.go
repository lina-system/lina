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
)

func TestM2DispatchedWorkArea(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/work-dispatch.json", false)
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
