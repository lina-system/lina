// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1ConversationHistoryOrder(t *testing.T) {
	var scenario struct {
		Width, Height int
		FirstInput    string `json:"first_input"`
		Partial       string `json:"partial"`
		FirstReply    string `json:"first_reply"`
		SecondInput   string `json:"second_input"`
		SecondReply   string `json:"second_reply"`
	}
	data, err := os.ReadFile("m1/conversation-history-order.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, command string
		stream        bool
	}{
		{"queue", "/queue ", true},
		{"steer", "", true},
		{"final-only", "/queue ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch calls.Add(1) {
				case 1:
					w.Header().Set("Content-Type", "text/event-stream")
					if tc.stream {
						fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", scenario.Partial)
						w.(http.Flusher).Flush()
					}
					close(started)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					emitResponse(w, scenario.FirstReply, true)
				case 2:
					emitResponse(w, scenario.SecondReply, true)
				default:
					t.Error("history restoration replayed a provider request")
				}
			}))
			t.Cleanup(server.Close)
			t.Cleanup(unblock)
			h, start := installedConversationHarness(t, server.URL)
			core := start(true)
			observer, conversation := conversationClient(t, h)
			socket := h.socket
			tap := newAccountTap(t, h, socket)
			h.socket = tap.socket
			terminal := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, scenario.Width, scenario.Height)
			h.socket = socket
			waitScreen(t, h, terminal, "Ctrl+D")
			// Controls can appear before negotiation permits input. Wait for Live.
			waitScreen(t, h, terminal, design.Text(design.English, "live"))
			// The composer is visible before History resolves its destination.
			// A conversation subscription proves that the TUI consumed History;
			// waiting for its response avoids submitting during discovery.
			for {
				frame := tap.next(t, h)
				if snapshot, ok := frame.Payload.(protocol.Snapshot); ok && snapshot.Topic == "conversation:"+conversation {
					break
				}
			}
			h.keys(terminal, scenario.FirstInput+"\r")
			select {
			case <-started:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			if tc.stream {
				waitScreen(t, h, terminal, scenario.Partial)
			}
			h.keys(terminal, tc.command+scenario.SecondInput+"\r")
			// Wait for durable admission, not just the TUI's local echo.
			for {
				frame, err := observer.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text == scenario.SecondInput {
					break
				}
			}
			unblock()
			turns := 2
			if tc.command == "" {
				turns = 1 // Steering is consumed by another round of the same turn.
			}
			for completed := 0; completed < turns; {
				frame, err := observer.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
					completed++
				}
			}
			want := []string{scenario.FirstInput, scenario.FirstReply, scenario.SecondInput, scenario.SecondReply}
			if !tc.stream {
				want[1], want[2] = want[2], want[1]
			}
			assertScreen := func(phase string) {
				t.Helper()
				text := screenText(waitScreen(t, h, terminal, scenario.SecondReply))
				previous := -1
				for _, message := range want {
					position := strings.Index(text, message)
					if position <= previous || strings.Count(text, message) != 1 {
						t.Errorf("%s: conversation order/content changed:\n%s", phase, text)
						return
					}
					previous = position
				}
			}
			type message struct {
				Key, Text string
				Speaker   protocol.ConversationSpeaker
			}
			snapshot := func() []message {
				t.Helper()
				topic := "conversation:" + conversation
				request, err := observer.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})
				if err != nil {
					t.Fatal(err)
				}
				var frame protocol.Envelope
				for {
					frame, err = observer.Receive(h.ctx)
					if err != nil {
						t.Fatal(err)
					}
					if frame.ReplyTo != nil && *frame.ReplyTo == request {
						break
					}
				}
				history, ok := frame.Payload.(protocol.Snapshot)
				if !ok || history.Topic != topic {
					t.Fatalf("expected snapshot, got %s", frame.Kind)
				}
				var messages []message
				for _, row := range history.Items {
					if row.Kind != protocol.KindItemDone {
						continue
					}
					var item protocol.ItemDone
					if err := json.Unmarshal(row.Payload, &item); err != nil {
						t.Fatal(err)
					}
					if item.Text == nil || item.Speaker == nil {
						t.Fatal("message lost content or speaker")
					}
					messages = append(messages, message{row.Key, *item.Text, *item.Speaker})
				}
				return messages
			}
			live := snapshot()
			var texts []string
			for _, item := range live {
				texts = append(texts, item.Text)
			}
			if !reflect.DeepEqual(texts, want) {
				t.Fatalf("live snapshot=%q, want=%q", texts, want)
			}
			assertScreen("live TUI")
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			observer.Close()
			h.stopCore(core)
			core = start(true)
			observer, conversation = conversationClient(t, h)
			restored := snapshot()
			t.Logf("live=%+v; restored=%+v; provider_calls=%d", live, restored, calls.Load())
			if !reflect.DeepEqual(restored, live) {
				t.Error("restored history differs from live order, identities or content")
			}
			terminal = h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, scenario.Width, scenario.Height)
			assertScreen("restored TUI")
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			observer.Close()
			h.stopCore(core)
			if calls.Load() != 2 {
				t.Fatalf("provider calls=%d, want 2", calls.Load())
			}
		})
	}
}
