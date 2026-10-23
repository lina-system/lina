// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2TUIWorkerQuestionNameInstalledCore(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Sion", "Cedar 한글"} {
		t.Run(name, func(t *testing.T) {
			fixture := codexWorkScenario{
				Input: "Ask the worker to prepare the artifact.", TaskPacket: "Write the assigned artifact after approval and an answer.",
				Question: "Which label should the worker use?", Answer: "Chosen label",
				Coordination: "Record the artifact for review.", Artifact: "result.txt", Result: "QUESTION_NAME_RESULT",
			}
			data, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			workdir := t.TempDir()
			finished := make(chan struct{})
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/worker-finished" {
					close(finished)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var req struct{ Input, Tools []json.RawMessage }
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if scriptedCodingManager(t, w, req) {
					return
				}
				if respondWorkerCompletion(w, req.Input, fixture.Result) {
					return
				}
				if calls.Add(1) == 1 {
					emitPersonaCall(w, "start-worker", "worker_start", map[string]any{"workdir": workdir, "task_packet": fixture.TaskPacket})
					return
				}
				emitResponse(w, "Worker started.", true)
			}))
			defer func() { server.CloseClientConnections(); server.Close() }()
			h, bootstrap := installedConversationHarness(t, server.URL)
			ids := seedCodingFixture(t, h, bootstrap)
			bin := t.TempDir()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
				t.Fatal(err)
			}
			core := h.command("core", "--state-root", h.root, "--socket", h.socket)
			core.command.Env = append(core.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=2", "CODEX_HOME=/fixture/codex-home", "LINA_WORK_TOOLS_SCENARIO="+string(data), "LINA_WORKER_BARRIER_URL="+server.URL+"/worker-finished", "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
			core.start(t)
			h.connect(core).Close()
			waitCoreReady(t, h)
			c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "identity-reader", []protocol.Capability{{Name: "agents", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			agent := ids["dev"]
			if name != "Sion" {
				op := "rename"
				if _, err := c.Agent(h.ctx, protocol.AgentRequest{Operation: &op, AgentID: &agent, Name: &name}); err != nil {
					t.Fatal(err)
				}
				frame, err := c.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				result, ok := frame.Payload.(protocol.AgentResult)
				if !ok || result.AgentID == nil || *result.AgentID != agent {
					t.Fatalf("agent rename failed: %+v", frame)
				}
			}
			terminal, _ := h.startConversationTUI(120, 36, "TERM=xterm-256color", "LC_ALL=C")
			waitComposerReady(t, h, terminal)
			// The person talks to LINA; the coding manager owns the worker.
			h.keys(terminal, fixture.Input+"\r")
			waitScreen(t, h, terminal, "Codex wants to run a command")
			// Enhanced key events keep selection and confirmation separate without sleeps.
			h.keys(terminal, "\x1b[?3u\x1b[17~")
			approval := waitScreen(t, h, terminal, design.Text(design.English, "card.confirm"))
			if screenRows(approval)[0] != name {
				t.Errorf("approval owner=%q, want %q", screenRows(approval)[0], name)
			}
			h.keys(terminal, "1")
			waitScreen(t, h, terminal, "[1 ")
			h.keys(terminal, "\r")
			waitScreen(t, h, terminal, design.Text(design.English, "resolved"))
			h.keys(terminal, "\x1b[27u")
			waitComposerReady(t, h, terminal)
			// The card row has its own label; matching just the name elsewhere on
			// Home would miss this regression because Home already knows the name.
			questionLabel := "[" + design.Text(design.English, "question") + "] " + fixture.Question
			home := waitScreen(t, h, terminal, questionLabel)
			if !strings.Contains(screenText(home), name+" "+questionLabel) || strings.Contains(screenText(home), agent) {
				t.Errorf("question row does not identify %q: %s", name, screenText(home))
			}
			waitScreen(t, h, terminal, name+": "+fixture.Question)
			h.keys(terminal, "\x1b[17~")
			question := waitScreen(t, h, terminal, "answer (text):")
			if screenRows(question)[0] != name || strings.Contains(screenText(question), agent) {
				t.Errorf("question owner=%q, want %q (agent ID %s)", screenRows(question)[0], name, agent)
			}
			h.keys(terminal, fixture.Answer)
			waitScreen(t, h, terminal, fixture.Answer)
			h.keys(terminal, "\r")
			waitScreen(t, h, terminal, design.Text(design.English, "resolved"))
			h.keys(terminal, "\x1b[27u")
			waitComposerReady(t, h, terminal)
			waitScreen(t, h, terminal, fixture.Result)
			select {
			case <-finished:
			case <-h.ctx.Done():
				t.Fatal("worker did not finish after the TUI answer")
			}
			artifact, err := os.ReadFile(filepath.Join(workdir, fixture.Artifact))
			if err != nil || string(artifact) != fixture.Result {
				t.Fatalf("worker artifact=%q: %v", artifact, err)
			}
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			c.Close()
			h.stopCore(core)
		})
	}
}
