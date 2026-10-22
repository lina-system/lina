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

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIInterruptedReplySurvivesCoreRestart(t *testing.T) {
	var scenario struct {
		Input     string `json:"input"`
		Partial   string `json:"partial"`
		NextInput string `json:"next_input"`
		Reply     string `json:"reply"`
	}
	data, err := os.ReadFile("m1/tui-interrupted-reply.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			if !strings.Contains(string(mustJSON(t, body.Input)), scenario.Input) {
				t.Error("typed input missing from request")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", scenario.Partial)
			w.(http.Flusher).Flush()
			<-r.Context().Done() // Esc cancels the held response; no timed completion.
			return
		}
		if strings.Contains(string(mustJSON(t, body.Input)), scenario.Partial) {
			t.Error("aborted partial entered model context")
		}
		emitResponse(w, scenario.Reply, true)
	}))
	t.Cleanup(provider.Close)
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	terminal, _ := h.startConversationTUI(80, 24, "TERM=xterm-256color")
	waitScreen(t, h, terminal, "Ctrl+D")
	h.keys(terminal, scenario.Input+"\r")
	waitScreen(t, h, terminal, scenario.Partial)
	h.keys(terminal, "\x1b")
	var stopped string
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && item.EchoKey == nil && (item.Speaker == nil || *item.Speaker == protocol.ConversationSpeakerSystem) {
			stopped = *item.Text
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	if stopped == "" {
		t.Fatal("stop produced no outcome notice")
	}
	waitScreen(t, h, terminal, stopped)
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	terminal = h.startScreenTUI("TERM=xterm-256color")
	waitScreen(t, h, terminal, scenario.Partial)
	waitScreen(t, h, terminal, stopped)
	t.Log("Esc and TUI-only reopen retain partial and stopped outcome")

	assertStored := func(c *clientapi.Client) {
		t.Helper()
		topic := "conversation:" + conversation
		if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, ok := frame.Payload.(protocol.Snapshot)
		if !ok {
			t.Fatalf("expected snapshot, got %s", frame.Kind)
		}
		counts := map[string]int{}
		for _, row := range snapshot.Items {
			if row.Kind != protocol.KindItemDone {
				continue
			}
			var item protocol.ItemDone
			if err := json.Unmarshal(row.Payload, &item); err != nil {
				t.Fatal(err)
			}
			if item.Text != nil {
				counts[*item.Text]++
			}
		}
		if counts[scenario.Input] != 1 || counts[scenario.Partial] != 1 || counts[stopped] != 1 {
			t.Fatalf("after Core restart: input=%d partial=%d stopped=%d; want 1 each", counts[scenario.Input], counts[scenario.Partial], counts[stopped])
		}
	}
	for _, mode := range []string{"graceful", "forced"} {
		before := c.Welcome.Epoch
		c.Close()
		if mode == "graceful" {
			h.stopCore(core)
		} else {
			core.killAndWait()
		}
		waitScreen(t, h, terminal, design.Text(design.English, "retrying"))
		core = start(true)
		c, _ = conversationClient(t, h)
		if c.Welcome.Epoch == before {
			t.Fatal("restart did not advance epoch")
		}
		assertStored(c)
		waitScreen(t, h, terminal, design.Text(design.English, "live"))
		waitScreen(t, h, terminal, scenario.Partial)
		waitScreen(t, h, terminal, stopped)
		if calls.Load() != 1 {
			t.Fatalf("restart replayed model request: calls=%d", calls.Load())
		}
		t.Logf("%s restart: stored partial and stopped outcome restored once; model calls=1", mode)
	}
	admittedTurn(t, h, c, conversation, "next", scenario.NextInput)
	waitScreen(t, h, terminal, scenario.Reply)
	if calls.Load() != 2 {
		t.Fatalf("model calls=%d, want 2 including explicit follow-up", calls.Load())
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	c.Close()
	h.stopCore(core)
}
