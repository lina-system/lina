// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIFirstRunInstalledCore(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		t.Run(string(language), func(t *testing.T) {
			const input, reply = "Check the orchard.", "The orchard is ready."
			release := make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct{ Input []json.RawMessage }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(mustJSON(t, body.Input)), input) {
					t.Error("typed input did not reach the installed Core's model")
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", reply)
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			}))
			defer provider.Close()
			defer finish()
			h, start := installedConversationHarness(t, provider.URL)
			core := start(true)
			observer, conversation := conversationClient(t, h)
			topic := "conversation:" + conversation
			assertSnapshot := func(populated bool) {
				t.Helper()
				if _, err := observer.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
					t.Fatal(err)
				}
				for {
					frame, err := observer.Receive(h.ctx)
					if err != nil {
						t.Fatal(err)
					}
					snapshot, ok := frame.Payload.(protocol.Snapshot)
					if !ok {
						continue
					}
					text := string(mustJSON(t, snapshot.Items))
					assertNoFirstRun(t, text, language)
					if populated && (!strings.Contains(text, input) || !strings.Contains(text, reply)) {
						t.Fatalf("Core snapshot lost the real exchange: %s", text)
					}
					for _, item := range snapshot.Items {
						if !populated && item.Kind != protocol.KindTurnState {
							t.Fatalf("fresh conversation has a visible item: %s", text)
						}
					}
					return
				}
			}
			assertSnapshot(false)
			// Observe the real TUI's history subscription before the input barrier.
			socket := h.socket
			tap := newAccountTap(t, h, socket)
			h.socket = tap.socket
			environment := []string{"TERM=xterm-256color", "COLORTERM=truecolor", "LINA_THEME=dark", "LC_ALL=" + string(language)}
			terminal := h.startTerminalSize(environment, false, true, 100, 34)
			for {
				frame := tap.next(t, h)
				if snapshot, ok := frame.Payload.(protocol.Snapshot); ok && snapshot.Topic == topic {
					break
				}
			}
			h.keys(terminal, input)
			// The tap observes socket writes, not reducer consumption. Wait for
			// the empty-history presentation as well as the draft before sampling.
			fresh, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
				text := screenText(s)
				if !s.AltScreen || !strings.Contains(text, "lina.") || !strings.Contains(text, input) {
					return false
				}
				for _, id := range firstRunSuggestionIDs {
					if !strings.Contains(text, design.Text(language, id)) {
						return false
					}
				}
				return true
			})
			if err != nil {
				t.Fatalf("first-run presentation did not render: %v; cells=%s", err, screenText(fresh))
			}
			text := screenText(fresh)
			if !fresh.AltScreen || !strings.Contains(text, "lina.") {
				t.Fatalf("fresh conversation has no wordmark: %s", text)
			}
			if !strings.Contains(text, "LINA") || !strings.Contains(text, design.Text(language, "live")) {
				t.Fatal("empty screen lost agent or connection identity")
			}
			for _, id := range firstRunSuggestionIDs {
				suggestion, found := design.Lookup(language, id)
				if !found || strings.Count(text, suggestion) != 1 {
					t.Fatalf("missing or repeated catalog suggestion %s: %s", id, text)
				}
				for y, row := range screenRows(fresh) {
					if strings.Contains(row, suggestion) {
						cell := fresh.Cells[y][0]
						if cell.Foreground == nil || cell.Bold {
							t.Fatalf("suggestion %s lost muted styling", id)
						}
						r, g, b, _ := cell.Foreground.RGBA()
						if got := fmt.Sprintf("#%02X%02X%02X", r>>8, g>>8, b>>8); got != strings.ToUpper(design.Dark().ColorMuted.Hex) {
							t.Fatalf("suggestion %s foreground = %s", id, got)
						}
					}
				}
			}
			// A new draft settles the redraw after Enter while the reply is held.
			h.keys(terminal, "\rnext-draft")
			assertNoFirstRun(t, screenText(waitScreen(t, h, terminal, "next-draft")), language)
			finish()
			assertNoFirstRun(t, screenText(waitScreen(t, h, terminal, reply)), language)
			for {
				frame, err := observer.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
					break
				}
			}
			assertSnapshot(true)
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			observer.Close()
			h.socket = socket
			h.stopCore(core)
			core = start(true)
			observer, _ = conversationClient(t, h)
			assertSnapshot(true)
			again := h.startTerminalSize(environment, false, true, 100, 34)
			assertNoFirstRun(t, screenText(waitScreen(t, h, again, reply)), language)
			h.keys(again, "\x04")
			again.process.wait(t, 0)
			h.expectTerminalRestored(again)
			observer.Close()
			h.stopCore(core)
			if calls.Load() != 1 {
				t.Fatalf("restart replayed the input: %d calls", calls.Load())
			}
		})
	}
}

var firstRunSuggestionIDs = []string{"conversation.start.memory", "conversation.start.voice", "conversation.start.task"}

func assertNoFirstRun(t *testing.T, text string, language design.Language) {
	t.Helper()
	if strings.Contains(text, "lina.") {
		t.Fatalf("wordmark persisted after first message or entered Core history: %s", text)
	}
	for _, id := range firstRunSuggestionIDs {
		if suggestion, found := design.Lookup(language, id); found && strings.Contains(text, suggestion) {
			t.Fatalf("starter suggestion %s persisted: %s", id, text)
		}
	}
}
