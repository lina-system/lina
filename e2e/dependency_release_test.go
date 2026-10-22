// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
)

func TestM3DependencyReleaseInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("m3/dependency-release.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario any
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var taskIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []json.RawMessage
			Tools []struct{ Name string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch n := calls.Add(1); n {
		case 1:
			var cycle map[string]any
			_ = json.Unmarshal(raw, &cycle)
			tasks := cycle["tasks"].([]any)
			tasks[0].(map[string]any)["depends_on"] = []string{"b"}
			emitPersonaCall(w, "cycle", "dispatch_work", cycle)
		case 2:
			var item struct{ Output string }
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			var result struct{ Code string }
			_ = json.Unmarshal([]byte(item.Output), &result)
			if result.Code != "invalid_arguments" {
				t.Errorf("Core accepted dependency cycle: %s", item.Output)
			}
			emitPersonaCall(w, "dispatch", "dispatch_work", scenario)
		case 3:
			var item struct{ Output string }
			if len(request.Input) == 0 {
				t.Error("no tool result")
				emitResponse(w, "Failed.", true)
				return
			}
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			var result struct {
				Code   string
				Output struct{ Result []string }
			}
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil || result.Code != "ok" {
				t.Errorf("dispatch result: %s %v", item.Output, err)
			}
			taskIDs = result.Output.Result
			emitResponse(w, "Work started.", true)
		case 4:
			emitPersonaCall(w, "refresh", "refresh_work", map[string]any{"tasks": taskIDs})
		default:
			emitResponse(w, "Work refreshed.", true)
		}
	}))
	defer server.Close()
	h, _ := installedConversationHarness(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	h.ctx = ctx
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	program, err := filepath.Abs("m3/dependency-worker.mjs")
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(h.root, "work", "clones", "fixture")
	if err = os.MkdirAll(clone, 0700); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(h.ctx, git, append([]string{"-C", clone}, args...)...)
		cmd.Env = []string{"HOME=" + h.root, "PATH=" + filepath.Dir(git), "GIT_CONFIG_NOSYSTEM=1"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "dev")
	runGit("config", "user.name", "Fixture")
	runGit("config", "user.email", "fixture@example.invalid")
	if err = os.WriteFile(filepath.Join(clone, "DESIGN.md"), []byte("# Design\n\n## Work\n\nProduce committed artifacts.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "fixture base")
	initialBase := runGit("rev-parse", "dev")
	remote := filepath.Join(h.root, "forge.git")
	runGit("init", "--bare", remote)
	runGit("remote", "add", "origin", remote)
	runGit("push", "origin", "dev")
	forge := filepath.Join(h.root, "forge")
	runGit("clone", "--branch", "dev", remote, forge)

	writeJSON := func(name string, value any) {
		t.Helper()
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(name, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(filepath.Join(h.root, "fixture.json"), map[string]string{"git": git, "clone": clone, "forge": forge})
	gh := filepath.Join(h.root, "fixture-gh")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err = os.WriteFile(gh, []byte(fmt.Sprintf("#!/bin/sh\nexec %s %s %s forge \"$@\"\n", quote(node), quote(program), quote(h.root))), 0700); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(h.root, "fixture-codex")
	if err = os.WriteFile(codex, []byte(fmt.Sprintf("#!/bin/sh\nexec %s %s %s \"$@\"\n", quote(node), quote(program), quote(h.root))), 0700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"codex": codex, "git": git, "gh": gh, "slots": 4, "repositories": map[string]any{"fixture/repo": map[string]string{"clone": clone, "base": "dev"}}}
	writeJSON(filepath.Join(h.root, "system", "core", "delegation.json"), config)
	start := func() *process {
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "LINA_RESPONSES_BASE_URL="+server.URL, "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=fixture-key")
		p.start(t)
		t.Cleanup(func() {
			if t.Failed() {
				p.killAndWait()
				t.Logf("Core stderr: %s", &p.stderr)
			}
		})
		h.connectWithin(p, 30*time.Second).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start()
	client, conversation := conversationClient(t, h)
	admittedTurn(t, h, client, conversation, "dispatch-input", "Complete the three related tasks.")
	if len(taskIDs) != 3 {
		t.Fatalf("no dispatched graph: %v", taskIDs)
	}
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store := work.New(db)
	await := func(id string, predicate func(work.Card) bool) work.Card {
		t.Helper()
		deadline := time.NewTimer(operationTimeout)
		defer deadline.Stop()
		retry := time.NewTicker(5 * time.Millisecond)
		defer retry.Stop()
		for {
			c, err := store.Get(h.ctx, id)
			if err == nil && predicate(c) {
				return c
			}
			select {
			case <-deadline.C:
				t.Fatalf("task state not reached: %+v %v", c, err)
			case <-retry.C:
			}
		}
	}
	a := await(taskIDs[0], func(c work.Card) bool { return c.Run != nil && !c.Active && c.TurnStatus == "completed" })
	await(taskIDs[2], func(c work.Card) bool { return c.Run != nil && !c.Active })
	b, err := store.Get(h.ctx, taskIDs[1])
	if err != nil || b.Run != nil || b.WaitReason == nil || b.WaitReason.Kind != "dependency" {
		t.Fatalf("premature dependent: %+v %v", b, err)
	}
	if !a.Scheduling.AreaHeld || a.Scheduling.Landed {
		t.Fatal("finish released write lease")
	}
	// The forge fixture reports merged only after the real repository has merged.
	runGit("-C", forge, "fetch", "origin")
	runGit("-C", forge, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "merge", "--no-ff", "origin/lina/"+a.Task.ID, "-m", "land fixture foundation")
	runGit("-C", forge, "push", "origin", "dev")
	if runGit("rev-parse", "dev") != initialBase {
		t.Fatal("forge merge changed parent clone local base")
	}
	if err = os.WriteFile(filepath.Join(h.root, a.Task.ID+".merged"), []byte("observed"), 0600); err != nil {
		t.Fatal(err)
	}
	admittedTurn(t, h, client, conversation, "refresh-input", "Observe the landed foundation.")
	b = await(taskIDs[1], func(c work.Card) bool { return c.Run != nil && !c.Active && c.TurnStatus == "completed" })
	for _, id := range taskIDs {
		dir := filepath.Join(h.root, "work", "tasks", id)
		if _, err = os.Stat(dir); err != nil {
			t.Fatal(err)
		}
		count, err := os.ReadFile(filepath.Join(h.root, id+".started"))
		if err != nil || string(count) != "1" {
			t.Fatalf("duplicate/missing worker %s: %s %v", id, count, err)
		}
	}
	result, err := os.ReadFile(filepath.Join(b.Task.Schedule.Workdir, "shared", "b.txt"))
	if err != nil || string(result) != "verified fixture result\n" {
		t.Fatalf("missing worker result: %q %v", result, err)
	}
	client.Close()
	h.stopCore(core)
	core = start()
	client, _ = conversationClient(t, h)
	after, err := store.Get(h.ctx, taskIDs[0])
	if err != nil || !after.Scheduling.Landed || after.State == "done" {
		t.Fatalf("restart lost merge or forged verdict: %+v %v", after, err)
	}
	if runGit("rev-parse", "dev") != initialBase {
		t.Fatal("scheduler rewrote parent clone local base")
	}
	client.Close()
	h.stopCore(core)
}
