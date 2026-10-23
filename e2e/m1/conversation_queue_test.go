// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Admit the second input while the first provider request is held. Core must
// finish the first turn's real skill tool call and reply, then prepare a separate
// turn with the queued input once, retaining the first input and its late reply.
func TestM1QueuedConversationPreparation(t *testing.T) {
	t.Parallel()
	const firstInput = "List the available skills."
	const queuedInput = "Summarize that catalog."
	const firstReply = "The skill catalog is available."
	const queuedReply = "The catalog includes persona editing."
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input        []json.RawMessage
			Instructions string
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		if len(instructionBlock(request.Instructions, "persona")) == 0 {
			t.Error("request lost its prepared persona")
		}
		body := string(mustJSON(t, request.Input))
		if n <= 2 && strings.Contains(body, queuedInput) {
			t.Error("queued input entered the preceding turn")
		}
		switch n {
		case 1:
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			emitPersonaCall(w, "queued-catalog", "skill_index", map[string]any{})
		case 2, 3:
			if n == 2 {
				toolResult := false
				for _, raw := range request.Input {
					var item struct {
						Type, Output string
						CallID       string `json:"call_id"`
					}
					if json.Unmarshal(raw, &item) == nil && item.Type == "function_call_output" && item.CallID == "queued-catalog" {
						toolResult = strings.Contains(item.Output, "persona-change")
					}
				}
				if !toolResult {
					t.Error("first turn did not receive the executed skill tool result")
				}
				emitResponse(w, firstReply, true)
				return
			}
			for _, text := range []string{firstInput, firstReply, queuedInput} {
				if strings.Count(body, text) != 1 {
					t.Errorf("queued request needs exactly one %q: %s", text, body)
				}
			}
			var current struct{ Role, Content string }
			if len(request.Input) == 0 || json.Unmarshal(request.Input[len(request.Input)-1], &current) != nil || current.Role != "user" || current.Content != queuedInput {
				t.Error("queued input is not in the current-input slot")
			}
			emitResponse(w, queuedReply, true)
		default:
			t.Error("unexpected provider request")
			emitResponse(w, "Unexpected request.", true)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admit := func(key, text string) {
		t.Helper()
		mode := protocol.InputModeQueue
		id, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text, Mode: &mode})
		if err != nil {
			t.Fatal(err)
		}
		for {
			frame, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.ReplyTo == nil || *frame.ReplyTo != id {
				continue
			}
			item, ok := frame.Payload.(protocol.ItemDone)
			if frame.Error != nil || frame.EffectState != protocol.EffectApplied || !ok || item.EchoKey == nil || *item.EchoKey != key {
				t.Fatalf("input was not durably admitted: %+v", frame)
			}
			return
		}
	}
	admit("first", firstInput)
	select {
	case <-started:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	admit("queued", queuedInput)
	unblock()
	var replies []string
	for completed := 0; completed < 2; {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerAgent && item.Text != nil {
			replies = append(replies, *item.Text)
		}
		if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
			completed++
		}
	}
	if !reflect.DeepEqual(replies, []string{firstReply, queuedReply}) || calls.Load() != 3 {
		t.Fatalf("queued input did not receive its own reply: replies=%q calls=%d", replies, calls.Load())
	}
	t.Logf("provider requests=%d; separate replies=%q; queued context retained the first input and late reply", calls.Load(), replies)
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		for _, key := range []string{"first", "queued"} {
			var count int
			if err := tx.QueryRowContext(h.ctx, `SELECT count(*) FROM inbox i JOIN turn_runs t ON t.inbox_id=i.id WHERE i.conversation=? AND i.source='client' AND i.native_id=? AND t.state='completed'`, conversation, key).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				t.Errorf("input %s has %d completed turns", key, count)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	h.stopCore(core)
}
