// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/install"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIConversationInstalledCore(t *testing.T) {
	data, err := os.ReadFile("m1/tui-conversation.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Input, Reply string }
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var modelCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(mustJSON(t, body.Input)), scenario.Input) {
			t.Error("typed input did not reach model")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", scenario.Reply)
		w.(http.Flusher).Flush()
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", scenario.Reply)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if len(client.Welcome.Capabilities) == 0 {
		t.Fatal("installed Core has no conversation service")
	}
	if _, err = client.History(h.ctx, protocol.HistoryGet{}); err != nil {
		t.Fatal(err)
	}
	discovery, err := client.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, ok := discovery.Payload.(protocol.History)
	if !ok || history.ConversationID == nil {
		t.Fatal("no conversation discovery")
	}
	topic := "conversation:" + *history.ConversationID
	if _, err = client.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := client.Receive(h.ctx); err != nil || snapshot.Kind != protocol.KindSnapshot {
		t.Fatal("no history snapshot")
	}
	terminal := h.startScreenTUI("TERM=xterm-256color", "COLORTERM=truecolor")
	fresh := waitScreen(t, h, terminal, "Ctrl+D")
	if text := screenText(fresh); strings.Contains(strings.ToLower(text), "unsupported") {
		t.Fatalf("fresh install shows an unsupported placeholder: %s", text)
	}
	h.keys(terminal, scenario.Input+"\r")
	snapshot := waitScreen(t, h, terminal, scenario.Reply)
	if text := screenText(snapshot); strings.Contains(strings.ToLower(text), "unsupported") {
		t.Fatalf("first exchange shows an unsupported placeholder: %s", text)
	}
	if !snapshot.AltScreen {
		t.Fatal("conversation did not use the terminal renderer")
	}
	var echo string
	for {
		frame, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.EchoKey != nil {
			echo = *item.EchoKey
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	if echo == "" {
		t.Fatal("missing durable input echo")
	}
	changed := "MUST-NOT-REPLACE"
	if _, err = client.Input(h.ctx, protocol.Input{ConversationID: history.ConversationID, InputID: &echo, Text: &changed}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := client.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := duplicate.Payload.(protocol.ItemDone)
	if !ok || item.Text == nil || *item.Text != scenario.Input {
		t.Fatal("duplicate replaced the admitted input")
	}
	foreign := "outside-conversation"
	if _, err = client.Interrupt(h.ctx, protocol.Interrupt{ConversationID: &foreign}); err != nil {
		t.Fatal(err)
	}
	refusal, err := client.Receive(h.ctx)
	if err != nil || refusal.Error == nil || refusal.Error.Code != protocol.ErrorOutOfScope {
		t.Fatal("foreign conversation was not refused")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	client.Close()
	h.stopCore(core)
	core = start(true)
	// Restarting Core and reopening the client restores the ledger without replay.
	again := h.startScreenTUI("TERM=xterm-256color")
	waitScreen(t, h, again, scenario.Reply)
	h.keys(again, "\x04")
	again.process.wait(t, 0)
	h.expectTerminalRestored(again)
	h.stopCore(core)
	core = start(false)
	plain, err := clientapi.Dial(h.ctx, h.socket, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Welcome.Capabilities) != 0 {
		t.Fatal("unconfigured Core retained model handlers")
	}
	plain.Close()
	h.stopCore(core)
	if modelCalls.Load() != 1 {
		t.Fatalf("duplicate or restart repeated the model call: %d", modelCalls.Load())
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func screenText(snapshot tuiterm.Snapshot) string {
	var text strings.Builder
	for _, row := range snapshot.Cells {
		for _, cell := range row {
			text.WriteString(cell.Content)
		}
		text.WriteByte('\n')
	}
	return text.String()
}
func waitScreen(t *testing.T, h *harness, terminal *terminal, text string) tuiterm.Snapshot {
	t.Helper()
	snapshot, err := terminal.screen.Wait(h.ctx, func(snapshot tuiterm.Snapshot) bool { return strings.Contains(screenText(snapshot), text) })
	if err != nil {
		select {
		case <-terminal.process.done:
			t.Logf("TUI exited: %v; stderr=%s", terminal.process.err, &terminal.process.stderr)
		default:
		}
		t.Fatalf("screen missing %q: %v; cells=%s", text, err, screenText(snapshot))
	}
	return snapshot
}

func installedConversationHarness(t *testing.T, providerURL string, installArgs ...string) (*harness, func(bool) *process) {
	t.Helper()
	h := newHarness(t, buildLina(t))
	base := h.root
	bundle := makeInstallBundle(t, base, h.binary, buildCommand(t, "lina-node"))
	return installConversationBundle(t, h, bundle, providerURL, installArgs...)
}

func installConversationBundle(t *testing.T, h *harness, bundle, providerURL string, installArgs ...string) (*harness, func(bool) *process) {
	t.Helper()
	base := h.root
	bin := filepath.Join(base, "tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\ncase \"$2\" in show) printf 'not-found\\n';; daemon-reload|enable) exit 0;; *) exit 97;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "state")
	args := append([]string{"install", "--state-root", root, "--unit-dir", filepath.Join(base, "units"), "--bundle", bundle}, installArgs...)
	p := h.command(args...)
	p.command.Env = append(p.command.Env, "PATH="+bin)
	p.start(t)
	p.wait(t, 0)
	data, err := os.ReadFile(filepath.Join(root, "system/install.json"))
	if err != nil {
		t.Fatal(err)
	}
	var installed install.Result
	if err := json.Unmarshal(data, &installed); err != nil {
		t.Fatal(err)
	}
	h.binary, h.root, h.socket = filepath.Join(root, "system/bin/lina"), root, installed.Socket
	start := func(configured bool) *process {
		core := h.command("core", "--state-root", root, "--socket", h.socket)
		if configured {
			core.command.Env = append(core.command.Env, "LINA_RESPONSES_BASE_URL="+providerURL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		}
		core.start(t)
		h.connect(core).Close()
		// A bound socket accepts reads while Core is still reconciling/probing.
		// Wait for mutation admission via an empty, always-refused input; no
		// conversation event or model request can result from this probe.
		ready, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		defer ready.Close()
		for {
			if _, err := ready.Input(h.ctx, protocol.Input{}); err != nil {
				t.Fatal(err)
			}
			frame, err := ready.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.Error == nil {
				t.Fatal("empty input accepted")
			}
			if frame.Error.Code == protocol.ErrorUnavailable {
				continue
			}
			if frame.Error.Code != protocol.ErrorMalformed && frame.Error.Code != protocol.ErrorCapabilityNotNegotiated {
				t.Fatalf("unexpected readiness code: %s", frame.Error.Code)
			}
			break
		}
		return core
	}

	return h, start
}
