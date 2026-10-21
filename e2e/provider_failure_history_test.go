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

	"github.com/thisisjun786/lina/kit/protocol"
)

// Exercise the installed Core and real TUI, including both failures from #358.
// Capture the notice from the protocol instead of pinning its wording.
func TestM1ProviderFailureHistory(t *testing.T) {
	var scenario struct {
		UnavailableInput string `json:"unavailable_input"`
		TruncatedInput   string `json:"truncated_input"`
		Partial          string `json:"partial"`
		NextInput        string `json:"next_input"`
		Reply            string `json:"reply"`
	}
	data, err := os.ReadFile("m1/provider-failure-history.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, truncated := range []bool{false, true} {
		name, input := "unavailable", scenario.UnavailableInput
		if truncated {
			name, input = "truncated", scenario.TruncatedInput
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Input []json.RawMessage }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if calls.Add(1) > 1 {
					if strings.Contains(string(mustJSON(t, body.Input)), scenario.Partial) {
						t.Error("failed partial entered subsequent model context")
					}
					emitResponse(w, scenario.Reply, true)
					return
				}
				if !truncated {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", scenario.Partial)
				w.(http.Flusher).Flush()
				// EOF without response.completed must remain a failed turn.
			}))
			defer server.Close()
			h, start := installedConversationHarness(t, server.URL)
			core := start(true)
			client, conversation := conversationClient(t, h)
			terminal := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 120, 60)
			waitScreen(t, h, terminal, "Ctrl+D")
			h.keys(terminal, input+"\r")
			var notice string
			for {
				frame, err := client.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text != input && *item.Text != scenario.Partial {
					notice = *item.Text
				}
				if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
					break
				}
			}
			if notice == "" {
				t.Fatal("failed turn has no visible outcome")
			}
			waitScreen(t, h, terminal, notice)
			if truncated {
				waitScreen(t, h, terminal, scenario.Partial)
			}
			t.Logf("before restart: input=true notice=true partial=%t provider_calls=%d", truncated, calls.Load())
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			client.Close()
			h.stopCore(core)
			core = start(true)
			client, conversation = conversationClient(t, h)
			topic := "conversation:" + conversation
			if _, err = client.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := client.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			restored := string(mustJSON(t, snapshot))
			hasNotice, hasPartial := strings.Contains(restored, notice), strings.Contains(restored, scenario.Partial)
			t.Logf("after restart: input=%t notice=%t partial=%t provider_calls=%d", strings.Contains(restored, input), hasNotice, hasPartial, calls.Load())
			if !hasNotice || truncated && !hasPartial {
				t.Fatalf("failed outcome lost after restart: notice=%t partial=%t", hasNotice, hasPartial)
			}
			again := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 120, 60)
			waitScreen(t, h, again, notice)
			if truncated {
				waitScreen(t, h, again, scenario.Partial)
			}
			if calls.Load() != 1 {
				t.Fatalf("restart replayed failed request: calls=%d", calls.Load())
			}
			admittedTurn(t, h, client, conversation, "next", scenario.NextInput)
			waitScreen(t, h, again, scenario.Reply)
			h.keys(again, "\x04")
			again.process.wait(t, 0)
			h.expectTerminalRestored(again)
			client.Close()
			h.stopCore(core)
			if calls.Load() != 2 {
				t.Fatalf("expected one failed and one new request: calls=%d", calls.Load())
			}
		})
	}
}
