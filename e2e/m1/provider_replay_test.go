// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Both modes invoke the installed CLI against a running Core; the loopback
// mode checks the actual wire, two tool loops and every report request in CI.
func TestM2ProviderReplay(t *testing.T) {
	t.Run("loopback", func(t *testing.T) {
		var calls atomic.Int64
		server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := int(calls.Add(1))
			round := (n-1)%6 + 1
			var request struct {
				Input, Tools []json.RawMessage
				Instructions string
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if n <= 12 && (len(request.Input) < 82 || len(request.Tools) < 40 || request.Instructions == "") {
				t.Error("fixture lost persona/history/tools")
			}
			if n <= 12 && (round == 2 || round == 5) {
				name := "skill_index"
				if round == 5 {
					name = "skill_notices"
				}
				emitPersonaCall(w, fmt.Sprint(n), name, struct{}{})
				return
			}
			if n <= 12 && (round == 3 || round == 6) {
				var result struct{ Type, Output string }
				if json.Unmarshal(request.Input[len(request.Input)-1], &result) != nil || result.Type != "function_call_output" || !strings.Contains(result.Output, `"code":"ok"`) {
					t.Error("recorded tool result did not reach provider")
				}
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":\"Public reply.\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Public reply.\"}]}}\n\n")
			fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":%d,\"input_tokens_details\":{\"cached_tokens\":70},\"output_tokens\":3}}}\n\n", 100+round)
		})))
		defer server.Close()
		h, _ := installedConversationHarness(t, server.URL)
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "LINA_QA_REAL_MODEL=1", "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		core.start(t)
		h.connect(core).Close()
		waitCoreReady(t, h)
		fixture, err := filepath.Abs("../../testdata/provider-replay/fixture.json")
		if err != nil {
			t.Fatal(err)
		}
		digest := func() [32]byte {
			var body []byte
			for _, suffix := range []string{"", "-wal"} {
				b, err := os.ReadFile(filepath.Join(h.root, "canon/canon.sqlite") + suffix)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				body = append(body, b...)
			}
			return sha256.Sum256(body)
		}
		before := digest()
		run := func(source, flag, out string) replay.ProviderReport {
			t.Helper()
			p := h.command("provider-replay", "--provider", "chatgpt", "--socket", h.socket, flag, source, "--out", out)
			p.command.Env = append(p.command.Env, "LINA_QA_REAL_MODEL=1")
			p.start(t)
			p.wait(t, 0)
			raw, err := os.ReadFile(filepath.Join(out, "report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report replay.ProviderReport
			if err = json.Unmarshal(raw, &report); err != nil {
				t.Fatal(err)
			}
			return report
		}
		var paths []string
		for arm := 0; arm < 2; arm++ {
			out := filepath.Join(h.root, fmt.Sprintf("provider-report-%d", arm))
			paths = append(paths, filepath.Join(out, "report.json"))
			report := run(fixture, "--fixture", out)
			if report.Failure != "" || report.Provider != "chatgpt" || len(report.Requests) != 6 {
				t.Fatalf("report: %+v", report)
			}
			for i, row := range report.Requests {
				if row.FirstOutputMS == nil || *row.FirstOutputMS < 0 || row.Written != nil || row.WrittenByLifetime != nil {
					t.Fatalf("missing timing or invented writes: %+v", row)
				}
				if i == 1 || i == 4 {
					if row.Input != nil || row.Read != nil || row.Plain != nil {
						t.Fatalf("unreported usage: %+v", row)
					}
					continue
				}
				if row.Input == nil || *row.Input != int64(101+i) || row.Read == nil || *row.Read != 70 || row.Plain == nil || *row.Plain != int64(31+i) {
					t.Fatalf("usage: %+v", row)
				}
			}
		}
		if before != digest() {
			t.Fatal("synthetic replay wrote canon")
		}
		p := h.command("provider-replay", "compare", paths[0], paths[1])
		p.start(t)
		p.wait(t, 0)
		if !strings.Contains(p.stdout.String(), "unknown") || !strings.Contains(p.stdout.String(), "4:2") {
			t.Fatal("comparison lost requests/unknowns")
		}
		// A normal conversation can be exported and replayed through this same
		// command, while Core rehydrates its source and checks removal/scope.
		c, conversation := conversationClient(t, h)
		admittedTurn(t, h, c, conversation, "provider-replay-export", "Give a short public greeting.")
		agent := c.Welcome.Agents[0].AgentID
		c.Close()
		export := h.command("export", "--socket", h.socket, "--agent", agent, "--conversation", conversation, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
		export.start(t)
		export.wait(t, 0)
		var exported protocol.ExportResult
		if err = json.Unmarshal(export.stdout.Bytes(), &exported); err != nil {
			t.Fatal(err)
		}
		before = digest()
		report := run(filepath.Join(exported.Directory, "episodes.jsonl"), "--episode", filepath.Join(h.root, "episode-report"))
		if len(report.Requests) != 1 || report.Failure != "" {
			t.Fatal(report)
		}
		if before != digest() {
			t.Fatal("episode replay wrote canon")
		}
		h.stopCore(core)
	})
	t.Run("real", func(t *testing.T) {
		requireRealModel(t)
		h, _ := installedConversationHarness(t, "")
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
		defer cancel()
		h.ctx = ctx
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "LINA_QA_REAL_MODEL=1")
		for _, key := range []string{"LINA_RESPONSES_BASE_URL", "LINA_RESPONSES_MODEL", "LINA_RESPONSES_API_KEY"} {
			core.command.Env = append(core.command.Env, key+"="+os.Getenv(key))
		}
		core.start(t)
		h.connect(core).Close()
		waitCoreReady(t, h)
		fixture, err := filepath.Abs("../../testdata/provider-replay/fixture.json")
		if err != nil {
			t.Fatal(err)
		}
		p := h.command("provider-replay", "--provider", "chatgpt", "--socket", h.socket, "--fixture", fixture, "--out", filepath.Join(h.root, "real-provider-report"))
		p.command.Env = append(p.command.Env, "LINA_QA_REAL_MODEL=1")
		p.start(t)
		p.waitWithin(t, 0, 10*time.Minute)
		h.stopCore(core)
	})
}
