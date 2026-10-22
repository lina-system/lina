// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUITranscriptInstalledCore(t *testing.T) {
	var scenario struct {
		Inputs, Replies []string
		Draft, Search   string
	}
	data, err := os.ReadFile("m1/tui-transcript.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(calls.Add(1)) - 1
		if i >= len(scenario.Replies) {
			t.Error("overlay sent an extra input")
			return
		}
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(mustJSON(t, request.Input)), scenario.Inputs[i]) {
			t.Error("typed input did not reach Core's provider")
		}
		emitResponse(w, scenario.Replies[i], true)
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	observer, _ := conversationClient(t, h)
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 100, 34)
	waitScreen(t, h, terminal, design.Text(design.English, "live"))
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
	h.keys(terminal, scenario.Draft)
	waitScreen(t, h, terminal, scenario.Draft)
	h.keys(terminal, "\x14")
	wait := func(predicate func(tuiterm.Snapshot) bool) tuiterm.Snapshot {
		t.Helper()
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		snapshot, err := terminal.screen.Wait(ctx, predicate)
		if err != nil {
			t.Fatalf("transcript assertion: %v\n%s", err, screenText(snapshot))
		}
		return snapshot
	}
	page := wait(func(s tuiterm.Snapshot) bool { return strings.Contains(screenText(s), "Esc  /  n") })
	text := screenText(page)
	position := 0
	for i, input := range scenario.Inputs {
		for j, message := range []string{input, scenario.Replies[i]} {
			speaker := design.Text(design.English, "person")
			if j == 1 {
				speaker = "LINA"
			}
			// Names are words on a separate line; terminal padding is irrelevant.
			lines := strings.Split(text, "\n")
			found := false
			for row := position + 1; row < len(lines); row++ {
				if strings.TrimSpace(lines[row]) == message && strings.TrimSpace(lines[row-1]) == speaker {
					position, found = row, true
					break
				}
			}
			if !found || strings.Count(text, message) != 1 {
				t.Fatalf("missing, reordered or repeated message %q from %q:\n%s", message, speaker, text)
			}
		}
	}
	if strings.Contains(text, scenario.Draft) {
		t.Fatal("unsent draft entered transcript")
	}
	// Shrink below the transcript height, move to the end, then search back
	// to the first message. Success depends on rendered cells, never a sleep.
	h.resizeTerminal(terminal, 100, 12)
	wait(func(s tuiterm.Snapshot) bool {
		return len(s.Cells) == 12 && strings.Contains(screenRows(s)[11], "Esc  /  n")
	})
	h.keys(terminal, "\x1b[F")
	wait(func(s tuiterm.Snapshot) bool {
		text := screenText(s)
		return len(s.Cells) == 12 && strings.Contains(text, scenario.Replies[2]) && !strings.Contains(text, scenario.Inputs[0])
	})
	h.keys(terminal, "/"+scenario.Search+"\r")
	wait(func(s tuiterm.Snapshot) bool {
		return strings.Contains(screenText(s), scenario.Inputs[0]) && strings.Contains(screenText(s), "Esc  /  n  "+scenario.Search)
	})
	h.keys(terminal, "n")
	h.keys(terminal, "\x1b")
	wait(func(s tuiterm.Snapshot) bool {
		text := screenText(s)
		return strings.Contains(text, scenario.Draft) && !strings.Contains(text, "Esc  /  n")
	})
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	observer.Close()
	h.stopCore(core)
	if calls.Load() != 3 {
		t.Fatalf("model calls = %d, want 3", calls.Load())
	}
}
