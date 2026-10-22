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
			terminal, _ := h.startConversationTUI(120, 60, "TERM=xterm-256color")
			waitComposerReady(t, h, terminal)
			h.keys(terminal, input+"\r")
			var notice, noticeKey, partialKey string
			for {
				frame, err := client.Receive(h.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text != input && *item.Text != scenario.Partial {
					notice = *item.Text
					if item.Stream != nil {
						noticeKey = item.Stream.Key
					}
				}
				if delta, ok := frame.Payload.(protocol.OutputDelta); ok && delta.Stream != nil {
					partialKey = delta.Stream.Key
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
			before := client.Welcome.Epoch
			client.Close()
			h.stopCore(core)
			waitScreen(t, h, terminal, design.Text(design.English, "retrying"))
			core = start(true)
			client, conversation = conversationClient(t, h)
			if client.Welcome.Epoch == before {
				t.Fatal("restart did not advance epoch")
			}
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
			history, ok := snapshot.Payload.(protocol.Snapshot)
			if !ok {
				t.Fatalf("expected history snapshot, got %s", snapshot.Kind)
			}
			counts := map[string]int{}
			for _, row := range history.Items {
				if row.Kind != protocol.KindItemDone {
					continue
				}
				var item protocol.ItemDone
				if err := json.Unmarshal(row.Payload, &item); err != nil {
					t.Fatal(err)
				}
				if item.Text == nil {
					continue
				}
				counts[*item.Text]++
				if *item.Text == notice && (row.Key != noticeKey || item.Speaker == nil || *item.Speaker != protocol.ConversationSpeakerSystem) {
					t.Fatal("failure notice changed identity or lost its system speaker")
				}
				if *item.Text == scenario.Partial && row.Key != partialKey {
					t.Fatal("partial did not converge on the original streamed block")
				}
			}
			if counts[input] != 1 || counts[notice] != 1 || truncated && counts[scenario.Partial] != 1 {
				t.Fatalf("restored history contains missing or duplicate outcomes: %v", counts)
			}
			waitScreen(t, h, terminal, design.Text(design.English, "live"))
			waitScreen(t, h, terminal, notice)
			if truncated {
				waitScreen(t, h, terminal, scenario.Partial)
			}
			if calls.Load() != 1 {
				t.Fatalf("restart replayed failed request: calls=%d", calls.Load())
			}
			admittedTurn(t, h, client, conversation, "next", scenario.NextInput)
			waitScreen(t, h, terminal, scenario.Reply)
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			client.Close()
			h.stopCore(core)
			if calls.Load() != 2 {
				t.Fatalf("expected one failed and one new request: calls=%d", calls.Load())
			}
		})
	}
}
