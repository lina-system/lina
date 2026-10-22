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
	for _, language := range []design.Language{design.English, design.Korean} {
		t.Run(string(language), func(t *testing.T) {
			testTUITranscriptInstalledCore(t, language)
		})
	}
}

func testTUITranscriptInstalledCore(t *testing.T, language design.Language) {
	var scenario struct {
		Inputs, Replies []string
		Draft, Search   string
	}
	data, err := os.ReadFile("tui-transcript.json")
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
	terminal, _ := h.startConversationTUI(100, 34, "TERM=xterm-256color", "LC_ALL="+string(language))
	waitScreen(t, h, terminal, design.Text(language, "live"))
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
	page := wait(func(s tuiterm.Snapshot) bool {
		rows := screenRows(s)
		return s.AltScreen && s.CursorHidden && len(rows) == 34 &&
			strings.TrimSpace(rows[0]) == design.Text(language, "person") &&
			strings.Contains(screenText(s), scenario.Replies[len(scenario.Replies)-1]) &&
			!strings.Contains(screenText(s), scenario.Draft)
	})
	text := screenText(page)
	position := 0
	for i, input := range scenario.Inputs {
		for j, message := range []string{input, scenario.Replies[i]} {
			speaker := design.Text(language, "person")
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
		rows := screenRows(s)
		// Resize changes the emulator's dimensions before the TUI redraws.
		// The connection label in the new status row proves that redraw arrived.
		return len(rows) == 12 && s.AltScreen && s.CursorHidden &&
			strings.Contains(strings.Join(rows[:11], "\n"), scenario.Inputs[0]) &&
			strings.Contains(rows[11], design.Text(language, "live"))
	})
	h.keys(terminal, "\x1b[F")
	wait(func(s tuiterm.Snapshot) bool {
		text := screenText(s)
		return len(s.Cells) == 12 && s.AltScreen && s.CursorHidden &&
			strings.Contains(text, scenario.Replies[2]) && !strings.Contains(text, scenario.Inputs[0])
	})
	h.keys(terminal, "/"+scenario.Search+"\r")
	wait(func(s tuiterm.Snapshot) bool {
		rows := screenRows(s)
		return len(rows) == 12 && s.AltScreen && s.CursorHidden &&
			strings.Contains(strings.Join(rows[:11], "\n"), scenario.Inputs[0]) &&
			strings.Contains(rows[11], scenario.Search)
	})
	h.keys(terminal, "n")
	h.keys(terminal, "\x1b")
	wait(func(s tuiterm.Snapshot) bool {
		return len(s.Cells) == 12 && s.AltScreen && !s.CursorHidden && s.CursorY == 10 &&
			strings.Contains(screenRows(s)[s.CursorY], scenario.Draft)
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
