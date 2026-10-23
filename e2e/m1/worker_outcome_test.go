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
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The provider only reports a checked result after Core returns actual file bytes.
// Cover both normal delivery and restart between admission and the follow-up.
func TestM2InstalledWorkerOutcome(t *testing.T) {
	t.Parallel()
	t.Run("after-delivery", func(t *testing.T) { installedWorkerOutcome(t, false) })
	t.Run("pending-at-restart", func(t *testing.T) { installedWorkerOutcome(t, true) })
}

func installedWorkerOutcome(t *testing.T, pendingAtRestart bool) {
	workdir := t.TempDir()
	initialWaiting := make(chan struct{})
	release := make(chan struct{})
	var launches atomic.Int32
	gate := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		launches.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})))
	defer gate.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var restarted, readBack atomic.Bool
	var followups atomic.Int32
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if scriptedCodingManager(t, w, req) {
			return
		}
		raw := string(mustJSON(t, req.Input))
		var last struct {
			Type, Output, Content string
			CallID                string `json:"call_id"`
		}
		json.Unmarshal(req.Input[len(req.Input)-1], &last)
		if restarted.Load() {
			if !strings.Contains(raw, "LINA_WORKER_RESULT") {
				t.Error("restart model context lost the recorded worker outcome")
				emitResponse(w, "unknown", true)
				return
			}
			if last.Type != "function_call_output" {
				emitPersonaCall(w, "recorded-worker-result", "worker_snapshot", map[string]any{"session": "fixture-thread"})
				return
			}
			var result struct {
				Code   string
				Output struct {
					Live           bool
					Status, Output string
				}
			}
			if err := json.Unmarshal([]byte(last.Output), &result); err != nil || result.Code != "ok" || result.Output.Live || result.Output.Status != "done" || !strings.Contains(result.Output.Output, "LINA_WORKER_RESULT") {
				t.Errorf("recorded snapshot unavailable after restart: %s", last.Output)
				emitResponse(w, "unknown", true)
				return
			}
			emitResponse(w, "Recorded outcome: LINA_WORKER_RESULT", true)
			return
		}
		if last.Type == "function_call_output" {
			if last.CallID == "read-worker-result" {
				var result struct {
					Code   string
					Output struct{ Content string }
				}
				if err := json.Unmarshal([]byte(last.Output), &result); err != nil || result.Code != "ok" || string(result.Output.Content) != "LINA_WORKER_RESULT" {
					t.Errorf("read-back did not return the artifact bytes: %s", last.Output)
					emitResponse(w, "read failed", true)
					return
				}
				readBack.Store(true)
				emitResponse(w, "Checked result: LINA_WORKER_RESULT", true)
			} else {
				if pendingAtRestart {
					close(initialWaiting)
					<-r.Context().Done()
					return
				}
				emitResponse(w, "Delegated; I will check the file when it finishes.", true)
			}
			return
		}
		if strings.Contains(last.Content, "LINA_WORKER_RESULT") {
			followups.Add(1)
			emitPersonaCall(w, "read-worker-result", "worker_read_file", map[string]any{"session": "fixture-thread", "path": "result.txt"})
			return
		}
		emitPersonaCall(w, "start-worker", "worker_start", map[string]any{"workdir": workdir, "task_packet": "Create result.txt with LINA_WORKER_RESULT; report its path."})
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
	start := func() *process {
		p := h.command("core", "--state-root", h.root, "--socket", h.socket)
		p.command.Env = append(p.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=1", "LINA_WORKER_FINISH_GATE="+gate.URL, "CODEX_HOME=/fixture/codex-home", "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		p.start(t)
		h.connect(p).Close()
		waitCoreReady(t, h)
		return p
	}
	core := start()
	c, conversation := conversationClient(t, h)
	voiced := 0
	const inputText = "Create result.txt and check its content after the worker finishes."
	if pendingAtRestart {
		inputID, text := "delegate", inputText
		if _, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &inputID, Text: &text}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-initialWaiting:
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		}
		close(release)
		// The ongoing initial turn keeps the completion queued. Observe the
		// actual committed inbox/event before stopping Core, without guessing
		// at worker timing or changing the installed process's implementation.
		waitWorkerCanon(t, h, `SELECT count(*) FROM inbox i JOIN conv_event e ON e.id=i.native_id LEFT JOIN turn_runs t ON t.inbox_id=i.id WHERE i.source='worker-finish' AND e.scaffold_kind='worker_completion' AND t.id IS NULL`, 1)
		c.Close()
		h.stopCore(core)
		core = start()
		c, conversation = conversationClient(t, h)
		// Provider items retain the read-back call in model history; only the
		// resulting persona reply counts as delivery to the conversation.
		waitWorkerCanon(t, h, `SELECT count(*) FROM conv_event e JOIN conv_event p ON p.id=e.parent_id JOIN turn_runs t ON t.id='turn:'||p.id WHERE p.scaffold_kind='worker_completion' AND e.speaker_kind='agent' AND coalesce(e.scaffold_kind,'') NOT IN ('result','provider_item') AND t.state='completed'`, 1)
		voiced = 1
	} else {
		admittedTurn(t, h, c, conversation, "delegate", inputText)
		close(release)
		// The deadline diagnoses absence; success requires the persona result
		// and a completed follow-up, never the expiry of an arbitrary sleep.
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		for {
			frame, err := c.Receive(ctx)
			if err != nil {
				t.Errorf("worker finished without an agent-voiced checked result: %v", err)
				break
			}
			if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && strings.Contains(*item.Text, "Checked result:") {
				if item.Speaker == nil || *item.Speaker != protocol.ConversationSpeakerAgent || item.AgentID == nil || "main:"+*item.AgentID != conversation {
					t.Errorf("result has wrong speaker: %+v", item)
				}
				voiced++
			}
			if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active && voiced > 0 {
				break
			}
		}
		cancel()
	}
	artifact, err := os.ReadFile(filepath.Join(workdir, "result.txt"))
	if err != nil || string(artifact) != "LINA_WORKER_RESULT" {
		t.Fatalf("worker did not finish: %q %v", artifact, err)
	}
	if voiced != 1 || followups.Load() != 1 || !readBack.Load() {
		t.Errorf("voiced=%d followups=%d read_back=%t", voiced, followups.Load(), readBack.Load())
	}
	c.Close()
	h.stopCore(core)
	restarted.Store(true)
	core = start()
	c, conversation = conversationClient(t, h)
	frames := admittedTurn(t, h, c, conversation, "recall", "How did the delegated file job go?")
	found := false
	for _, frame := range frames {
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerAgent && item.Text != nil {
			found = found || strings.Contains(*item.Text, "Recorded outcome: LINA_WORKER_RESULT")
		}
	}
	if !found {
		t.Error("agent could not report the recorded outcome after restart")
	}
	if launches.Load() != 1 {
		t.Errorf("worker was relaunched: %d", launches.Load())
	}
	c.Close()
	h.stopCore(core)
	if pendingAtRestart {
		assertWorkerOutcomeDeliveredOnce(t, h)
	}
}

