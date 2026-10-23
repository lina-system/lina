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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestM3DelegationUnavailableLeavesConversation(t *testing.T) {
	raw, err := os.ReadFile("../m3/delegation-readiness.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Cases []string }
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, failure := range scenario.Cases {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Tools []struct{ Name string } }
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				for _, tool := range req.Tools {
					if tool.Name == "schedule_work" {
						t.Error("unavailable delegation was advertised")
					}
				}
				emitResponse(w, "Conversation remains available.", true)
			}))
			defer server.Close()
			h, _ := installedConversationHarness(t, server.URL)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			h.ctx = ctx
			node, err := exec.LookPath("node")
			if err != nil {
				t.Fatal(err)
			}
			git, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			program, err := filepath.Abs("../m3/dependency-worker.mjs")
			if err != nil {
				t.Fatal(err)
			}
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			binary := filepath.Join(h.root, "fixture-worker")
			if failure != "missing" {
				if err = os.WriteFile(binary, []byte(fmt.Sprintf("#!/bin/sh\nexec %s %s %s \"$@\"\n", quote(node), quote(program), quote(h.root))), 0700); err != nil {
					t.Fatal(err)
				}
			}
			fixture, _ := json.Marshal(map[string]string{"fail": failure})
			if err = os.WriteFile(filepath.Join(h.root, "fixture.json"), fixture, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, _ := json.Marshal(map[string]any{"codex": binary, "git": git, "gh": git, "slots": 1, "repositories": map[string]any{}})
			if err = os.WriteFile(filepath.Join(h.root, "system", "core", "delegation.json"), cfg, 0600); err != nil {
				t.Fatal(err)
			}
			core := h.command("core", "--state-root", h.root, "--socket", h.socket)
			core.command.Env = append(core.command.Env, "LINA_RESPONSES_BASE_URL="+server.URL, "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=fixture-key")
			core.start(t)
			// Optional readiness has a fifteen-second deadline, so observe
			// startup beyond that bound rather than the usual ten-second dial.
			h.connectWithin(core, 30*time.Second).Close()
			waitCoreReady(t, h)
			client, conversation := conversationClient(t, h)
			admittedTurn(t, h, client, conversation, "readiness", "Can we still talk?")
			client.Close()
			if pidRaw, err := os.ReadFile(filepath.Join(h.root, "worker.pid")); err == nil {
				pid, err := strconv.Atoi(string(pidRaw))
				if err != nil {
					t.Fatal(err)
				}
				if syscall.Kill(pid, 0) == nil {
					t.Fatal("failed optional worker process remains alive")
				}
			}
			h.stopCore(core)
		})
	}
}
