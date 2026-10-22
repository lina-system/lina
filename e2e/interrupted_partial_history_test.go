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
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

type partialHistoryRow struct {
	Key, Text string
	Speaker   protocol.ConversationSpeaker
}

// Compare visible rows, including identities and order, across the live delta
// and restored item_done representations. Stream cursors change on restart.
func partialHistory(t *testing.T, h *harness, client *clientapi.Client, conversation string) []partialHistoryRow {
	t.Helper()
	topic := "conversation:" + conversation
	if _, err := client.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, ok := frame.Payload.(protocol.Snapshot)
		if !ok {
			continue
		}
		var rows []partialHistoryRow
		for _, row := range snapshot.Items {
			if row.Kind != protocol.KindOutputDelta && row.Kind != protocol.KindItemDone {
				continue
			}
			var item protocol.ItemDone
			if err := json.Unmarshal(row.Payload, &item); err != nil {
				t.Fatal(err)
			}
			if item.Text == nil {
				t.Fatal("visible row has no text")
			}
			speaker := protocol.ConversationSpeakerAgent
			if item.Speaker != nil {
				speaker = *item.Speaker
			}
			rows = append(rows, partialHistoryRow{row.Key, *item.Text, speaker})
		}
		return rows
	}
}

func TestM1InterruptedPartialHistory(t *testing.T) {
	var scenario struct {
		Input         string
		Partials      []string
		ToolArguments string `json:"tool_arguments"`
		NextInput     string `json:"next_input"`
		Reply         string
	}
	data, err := os.ReadFile("m1/interrupted-partial-history.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, crash := range []bool{true, false} {
		name := "failed-tool-arguments"
		if crash {
			name = "kill-during-stream"
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
					for _, partial := range append([]string{scenario.ToolArguments}, scenario.Partials...) {
						if strings.Contains(string(mustJSON(t, body.Input)), partial) {
							t.Error("unfinished output entered subsequent model context")
						}
					}
					emitResponse(w, scenario.Reply, true)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for i, partial := range scenario.Partials {
					for _, chunk := range []string{partial[:5], partial[5:]} {
						fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":%d,\"delta\":%q}\n\n", i, chunk)
					}
				}
				if !crash {
					for i, kind := range []string{"response.function_call_arguments.delta", "response.custom_tool_call_input.delta"} {
						fmt.Fprintf(w, "data: {\"type\":%q,\"output_index\":%d,\"delta\":%q}\n\n", kind, i+2, scenario.ToolArguments)
					}
				}
				w.(http.Flusher).Flush()
				if crash {
					<-r.Context().Done() // Only SIGKILL closes this response.
				}
				// Otherwise fail with EOF, after text and both kinds of tool input.
			}))
			t.Cleanup(server.Close)
			h, start := installedConversationHarness(t, server.URL)
			core := start(true)
			client, conversation := conversationClient(t, h)
			terminal := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 120, 60)
			waitComposerReady(t, h, terminal)
			h.keys(terminal, scenario.Input+"\r")
			for _, partial := range scenario.Partials {
				waitScreen(t, h, terminal, partial)
			}
			if !crash {
				for {
					frame, err := client.Receive(h.ctx)
					if err != nil {
						t.Fatal(err)
					}
					if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
						break
					}
				}
			}
			live := partialHistory(t, h, client, conversation)
			wantCount := 1 + len(scenario.Partials)
			if !crash {
				wantCount++ // The failure outcome follows the visible text.
			}
			if len(live) != wantCount {
				t.Fatalf("live history contains missing or invented rows: got=%+v want_count=%d", live, wantCount)
			}
			for i, text := range append([]string{scenario.Input}, scenario.Partials...) {
				if live[i].Text != text {
					t.Fatalf("live order/content mismatch: %+v", live)
				}
			}
			t.Logf("before restart: %d exact visible rows, provider_calls=%d", len(live), calls.Load())
			before := client.Welcome.Epoch
			client.Close()
			if crash {
				core.killAndWait()
			} else {
				h.stopCore(core)
			}
			waitScreen(t, h, terminal, design.Text(design.English, "retrying"))
			core = start(true)
			client, conversation = conversationClient(t, h)
			if client.Welcome.Epoch == before {
				t.Fatal("restart did not advance epoch")
			}
			restored := partialHistory(t, h, client, conversation)
			if crash {
				if len(restored) != len(live)+1 || restored[len(restored)-1].Speaker != protocol.ConversationSpeakerSystem || restored[len(restored)-1].Text == "" {
					t.Fatalf("crash history must retain all live rows and append one interruption: live=%+v restored=%+v", live, restored)
				}
				live = append(live, restored[len(restored)-1])
			}
			if !reflect.DeepEqual(live, restored) {
				t.Fatalf("restart changed visible order, identity or content: live=%+v restored=%+v", live, restored)
			}
			waitScreen(t, h, terminal, design.Text(design.English, "live"))
			for _, row := range restored {
				waitScreen(t, h, terminal, row.Text)
			}
			if calls.Load() != 1 {
				t.Fatalf("restart replayed request: calls=%d", calls.Load())
			}
			admittedTurn(t, h, client, conversation, "next", scenario.NextInput)
			waitScreen(t, h, terminal, scenario.Reply)
			if calls.Load() != 2 {
				t.Fatalf("expected only the explicit follow-up: calls=%d", calls.Load())
			}
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			client.Close()
			h.stopCore(core)
		})
	}
}
