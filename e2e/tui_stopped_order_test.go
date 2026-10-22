// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIStoppedNoticeOrder(t *testing.T) {
	var scenario struct {
		Width, Height                int
		ResizedWidth                 int `json:"resized_width"`
		Input, Partial, Reply, Draft string
		NextInput                    string `json:"next_input"`
	}
	data, err := os.ReadFile("m1/tui-stopped-order.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		input := string(mustJSON(t, body.Input))
		switch calls.Add(1) {
		case 1:
			if !strings.Contains(input, scenario.Input) {
				t.Error("first request missing typed input")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", scenario.Partial)
			w.(http.Flusher).Flush()
			<-r.Context().Done() // Only /stop ends this response.
		case 2:
			if !strings.Contains(input, scenario.NextInput) || strings.Contains(input, scenario.Partial) {
				t.Error("follow-up input missing or interrupted partial entered context")
			}
			emitResponse(w, scenario.Reply, true)
		default:
			t.Error("reconnect or redraw replayed a model request")
		}
	}))
	t.Cleanup(provider.Close)
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	observer, conversation := conversationClient(t, h)
	waitTurn := func() string {
		t.Helper()
		var outcome string
		for {
			frame, err := observer.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && item.EchoKey == nil && (item.Speaker == nil || *item.Speaker == protocol.ConversationSpeakerSystem) {
				outcome = *item.Text
			}
			if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
				return outcome
			}
		}
	}
	environment := []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	terminal := h.startTerminalSize(environment, false, true, scenario.Width, scenario.Height)
	waitScreen(t, h, terminal, design.Text(design.English, "live"))
	h.keys(terminal, scenario.Input+"\r")
	waitScreen(t, h, terminal, scenario.Partial)
	h.keys(terminal, "/stop\r")
	stopped := waitTurn()
	if stopped == "" {
		t.Fatal("stop produced no system outcome")
	}
	waitScreen(t, h, terminal, stopped)
	h.keys(terminal, scenario.NextInput+"\r")
	waitTurn()
	waitScreen(t, h, terminal, scenario.Reply)
	assertOrder := func(phase, text string) {
		t.Helper()
		previous := -1
		var positions []int
		ordered := true
		for _, item := range []string{scenario.Input, stopped, scenario.NextInput, scenario.Reply} {
			position := strings.Index(text, item)
			positions = append(positions, position)
			ordered = ordered && position > previous && strings.Count(text, item) == 1
			previous = position
		}
		t.Logf("%s: input/stopped/follow-up/reply positions=%v; ordered once=%t", phase, positions, ordered)
		if !ordered {
			t.Errorf("%s: stopped outcome detached from its turn; transcript:\n%s", phase, strings.TrimSpace(text))
		}
	}
	h.keys(terminal, scenario.Draft)
	assertOrder("before restart", screenText(waitResizeChrome(t, h, terminal, scenario.Height, scenario.Draft)))
	before := observer.Welcome.Epoch
	observer.Close()
	h.stopCore(core)
	waitScreen(t, h, terminal, design.Text(design.English, "retrying"))
	core = start(true)
	observer, _ = conversationClient(t, h)
	if observer.Welcome.Epoch == before {
		t.Fatal("restart did not advance epoch")
	}
	// Check Core's authoritative order independently of the existing TUI cache.
	topic := "conversation:" + conversation
	if _, err := observer.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := observer.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if !ok {
		t.Fatalf("expected snapshot, got %s", frame.Kind)
	}
	var history []string
	for _, row := range snapshot.Items {
		if row.Kind == protocol.KindItemDone {
			var item protocol.ItemDone
			if err := json.Unmarshal(row.Payload, &item); err != nil {
				t.Fatal(err)
			}
			if item.Text != nil {
				history = append(history, *item.Text)
			}
		}
	}
	assertOrder("Core snapshot", strings.Join(history, "\n"))
	waitScreen(t, h, terminal, design.Text(design.English, "live"))
	// A composer edit is a redraw barrier; no sleep determines correctness.
	h.keys(terminal, "x")
	assertOrder("after reconnect", screenText(waitResizeChrome(t, h, terminal, scenario.Height, scenario.Draft+"x")))
	h.resizeTerminal(terminal, scenario.ResizedWidth, scenario.Height)
	h.keys(terminal, "y")
	assertOrder("after resize", screenText(waitResizeChrome(t, h, terminal, scenario.Height, scenario.Draft+"xy")))
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	fresh := h.startTerminalSize(environment, false, true, scenario.ResizedWidth, scenario.Height)
	waitScreen(t, h, fresh, scenario.Reply)
	assertOrder("fresh TUI", screenText(waitResizeChrome(t, h, fresh, scenario.Height, "")))
	if calls.Load() != 2 {
		t.Fatalf("provider calls=%d, want exactly two explicit turns", calls.Load())
	}
	h.keys(fresh, "\x04")
	fresh.process.wait(t, 0)
	h.expectTerminalRestored(fresh)
	observer.Close()
	h.stopCore(core)
}
