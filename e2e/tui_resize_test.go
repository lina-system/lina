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

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIConversationResize(t *testing.T) {
	data, err := os.ReadFile("m1/tui-resize.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Sizes               []struct{ Width, Height int }
		Input, Reply, Draft string
		NextInput           string `json:"next_input"`
		NextReply           string `json:"next_reply"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input, reply := scenario.Input, scenario.Reply
		if calls.Add(1) == 2 {
			input, reply = scenario.NextInput, scenario.NextReply
		}
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(mustJSON(t, body.Input)), input) {
			t.Errorf("provider did not receive %q", input)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", reply)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	observer, _ := conversationClient(t, h)
	defer observer.Close()
	waitTurn := func() {
		for {
			frame, err := observer.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
				return
			}
		}
	}
	environment := []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	size := scenario.Sizes[0]
	session := h.startTerminalSize(environment, false, true, size.Width, size.Height)
	waitScreen(t, h, session, design.Text(design.English, "live"))
	h.keys(session, scenario.Input+"\r")
	waitTurn()
	waitScreen(t, h, session, "REPLY-340")
	h.keys(session, scenario.Draft)
	initial := waitResizeChrome(t, h, session, size.Height, scenario.Draft)
	header := screenRows(initial)[0]
	if header == "" || !strings.Contains(screenText(initial), "ROW-01") {
		t.Fatal("initial header or transcript missing")
	}
	for _, size := range scenario.Sizes[1:] {
		h.resizeTerminal(session, size.Width, size.Height)
		resized := waitResizeChrome(t, h, session, size.Height, scenario.Draft)
		if screenRows(resized)[0] != header {
			t.Fatal("resize lost the header")
		}
		// A fresh client at the same dimensions is an independent oracle for
		// every row, including blank rows and transcript wrapping. Do not clear
		// the resized emulator: stale cells must be erased by the application.
		fresh := h.startTerminalSize(environment, false, true, size.Width, size.Height)
		waitScreen(t, h, fresh, "REPLY-340")
		h.keys(fresh, scenario.Draft)
		expected := waitResizeChrome(t, h, fresh, size.Height, scenario.Draft)
		waitMatchingScreen(t, h, session, expected)
		h.keys(fresh, "\x04")
		fresh.process.wait(t, 0)
		h.expectTerminalRestored(fresh)
		t.Logf("%dx%d: header, status, draft and cursor visible; all rows match a fresh client", size.Width, size.Height)
	}
	h.keys(session, "\x03"+scenario.NextInput+"\r")
	waitTurn()
	waitScreen(t, h, session, scenario.NextReply)
	size = scenario.Sizes[len(scenario.Sizes)-1]
	fresh := h.startTerminalSize(environment, false, true, size.Width, size.Height)
	waitScreen(t, h, fresh, scenario.NextReply)
	expected := waitResizeChrome(t, h, fresh, size.Height, "")
	waitMatchingScreen(t, h, session, expected)
	if calls.Load() != 2 {
		t.Fatalf("resize sent the draft or repeated a request: calls=%d", calls.Load())
	}
	for _, term := range []*terminal{session, fresh} {
		h.keys(term, "\x04")
		term.process.wait(t, 0)
		h.expectTerminalRestored(term)
	}
	observer.Close()
	h.stopCore(core)
}

func screenRows(snapshot tuiterm.Snapshot) []string {
	rows := strings.Split(strings.TrimSuffix(screenText(snapshot), "\n"), "\n")
	for i := range rows {
		rows[i] = strings.TrimRight(rows[i], " ")
	}
	return rows
}

func waitResizeChrome(t *testing.T, h *harness, term *terminal, height int, draft string) tuiterm.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	content := draft
	if content == "" {
		content = design.Text(design.English, "composer.placeholder")
	}
	snapshot, err := term.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		rows := screenRows(s)
		return len(rows) == height && s.AltScreen && !s.CursorHidden &&
			s.CursorY == height-2 && strings.TrimSpace(rows[height-2]) == "› "+content &&
			s.CursorX == (render.Unicode{}).Width("› "+draft) &&
			strings.Contains(rows[height-1], design.Text(design.English, "live"))
	})
	if err != nil {
		t.Fatalf("composer/status/cursor missing at height %d: %v\n%s", height, err, strings.Join(screenRows(snapshot), "\n"))
	}
	return snapshot
}

func waitMatchingScreen(t *testing.T, h *harness, term *terminal, expected tuiterm.Snapshot) {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	want := strings.Join(screenRows(expected), "\n")
	actual, err := term.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		return strings.Join(screenRows(s), "\n") == want && s.CursorX == expected.CursorX &&
			s.CursorY == expected.CursorY && s.CursorHidden == expected.CursorHidden && s.AltScreen
	})
	if err != nil {
		t.Fatalf("resized screen differs from fresh client: %v\nactual:\n%s\nexpected:\n%s", err, strings.Join(screenRows(actual), "\n"), want)
	}
}
