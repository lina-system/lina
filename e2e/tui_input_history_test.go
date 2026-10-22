// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/render"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIInputHistory(t *testing.T) {
	data, err := os.ReadFile("m1/tui-input-history.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Inputs, Replies []string
		Draft, Token    string
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if n >= len(scenario.Inputs) {
			t.Error("recall sent an extra model request")
			http.Error(w, "unexpected request", 500)
			return
		}
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(mustJSON(t, body.Input)), scenario.Inputs[n]) {
			t.Errorf("provider missing input %d", n)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", scenario.Replies[n])
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	observer, _ := conversationClient(t, h)
	defer observer.Close()
	terminal, _ := h.startConversationTUI(100, 34, "TERM=xterm-256color", "LC_ALL=en_US.UTF-8", "LINA_GLYPHS=unicode")
	for i, input := range scenario.Inputs {
		h.keys(terminal, input+"\r")
		for {
			frame, err := observer.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
				break
			}
		}
		waitScreen(t, h, terminal, scenario.Replies[i])
	}
	// The real channel overlay receives the synthetic token. Cancel before
	// connecting: this test requires no external platform or credential.
	h.keys(terminal, "/channels\r")
	waitScreen(t, h, terminal, "c Connect")
	h.keys(terminal, "c")
	waitScreen(t, h, terminal, "bot token")
	h.keys(terminal, scenario.Token)
	waitScreen(t, h, terminal, strings.Repeat("*", len(scenario.Token)))
	h.keys(terminal, "\x1b")
	waitScreen(t, h, terminal, "c Connect")
	h.keys(terminal, "\x1b")
	waitHistoryComposer(t, h, terminal, "", 32)
	h.keys(terminal, scenario.Draft)
	waitHistoryComposer(t, h, terminal, scenario.Draft, 32)
	for _, step := range []struct{ key, want string }{
		{"\x1b[A", scenario.Inputs[1]}, {"\x1b[A", scenario.Inputs[0]},
		{"\x1b[A", scenario.Inputs[0]}, {"\x1b[B", scenario.Inputs[1]},
		{"\x1b[B", scenario.Draft},
	} {
		h.keys(terminal, step.key)
		waitHistoryComposer(t, h, terminal, step.want, 32)
	}
	// With three physical draft lines, Up moves inside the composer.
	h.keys(terminal, "\x03\x1b[200~one\ntwo\nthree\x1b[201~")
	waitHistoryComposer(t, h, terminal, "three", 32)
	h.keys(terminal, "\x1b[A")
	waitHistoryComposer(t, h, terminal, "two", 31)
	rows := screenRows(terminal.screen.Snapshot())
	if !strings.HasSuffix(rows[30], "one") || !strings.HasSuffix(rows[32], "three") {
		t.Fatal("Up replaced a multiline draft")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	// A fresh TUI starts with no draft and derives recall from Core's snapshot.
	fresh := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=en_US.UTF-8", "LINA_GLYPHS=unicode"}, false, true, 100, 34)
	waitScreen(t, h, fresh, scenario.Replies[1])
	waitHistoryComposer(t, h, fresh, "", 32)
	h.keys(fresh, "\x1b[A")
	waitHistoryComposer(t, h, fresh, scenario.Inputs[1], 32)
	h.keys(fresh, "\x04")
	fresh.process.wait(t, 0)
	h.expectTerminalRestored(fresh)
	h.stopCore(core)
	if calls.Load() != 2 {
		t.Fatalf("model calls=%d, want two sent inputs only", calls.Load())
	}
	if strings.Contains(string(terminal.screen.Bytes()), scenario.Token) {
		t.Fatal("token reached terminal output")
	}
}

func waitHistoryComposer(t *testing.T, h *harness, terminal *terminal, text string, row int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	_, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		if !s.AltScreen || s.CursorHidden || s.CursorY != row {
			return false
		}
		line := screenRows(s)[row]
		if text == "" {
			return strings.HasPrefix(line, "› Talk to LINA") && s.CursorX == 2
		}
		return strings.TrimPrefix(strings.TrimPrefix(line, "› "), "  ") == text && s.CursorX == 2+(render.Unicode{}).Width(text)
	})
	if err != nil {
		t.Fatalf("composer did not show %q at row %d: %v\n%s", text, row, err, screenText(terminal.screen.Snapshot()))
	}
}
