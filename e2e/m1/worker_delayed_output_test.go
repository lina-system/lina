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

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Real Core, worker tools, protocol, files and canon; synthetic worker and model.
// The fixture orders B's output, A's delayed finish, then B's finish on one pipe.
func TestM2WorkerDelayedCompletionOutput(t *testing.T) {
	dir := t.TempDir()
	const output = "The follow-up result survived."
	var readBack atomic.Bool
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if scriptedCodingManager(t, w, req) {
			return
		}
		var last struct {
			Type, Output, Content string
			CallID                string `json:"call_id"`
		}
		if len(req.Input) == 0 || json.Unmarshal(fixtureLatestInput(req.Input), &last) != nil {
			t.Error("missing model input")
			return
		}
		if last.Type == "function_call_output" {
			var result struct {
				Code   string
				Output map[string]any
			}
			if err := json.Unmarshal([]byte(last.Output), &result); err != nil || result.Code != "ok" {
				t.Errorf("worker tool failed: %s", last.Output)
				emitResponse(w, "Tool failed.", true)
				return
			}
			switch last.CallID {
			case "snapshot":
				emitPersonaCall(w, "continue", "worker_input", map[string]any{"session": result.Output["session"], "turn": result.Output["turn"], "generation": result.Output["generation"], "binding": result.Output["binding"], "text": "Write follow-up.txt and report its content."})
			case "read-result":
				if result.Output["content"] != output {
					t.Errorf("file readback lost the follow-up: %+v", result.Output)
				}
				readBack.Store(true)
				emitResponse(w, output, true)
			case "recall":
				if result.Output["live"] != false || result.Output["turn"] != "turn-2" || result.Output["output"] != output {
					t.Errorf("restart lost the recorded result: %+v", result.Output)
				}
				emitResponse(w, output, true)
			default:
				emitResponse(w, "The worker is running.", true)
			}
			return
		}
		switch {
		case strings.Contains(last.Content, "Continue the recovered worker"):
			emitPersonaCall(w, "snapshot", "worker_snapshot", map[string]any{"session": "recovery-thread"})
		case strings.Contains(last.Content, "Recall the follow-up"):
			emitPersonaCall(w, "recall", "worker_snapshot", map[string]any{"session": "recovery-thread"})
		case strings.Contains(last.Content, "turn-2"):
			if !strings.Contains(last.Content, output) {
				t.Error("follow-up completion reached the model without its output")
			}
			emitPersonaCall(w, "read-result", "worker_read_file", map[string]any{"session": "recovery-thread", "path": "follow-up.txt"})
		default:
			if respondWorkerCompletion(w, req.Input, "Recovered the first result.") {
				return
			}
			emitPersonaCall(w, "start", "worker_start", map[string]any{"workdir": dir, "task_packet": "Preserve this task's result."})
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
	if err := os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	start := func() *process {
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=3", "LINA_RECOVERY_DIR="+dir, "LINA_RECOVERY_CASE=delayed", "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		p.start(t)
		h.connect(p).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start()
	c, conv := conversationClient(t, h)
	admittedTurn(t, h, c, conv, "start", "Delegate a file task and keep its result.")
	waitRecovery(t, h, func() bool {
		raw, err := os.ReadFile(filepath.Join(dir, "recovery-result.txt"))
		return err == nil && string(raw) == "The worker preserved its work."
	})
	c.Close()
	core.killAndWait()
	if err := os.WriteFile(filepath.Join(dir, "snapshot-release"), []byte("read now"), 0600); err != nil {
		t.Fatal(err)
	}
	core = start()
	waitWorkerCanon(t, h, `SELECT count(*) FROM worker_recovery WHERE json_extract(record,'$.State')='finished'`, 1)
	waitRecovery(t, h, func() bool { return workerRecoveryReactions(t, h) == 1 })
	c, conv = conversationClient(t, h)
	admittedTurn(t, h, c, conv, "continue", "Continue the recovered worker.")
	waitRecovery(t, h, func() bool { return workerRecoveryReactions(t, h) == 2 })
	if !readBack.Load() {
		t.Error("follow-up did not read back the worker's file")
	}
	// Read the completion itself: the adapter's current snapshot still has B's
	// output even on the broken implementation and cannot prove delivery.
	db, closeDB, err := canon.OpenSQLiteInspection(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	err = db.QueryRowContext(h.ctx, `SELECT json_extract(body,'$.output') FROM conv_event WHERE scaffold_kind='worker_completion' AND json_extract(body,'$.turn')='turn-2'`).Scan(&stored)
	closeDB()
	if err != nil || stored != output {
		t.Errorf("follow-up completion lost output: got=%q err=%v", stored, err)
	}
	waitWorkerCanon(t, h, `SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion'`, 2)
	waitWorkerCanon(t, h, `SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion' AND json_extract(body,'$.turn')='turn-1' AND json_extract(body,'$.output')='Retained worker result.'`, 1)
	c.Close()
	h.stopCore(core)
	core = start()
	c, conv = conversationClient(t, h)
	admittedTurn(t, h, c, conv, "recall", "Recall the follow-up result.")
	c.Close()
	h.stopCore(core)
	if raw, err := os.ReadFile(filepath.Join(dir, "turn-count")); err != nil || string(raw) != "2" {
		t.Fatalf("unexpected worker execution: turns=%s err=%v", raw, err)
	}
	if reactions := workerRecoveryReactions(t, h); reactions != 2 {
		t.Fatalf("completion replayed after restart: reactions=%d", reactions)
	}
}
