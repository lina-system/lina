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
	"github.com/thisisjun786/lina/kit/protocol"
)

func conversationClient(t *testing.T, h *harness, extra ...protocol.Capability) (*clientapi.Client, string) {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", append([]protocol.Capability{{Name: "conversation", Version: "1.0"}}, extra...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if _, err = c.History(h.ctx, protocol.HistoryGet{}); err != nil {
		t.Fatal(err)
	}
	e, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	history, ok := e.Payload.(protocol.History)
	if !ok || history.ConversationID == nil {
		t.Fatal("no conversation")
	}
	topic := "conversation:" + *history.ConversationID
	if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := c.Receive(h.ctx); err != nil || snapshot.Kind != protocol.KindSnapshot {
		t.Fatal("no history snapshot")
	}
	// Empty input probes admission without creating a message. Only recovery's
	// explicit refusal permits another probe.
	for {
		if _, err = c.Input(h.ctx, protocol.Input{}); err != nil {
			t.Fatal(err)
		}
		ready, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ready.Error != nil && ready.Error.Code == protocol.ErrorMalformed {
			break
		}
		if ready.Error == nil || ready.Error.Code != protocol.ErrorUnavailable {
			t.Fatal("unexpected readiness refusal")
		}
	}
	return c, *history.ConversationID
}
func admittedTurn(t *testing.T, h *harness, c *clientapi.Client, conversation, key, text string) []protocol.Envelope {
	t.Helper()
	for {
		id, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text})
		if err != nil {
			t.Fatal(err)
		}
		ack, done, retry := false, false, false
		var frames []protocol.Envelope
		for !ack || !done {
			e, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, e)
			if e.ReplyTo != nil && *e.ReplyTo == id {
				if e.Error != nil {

					t.Fatalf("input admission refused: %s", e.Error.Code)
				}
				ack = true
			}
			if turn, ok := e.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
				done = true
			}
		}
		if !retry {
			return frames
		}
	}
}
func emitResponse(w http.ResponseWriter, text string, complete bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n\n", text)
	if complete {
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}
}

func TestM1ConversationLargeUnicodeAndMasking(t *testing.T) {
	var scenario struct {
		Runes         int
		Secret, Reply string
	}
	data, err := os.ReadFile("conversation-admission.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("한", scenario.Runes)
	scenario.Reply += strings.Repeat("라", scenario.Runes)
	var received atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		body := string(mustJSON(t, input.Input))
		if !strings.Contains(body, large) || strings.Contains(body, scenario.Secret) {
			t.Error("large masked input was not preserved safely")
		}
		received.Store(true)
		emitResponse(w, scenario.Reply, true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	frames := admittedTurn(t, h, c, conversation, "large", large+" password="+scenario.Secret)
	found := false
	for _, e := range frames {
		if item, ok := e.Payload.(protocol.Result); ok && item.Type == protocol.ResultTypeDocumentDraft && item.Text != nil && *item.Text == scenario.Reply && item.Summary == "" {
			found = true
		}
	}
	if !received.Load() || !found {
		t.Fatal("valid Unicode input did not complete")
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	if _, err = c.History(h.ctx, protocol.HistoryGet{ConversationID: &conversation}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Receive(h.ctx); err != nil {
		t.Fatal(err)
	}
	topic := "conversation:" + conversation
	if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(mustJSON(t, snapshot))
	if !strings.Contains(encoded, large) || strings.Contains(encoded, scenario.Secret) {
		t.Fatal("material recovery lost large text or exposed a secret")
	}
	c.Close()
	h.stopCore(core)
}

func TestM1ConversationFailedResponseIsNotFinalHistory(t *testing.T) {
	var scenario struct{ Input, FailedReply, NextInput, Reply string }
	data, err := os.ReadFile("conversation-failure.json")
	if err != nil {
		t.Fatal(err)
	}
	// Explicit tags keep the fixture's wire-style names.
	var wire struct {
		Input       string `json:"input"`
		FailedReply string `json:"failed_reply"`
		NextInput   string `json:"next_input"`
		Reply       string `json:"reply"`
	}
	if err = json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	scenario.Input, scenario.FailedReply, scenario.NextInput, scenario.Reply = wire.Input, wire.FailedReply, wire.NextInput, wire.Reply
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			emitResponse(w, scenario.FailedReply, false)
			return
		}
		if strings.Contains(string(mustJSON(t, body.Input)), scenario.FailedReply) {
			t.Error("failed reply entered subsequent model context")
		}
		emitResponse(w, scenario.Reply, true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	frames := admittedTurn(t, h, c, conversation, "failed", scenario.Input)
	for _, e := range frames {
		if item, ok := e.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text == scenario.FailedReply {
			t.Fatal("failed response was published as a completed reply")
		}
	}
	admittedTurn(t, h, c, conversation, "next", scenario.NextInput)
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	topic := "conversation:" + conversation
	if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustJSON(t, snapshot)), scenario.FailedReply) {
		t.Fatal("failed response was restored as final history")
	}
	c.Close()
	h.stopCore(core)
}
