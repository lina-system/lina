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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/protocol"
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
			var foreground atomic.Int32
			background := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Instructions string
					Tools        []struct{ Name string }
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				for _, tool := range req.Tools {
					if tool.Name == "schedule_work" || tool.Name == "refresh_work" {
						t.Error("unavailable delegation was advertised")
					}
				}
				if strings.Contains(req.Instructions, "self-contained background brief") {
					emitResponse(w, string(mustJSON(t, runs.Artifact{Body: "readiness result", Done: true})), true)
					background <- struct{}{}
					return
				}
				if foreground.Add(1) == 1 {
					emitPersonaCall(w, "background", "start_run", runs.Brief{Goal: "Check availability", Scope: "read skills", NotTouch: "writes", Done: "inventory checked", Result: "summary", Allow: []string{"skill_index"}, Checks: []runs.Check{{Name: "inventory", Kind: runs.Claimed}}})
					return
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
				if err = os.WriteFile(binary, []byte(fmt.Sprintf("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexec %s %s %s \"$@\"\n", quote(node), quote(program), quote(h.root))), 0700); err != nil {
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
			defer h.stopCore(core)
			// Optional readiness has a fifteen-second deadline, so observe
			// startup beyond that bound rather than the usual ten-second dial.
			h.connectWithin(core, 30*time.Second).Close()
			waitCoreReady(t, h)
			// The core topic must retain readiness for clients connecting after boot.
			readiness, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}, {Name: "channels", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer readiness.Close()
			topic := "core"
			if _, err = readiness.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			frame, err := readiness.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, ok := frame.Payload.(protocol.Snapshot)
			if !ok {
				t.Fatalf("no readiness snapshot: %+v", frame)
			}
			wantStep := "delegation.worker"
			if strings.HasPrefix(failure, "quota") {
				wantStep = "delegation.quota"
			}
			found := false
			for _, item := range snapshot.Items {
				if item.Kind != protocol.KindReadinessFailure {
					continue
				}
				var report protocol.ReadinessFailure
				if err := json.Unmarshal(item.Payload, &report); err != nil {
					t.Fatal(err)
				}
				if report.Step != wantStep {
					t.Fatalf("readiness step=%q, want %q", report.Step, wantStep)
				}
				found = true
			}
			if !found {
				t.Error("delegation failure missing from readiness snapshot")
			}
			operation := "list"
			if _, err = readiness.Channel(h.ctx, protocol.ChannelRequest{Operation: &operation}); err != nil {
				t.Fatal(err)
			}
			frame, err = readiness.Receive(h.ctx)
			if err != nil || frame.Kind != protocol.KindChannelResult || frame.Error != nil {
				t.Fatalf("channels unavailable: %+v %v", frame, err)
			}
			client, conversation := conversationClient(t, h)
			admittedTurn(t, h, client, conversation, "readiness", "Can we still talk?")
			client.Close()
			if failure != "missing" {
				pidRaw, err := os.ReadFile(filepath.Join(h.root, "worker.pid"))
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(pidRaw))
				if err != nil {
					t.Fatal(err)
				}
				if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
					t.Fatal("failed optional worker process remains alive")
				}
			}
			select {
			case <-background:
			case <-h.ctx.Done():
				t.Fatal("background run did not start")
			}
			running, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "running", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer running.Close()
			topic = "running"
			if _, err = running.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			for {
				frame, err := running.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(mustJSON(t, frame)), "ready_for_review") {
					break
				}
			}
			db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var recordRaw []byte
			if err := db.QueryRowContext(h.ctx, "SELECT record FROM background_runs").Scan(&recordRaw); err != nil {
				t.Fatal(err)
			}
			var record runs.Record
			if err := json.Unmarshal(recordRaw, &record); err != nil {
				t.Fatal(err)
			}
			if record.Outcome != runs.Complete || record.Artifact.Body != "readiness result" || record.Material.ID == "" {
				t.Fatalf("background result missing: %+v", record)
			}
		})
	}
}
