// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/work"
)

func TestM2WorkArea(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../m2/work-area.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		TaskID              string `json:"task_id"`
		Title, Done, Output string
		Tools               []string
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}

	source := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append([]string{"PATH=" + os.Getenv("PATH")}, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git(source, "init", "-b", "main")
	if err = os.WriteFile(filepath.Join(source, "base.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	git(source, "add", ".")
	git(source, "commit", "-m", "base")
	base := git(source, "rev-parse", "HEAD")
	branch := "task/" + scenario.TaskID
	workspace := make(chan work.Workspace, 1)
	results := make(chan []work.Material, 2)
	var receiptID, head string
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		names := map[string]bool{}
		for _, raw := range request.Tools {
			var d struct{ Name string }
			_ = json.Unmarshal(raw, &d)
			names[d.Name] = true
		}
		for _, name := range scenario.Tools {
			if !names[name] {
				t.Errorf("running Core missing tool %s", name)
				emitResponse(w, "Unavailable.", true)
				return
			}
		}
		count++
		switch count {
		case 1:
			emitPersonaCall(w, "prepare", "prepare_work", map[string]any{"task_id": scenario.TaskID, "title": scenario.Title, "done": scenario.Done, "workspace": work.WorkspaceSpec{RepositoryID: "fixture", Source: source, Base: base, Branch: branch}, "requires_apply": true})
			return
		case 3, 5:
			emitPersonaCall(w, fmt.Sprint("retain-", count), "retain_work_result", map[string]any{"task_id": scenario.TaskID, "receipt_id": receiptID, "head": head})
			return
		}
		var item struct{ Output string }
		var result struct {
			Code   string
			Output json.RawMessage
		}
		_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
		_ = json.Unmarshal([]byte(item.Output), &result)
		if result.Code != "ok" {
			t.Errorf("work tool: %s", item.Output)
		} else if count == 2 {
			var allocated work.Workspace
			_ = json.Unmarshal(result.Output, &allocated)
			workspace <- allocated
		} else {
			var saved []work.Material
			_ = json.Unmarshal(result.Output, &saved)
			results <- saved
		}
		emitResponse(w, "Recorded.", true)
	}))
	defer server.Close()
	h, _ := installedConversationHarness(t, server.URL)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	start := func(_ bool) *process {
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "PATH="+filepath.Dir(gitPath), "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		p.start(t)
		h.connect(p).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "prepare-area", "Prepare the task workspace.")
	var allocated work.Workspace
	select {
	case allocated = <-workspace:
	default:
		t.Fatal("no work allocation")
	}
	if allocated.Path != filepath.Join(h.root, "work/tasks", scenario.TaskID) || git(allocated.Path, "rev-parse", "HEAD") != base {
		t.Fatal("Core did not allocate the pinned worktree")
	}
	// A deterministic worker subprocess produces and commits real output in the
	// returned directory. Vendor selection/dispatch is outside this issue.
	worker := exec.Command("sh", "-c", `git switch -c "$BRANCH" && printf '%s' "$RESULT" > report.txt && git add report.txt && git -c user.name=Fixture -c user.email=fixture@example.invalid commit -m result`)
	worker.Dir = allocated.Path
	worker.Env = append([]string{"PATH=" + os.Getenv("PATH")}, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "BRANCH="+branch, "RESULT="+scenario.Output)
	if b, e := worker.CombinedOutput(); e != nil {
		t.Fatalf("fixture worker: %v %s", e, b)
	}
	head = git(allocated.Path, "rev-parse", "HEAD")
	rejected := exec.Command("git", "update-ref", "refs/heads/main", head)
	rejected.Dir = allocated.Path
	if b, e := rejected.CombinedOutput(); e == nil {
		t.Fatalf("worker moved protected main: %s", b)
	}
	c.Close()
	h.stopCore(core)
	// Supply only adapter-observed lifecycle facts while Core is stopped. The
	// task/worktree were created by Core; Core must retain and back up the result.
	ctl, e := ctlfiles.Open(h.root)
	if e != nil {
		t.Fatal(e)
	}
	defer ctl.Close()
	if e = ctl.Acquire(); e != nil {
		t.Fatal(e)
	}
	dbpath := filepath.Join(h.root, "canon/canon.sqlite")
	_, epoch, e := canon.Inspect(h.ctx, dbpath)
	if e != nil {
		t.Fatal(e)
	}
	db, e := canon.Open(h.ctx, canon.Config{Path: dbpath, Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close(context.Background())
	service := work.New(db)
	run := work.Run{ID: "fixture-run", TaskID: scenario.TaskID, Generation: 1, Worker: "fixture", ThreadID: "fixture-thread", TurnID: "fixture-turn", NodeID: "fixture-node"}
	if e = service.Start(h.ctx, run); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(allocated.Path, "report.txt")
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(scenario.Output)))
	now := time.Now().UTC()
	receipt := work.Receipt{SchemaVersion: 1, RelationshipID: scenario.TaskID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now, Manifest: []work.Artifact{{Path: file, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: "fixture-output", TaskID: scenario.TaskID, NodeID: run.NodeID, Path: file, SHA256: hash, ObservedAt: now}}}
	receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
	receipt.EventID = work.ReceiptID(receipt)
	receiptID = receipt.EventID
	if e = service.Submit(h.ctx, run.ID, receipt); e != nil {
		t.Fatal(e)
	}
	if e = service.Finish(h.ctx, run.TaskID, run.ID, "completed"); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(h.ctx); e != nil {
		t.Fatal(e)
	}
	if e = ctl.Close(); e != nil {
		t.Fatal(e)
	}
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "retain-area", "Preserve the completed local result without applying it.")
	var saved []work.Material
	select {
	case saved = <-results:
	default:
		t.Fatal("no retained result")
	}
	if len(saved) != 2 {
		t.Fatalf("need file and bundle: %+v", saved)
	}
	c.Close()
	h.stopCore(core)
	if e = os.RemoveAll(filepath.Join(h.root, "work")); e != nil {
		t.Fatal(e)
	}
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "repeat-retain", "Read the saved result again.")
	select {
	case again := <-results:
		if !reflect.DeepEqual(saved, again) {
			t.Fatal("retry replaced retained result")
		}
	default:
		t.Fatal("retention retry needed lost work area")
	}
	c.Close()
	h.stopCore(core)
	backupProcess := h.command("backup", "--state-root", h.root)
	backupProcess.start(t)
	backupProcess.wait(t, 0)
	entries, e := os.ReadDir(filepath.Join(h.root, "backups"))
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		generation := filepath.Join(h.root, "backups", entry.Name())
		manifest, e := backup.Verify(h.ctx, generation)
		if e != nil {
			t.Fatal(e)
		}
		reader, e := canon.OpenReader(h.ctx, filepath.Join(generation, "canon/canon.sqlite"))
		if e != nil {
			t.Fatal(e)
		}
		card, e := work.New(reader).Get(h.ctx, scenario.TaskID)
		reader.Close(context.Background())
		if e != nil || !reflect.DeepEqual(card.Materials, saved) || card.Applied || card.Verdict != "" || card.State == "done" {
			t.Fatalf("backup task: %+v %v", card, e)
		}
		for _, m := range saved {
			var path string
			for _, a := range manifest.Assets {
				if a.AssetID == m.ID && a.Revision == m.Revision {
					path = filepath.Join(generation, a.Path)
				}
			}
			if path == "" {
				t.Fatal("retained result absent from generation")
			}
			if strings.HasSuffix(m.Path, "branch.bundle") {
				clone := t.TempDir()
				git(clone, "clone", "-b", branch, path, "copy")
				if git(filepath.Join(clone, "copy"), "rev-parse", "HEAD") != head {
					t.Fatal("lost branch history")
				}
			} else {
				body, e := os.ReadFile(path)
				if e != nil || string(body) != scenario.Output {
					t.Fatal("lost result bytes")
				}
			}
		}
		if _, e = os.Stat(filepath.Join(generation, "work")); !os.IsNotExist(e) {
			t.Fatal("work area leaked into backup")
		}
		found = true
	}
	if !found {
		t.Fatal("no verified generation")
	}
}
