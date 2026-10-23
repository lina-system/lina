// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

// A conversation delegation remains visible after the persona's turn ends.
// The fixture completes only after a second conversation steers it, so the
// running observation cannot pass or fail based on the worker's duration.
func TestM2ConversationWorkerRunningInstalledCore(t *testing.T) {
	t.Parallel()
	testConversationWorkerViews(t, false, false)
}

func TestM2ConversationWorkerProgressInstalledCore(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			testConversationWorkerViews(t, true, outcome == "failure")
		})
	}
}

func testConversationWorkerViews(t *testing.T, progress, failure bool) {
	workdir := t.TempDir()
	var calls atomic.Int32
	var control map[string]any
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if scriptedCodingManager(t, w, req) {
			return
		}
		if respondWorkerCompletion(w, req.Input, "") {
			if failure && len(req.Tools) != 0 {
				t.Error("failed worker reaction can start more work")
			}
			return
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "start-worker", "worker_start", map[string]any{"workdir": workdir, "task_packet": "hold for input"})
		case 2:
			var item struct{ Output string }
			var result struct {
				Code   string
				Output map[string]any
			}
			if err := json.Unmarshal(req.Input[len(req.Input)-1], &item); err != nil {
				t.Error(err)
			}
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil || result.Code != "ok" || result.Output["state"] != "active" {
				t.Errorf("worker_start did not start an active worker: %s (%v)", item.Output, err)
			}
			instruction := "finish running-list fixture"
			if failure {
				instruction = "fail running-list fixture"
			}
			control = map[string]any{"session": result.Output["session"], "turn": result.Output["turn"], "generation": result.Output["generation"], "binding": result.Output["binding"], "text": instruction}
			emitResponse(w, "The delegated work is running.", true)
		case 3:
			emitPersonaCall(w, "finish-worker", "worker_input", control)
		default:
			emitResponse(w, "The worker received the finish instruction.", true)
		}
	})))
	defer server.Close()
	h, bootstrap := installedConversationHarness(t, server.URL)
	seedCodingFixture(t, h, bootstrap)
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=1", "CODEX_HOME=/fixture/codex-home", "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
	core.start(t)
	h.connect(core).Close()
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "start-delegation", "Delegate a small file job.")
	if calls.Load() != 2 {
		t.Fatalf("start rounds=%d", calls.Load())
	}
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 110, 40)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "\x12") // Ctrl+R
	wait := func(predicate func(string) bool) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		screen, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool { return predicate(screenText(s)) })
		if err != nil {
			t.Fatalf("running list: %v\n%s", err, screenText(screen))
		}
		return screenText(screen)
	}
	wait(func(s string) bool {
		if !strings.Contains(s, design.Text(design.English, "running.keys")) {
			return false
		}
		for _, line := range strings.Split(s, "\n") {
			owner, tool := strings.Index(line, "Sion"), strings.Index(line, "Codex")
			if owner >= 0 && tool > owner {
				return true
			}
		}
		return false
	})
	if progress {
		h.keys(terminal, "\x1b")
		waitComposerReady(t, h, terminal)
		h.keys(terminal, "\x10") // Ctrl+P
		wait(func(s string) bool {
			return strings.Contains(s, "Delegate a small file job.") && strings.Contains(s, design.Text(design.English, "running.state.in_progress"))
		})
	}
	// Keep the panel open: completion must update the live cards, without
	// requiring Ctrl+R again or confusing an idle session with verified delivery.
	admittedTurn(t, h, c, conversation, "finish-delegation", "Finish the worker's current job.")
	receiveWorkerFollowups(t, h, c, 1)
	if progress {
		wait(func(s string) bool {
			if failure {
				return strings.Contains(s, "Delegate a small file job.") && strings.Contains(s, design.Text(design.English, "running.state.failed"))
			}
			return !strings.Contains(s, "Delegate a small file job.") && strings.Contains(s, design.Text(design.English, "work.title"))
		})
		h.keys(terminal, "\x1b")
		waitComposerReady(t, h, terminal)
		h.keys(terminal, "\x12")
	}
	wait(func(s string) bool {
		return strings.Contains(s, design.Text(design.English, "running.keys")) && (strings.Contains(s, "Codex") == failure) && (!failure || strings.Contains(s, design.Text(design.English, "running.state.failed")))
	})
	if failure {
		h.keys(terminal, "d")
		wait(func(s string) bool {
			return strings.Contains(s, design.Text(design.English, "running.keys")) && !strings.Contains(s, "Codex")
		})
	}
	if progress {
		h.keys(terminal, "\x1b")
		waitComposerReady(t, h, terminal)
		h.keys(terminal, "\x10")
		wait(func(s string) bool {
			return strings.Contains(s, design.Text(design.English, "work.title")) && !strings.Contains(s, "Delegate a small file job.")
		})
	}
	if calls.Load() != 4 {
		t.Fatalf("worker rounds=%d", calls.Load())
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	h.stopCore(core)
}
