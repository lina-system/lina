// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM3AntigravityThroughInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("m3/antigravity.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases      []struct{ Name, Expected string }
		What, Done string
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, test := range scenario.Cases {
		t.Run(test.Name, func(t *testing.T) {
			t.Parallel()
			var err error
			fixture := t.TempDir()
			repo := filepath.Join(fixture, "repo")
			bin := filepath.Join(fixture, "bin")
			for _, dir := range []string{repo, bin} {
				if err = os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
				cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + fixture, "GIT_CONFIG_NOSYSTEM=1"}
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git: %s %v", out, err)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-b", "dev")
			os.WriteFile(filepath.Join(repo, "design.md"), []byte("# Result\nProduce result.txt.\n"), 0600)
			git("add", "design.md")
			git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Initial design")
			base := git("rev-parse", "dev")
			git("remote", "add", "origin", "https://github.com/fixture/project.git")
			script, err := os.ReadFile("m3/antigravity-worker.mjs")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"agy", "gh"} {
				if err = os.WriteFile(filepath.Join(bin, name), script, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			var offered atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Tools []struct{ Name string }
					Input []json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if calls.Add(1) == 1 {
					for _, tool := range req.Tools {
						if tool.Name == "dispatch_work" {
							offered.Store(true)
						}
					}
					if !offered.Load() {
						t.Error("running Core does not offer dispatch_work with installed agy")
						emitResponse(w, "Unavailable.", true)
						return
					}
					emitPersonaCall(w, "worker-start", "dispatch_work", map[string]any{"repository": repo, "base": "dev", "branch": "task-branch", "what": scenario.What, "why": "Prove real worker execution", "scope": []string{"result.txt"}, "not_touch": []string{"design.md"}, "done": scenario.Done, "design_refs": []string{"design.md#result"}, "write_area": []string{"result.txt"}})
				} else {
					emitResponse(w, "Task accepted.", true)
				}
			}))
			defer server.Close()
			h, _ := installedConversationHarness(t, server.URL)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			t.Cleanup(cancel)
			h.ctx = ctx
			start := func() *process {
				p := h.command("core", "--state-root", h.root, "--socket", h.socket)
				p.command.Env = append(p.command.Env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "LINA_WORKER_FIXTURE="+fixture, "LINA_WORKER_CASE="+test.Name, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
				p.start(t)
				h.connectWithin(p, time.Minute).Close()
				waitCoreReady(t, h)
				return p
			}
			core := start()
			c, conversation := conversationClient(t, h)
			events := admittedTurn(t, h, c, conversation, "delegate", "Create result.txt through the installed worker and submit its PR.")
			if !offered.Load() {
				c.Close()
				h.stopCore(core)
				return
			}
			found := false
			inspect := func(e protocol.Envelope) {
				if item, ok := e.Payload.(protocol.ItemDone); ok && item.Text != nil && strings.Contains(*item.Text, "antigravity") {
					if !strings.Contains(*item.Text, test.Expected) {
						t.Fatalf("unexpected worker result: %s", *item.Text)
					}
					found = true
				}
			}
			for _, e := range events {
				inspect(e)
			}
			for !found {
				e, err := c.Receive(h.ctx)
				if err != nil {
					t.Fatal("worker result missing", err)
				}
				inspect(e)
			}
			c.Close()
			h.stopCore(core)
			if git("rev-parse", "dev") != base {
				t.Fatal("worker changed protected base")
			}
			if test.Name == "success" || test.Name == "pending" {
				wd, err := os.ReadFile(filepath.Join(fixture, "workdir"))
				if err != nil {
					t.Fatal(err)
				}
				body, err := os.ReadFile(filepath.Join(string(wd), "result.txt"))
				if err != nil || !strings.Contains(string(body), "worker produced") {
					t.Fatal("no real worker artifact", err)
				}
				if _, err = os.Stat(filepath.Join(fixture, "created")); err != nil {
					t.Fatal("worker did not open PR")
				}
			}
			core = start()
			c, _ = conversationClient(t, h)
			topic := "conversation:" + conversation
			if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			e, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			saved, _ := json.Marshal(e.Payload)
			if !strings.Contains(string(saved), test.Expected) {
				t.Fatalf("durable result lost: %s", saved)
			}
			c.Close()
			h.stopCore(core)
			if calls.Load() != 2 {
				t.Fatalf("restart replayed provider: %d", calls.Load())
			}
		})
	}
}