// Inspect the final committed state after restart, recall and shutdown. The
// reply boundary alone cannot detect an extra completion with no reply yet.
func assertWorkerOutcomeDeliveredOnce(t *testing.T, h *harness) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		for _, check := range []struct{ name, query string }{
			{"worker completions", `SELECT count(*) FROM conv_event WHERE scaffold_kind='worker_completion'`},
			{"completed follow-up turns", `SELECT count(*) FROM turn_runs t JOIN inbox i ON i.id=t.inbox_id JOIN conv_event e ON e.id=i.native_id WHERE i.source='worker-finish' AND e.scaffold_kind='worker_completion' AND t.state='completed'`},
			{"agent replies", `SELECT count(*) FROM conv_event e JOIN conv_event p ON p.id=e.parent_id WHERE p.scaffold_kind='worker_completion' AND e.speaker_kind='agent' AND coalesce(e.scaffold_kind,'') NOT IN ('result','provider_item')`},
		} {
			var count int
			if err := tx.QueryRowContext(h.ctx, check.query).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Errorf("%s after restart: count=%d want=1", check.name, count)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Read-only observation of committed installed-Core state provides the restart
// boundary. A timeout fails; it never substitutes for the expected transition.
func waitWorkerCanon(t *testing.T, h *harness, query string, want int) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var count int
		err := db.Read(ctx, func(tx *canon.Tx) error { return tx.QueryRowContext(ctx, query).Scan(&count) })
		if err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		if count > want {
			t.Fatalf("worker completion repeated: count=%d want=%d", count, want)
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatalf("worker completion boundary missing: count=%d want=%d: %v", count, want, ctx.Err())
		}
	}
}
