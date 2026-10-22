// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIStreamingCodeInstalledCore(t *testing.T) {
	var code strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&code, "CODE-%03d 한글\n", i)
	}
	open := "Before the code\n\n```go\n" + code.String()
	closing := "```\n\nAfter the code\n"
	closeFence, finish := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(string(mustJSON(t, body.Input)), "Write a long example") {
			t.Error("typed request missing from provider input")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta := func(text string) {
			fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", text)
			w.(http.Flusher).Flush()
		}
		for _, line := range strings.SplitAfter(open, "\n") {
			delta(line)
		}
		select {
		case <-closeFence:
		case <-r.Context().Done():
			return
		}
		delta(closing)
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		emitResponse(w, open+closing, true)
	}))
	defer provider.Close()
	defer provider.CloseClientConnections()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	client, conversation := conversationClient(t, h)
	defer client.Close()
	socket := h.socket
	tap := newAccountTap(t, h, socket, true)
	h.socket = tap.socket
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "COLORTERM=truecolor", "LINA_THEME=dark"}, false, true, 100, 240)
	h.socket = socket
	waitScreen(t, h, terminal, "Ctrl+D")
	for {
		frame := tap.next(t, h)
		if snapshot, ok := frame.Payload.(protocol.Snapshot); ok && strings.HasPrefix(snapshot.Topic, "conversation:") {
			break
		}
	}
	h.keys(terminal, "Write a long example\r")
	// Drain the capture while all 200 deltas cross the socket; its bounded
	// channel must not backpressure the TUI before the held provider checkpoint.
	for {
		frame := tap.next(t, h)
		if result, ok := frame.Payload.(protocol.Result); ok && result.Text != nil && strings.Contains(*result.Text, "CODE-200") {
			break
		}
	}
	inline := waitScreen(t, h, terminal, "CODE-200")
	if text := screenText(inline); strings.Count(text, "CODE-") != 6 || !strings.Contains(text, "writing code… (200 lines)") {
		t.Fatalf("inline reply lost the streaming code bound: %d code rows", strings.Count(text, "CODE-"))
	}
	h.keys(terminal, "\x0f")
	waitScreen(t, h, terminal, "Esc  /  n")
	snapshot := waitScreen(t, h, terminal, "CODE-200")
	text := screenText(snapshot)
	if strings.Count(text, "CODE-") != 6 || !strings.Contains(text, "writing code… (200 lines)") || !strings.Contains(text, "Before the code") {
		t.Fatalf("open fence must show its last six rows and one placeholder, preserving prose; code rows=%d\n%s", strings.Count(text, "CODE-"), text)
	}
	rows := screenRows(snapshot)
	placeholder, first := -1, -1
	for y, row := range rows {
		if strings.Contains(row, "writing code") {
			placeholder = y
		}
		if strings.Contains(row, "CODE-195") {
			first = y
		}
	}
	if placeholder < 0 || first != placeholder+1 {
		t.Fatalf("placeholder/tail not adjacent: %d %d", placeholder, first)
	}
	if reflect.DeepEqual(snapshot.Cells[placeholder][0].Foreground, snapshot.Cells[first][0].Foreground) {
		t.Error("placeholder is not muted relative to code")
	}
	t.Logf("open fence: %s", strings.Join(rows[placeholder:placeholder+7], "\n"))
	close(closeFence)
	snapshot = waitScreen(t, h, terminal, "After the code")
	text = screenText(snapshot)
	if strings.Contains(text, "writing code") || strings.Count(text, "CODE-") != 200 {
		t.Fatalf("closing delta did not restore 200 code rows before item_done: %d", strings.Count(text, "CODE-"))
	}
	close(finish)
	for {
		frame, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	topic := "conversation:" + conversation
	if _, err := client.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot, ok := frame.Payload.(protocol.Snapshot); ok {
			found := false
			for _, item := range snapshot.Items {
				if item.Kind == protocol.KindResult {
					var result protocol.Result
					if err := json.Unmarshal(item.Payload, &result); err != nil {
						t.Fatal(err)
					}
					found = result.Text != nil && *result.Text == open+closing
				}
			}
			if !found {
				t.Fatal("Core did not retain the full result")
			}
			break
		}
	}
	t.Log("closing fence restored all 200 rows before item_done; Core retained the exact result")
	h.keys(terminal, "\x1b")
	waitScreen(t, h, terminal, "Ctrl+O")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
}
