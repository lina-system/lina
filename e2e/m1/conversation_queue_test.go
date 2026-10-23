// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// Queue two inputs while a tool-using turn is held. Its completed reply pushes
// earlier history past the compaction threshold. Both queued turns must finish
// separately, retaining the tool transcript and excluding later queued input.
func TestM1QueuedConversationPreparation(t *testing.T) {
	t.Parallel()
	const firstInput = "List the available skills."
	const queuedInput = "Summarize that catalog."
	const firstReply = "The skill catalog is available."
	const queuedReply = "The catalog includes persona editing."
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var calls, summaries atomic.Int32
	var seeded atomic.Bool
	var previousTools []json.RawMessage
	var transcriptMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input        []json.RawMessage
			Instructions string
			Tools        []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		body := string(mustJSON(t, request.Input))
		var last struct{ Role, Content string }
		if len(request.Input) > 0 {
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &last)
		}
		if last.Role == "developer" && strings.Contains(last.Content, "unanswered_requests") && strings.Contains(last.Content, "omitted_detail") {
			summaries.Add(1)
			if !strings.Contains(body, "Archived source") || strings.Contains(body, firstInput) || strings.Contains(body, queuedInput) || strings.Contains(body, "Another queued input") {
				t.Error("summary crossed the retained or queued input boundary")
			}
			emitResponse(w, `{"unanswered_requests":"none","topics_and_conclusions":"Archive","completed_actions":"Archive recorded","specific_values":"receipt 42","omitted_detail":"Archive padding"}`, true)
			return
		}
		if !seeded.Swap(true) {
			// Leave room for the next turn, then let its reply cross the
			// compaction threshold. Count the same complete request as Core.
			size := len(request.Instructions)
			for _, items := range [][]json.RawMessage{request.Input, request.Tools} {
				for _, item := range items {
					size += len(item)
				}
			}
			if size >= 108000 {
				t.Error("archive fixture exceeds its initial budget")
				return
			}
			emitResponse(w, "Archived reply "+strings.Repeat("a", 108000-size), true)
			return
		}
		n := calls.Add(1)
		transcriptMu.Lock()
		defer transcriptMu.Unlock()
		if len(instructionBlock(request.Instructions, "persona")) == 0 {
			t.Error("request lost its prepared persona")
		}
		if n <= 2 && (strings.Contains(body, queuedInput) || strings.Contains(body, "Another queued input")) {
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
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"reasoning\",\"id\":\"reasoning-catalog\",\"encrypted_content\":\"opaque-catalog\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"queued-catalog\",\"name\":\"skill_index\",\"arguments\":\"{}\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
		case 2, 3, 4, 5:
			if n == 2 {
				toolResult := false
				for _, raw := range request.Input {
					var item struct {
						Type, Output string
						CallID       string `json:"call_id"`
					}
					if json.Unmarshal(raw, &item) == nil && (item.Type == "reasoning" || item.Type == "function_call" || item.Type == "function_call_output") {
						previousTools = append(previousTools, append(json.RawMessage(nil), raw...))
					}
					if json.Unmarshal(raw, &item) == nil && item.Type == "function_call_output" && item.CallID == "queued-catalog" {
						toolResult = strings.Contains(item.Output, "persona-change")
					}
				}
				if !toolResult {
					t.Error("first turn did not receive the executed skill tool result")
				}
				emitResponse(w, firstReply+strings.Repeat("b", 12000), true)
				return
			}
			if len(previousTools) != 3 {
				t.Errorf("first follow-up has %d tool/reasoning items, want 3", len(previousTools))
			}
			position := -1
			for _, want := range previousTools {
				matches := 0
				for index, raw := range request.Input {
					if bytes.Equal(raw, want) {
						matches++
						if index <= position {
							t.Error("prior tool transcript changed order")
						}
						position = index
					}
				}
				if matches != 1 {
					var item struct {
						Type   string
						CallID string `json:"call_id"`
					}
					_ = json.Unmarshal(want, &item)
					t.Errorf("request %d: prior %s %s occurs %d times, want 1", n, item.Type, item.CallID, matches)
				}
			}
			if summaries.Load() != 1 || strings.Contains(body, "Archived source") || !strings.Contains(body, "receipt 42") {
				t.Error("queued turn did not reuse its published summary")
			}
			if n == 5 {
				emitResponse(w, "The catalog survived restart.", true)
				return
			}
			if n == 4 {
				if strings.Count(body, "Another queued input") != 1 {
					t.Error("later queued input lost its own turn")
				}
				emitResponse(w, "Another reply.", true)
				return
			}
			if strings.Contains(body, "Another queued input") {
				t.Error("later queued input entered the current turn")
			}
			for _, text := range []string{firstInput, firstReply, queuedInput} {
				if strings.Count(body, text) != 1 {
					t.Errorf("queued request needs exactly one %q: %s", text, body)
				}
			}
			currentIndex := -1
			for i, raw := range request.Input {
				var current struct{ Role, Content string }
				if json.Unmarshal(raw, &current) == nil && current.Role == "user" && current.Content == queuedInput {
					currentIndex = i
				}
			}
			if currentIndex <= position || currentIndex == len(request.Input)-1 {
				t.Error("queued input must follow retained history and precede the context packet")
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
	admittedTurn(t, h, c, conversation, "archive", "Archived source "+strings.Repeat("a", 10000))
	admit("first", firstInput)
	select {
	case <-started:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	admit("queued", queuedInput)
	admit("another", "Another queued input")
	unblock()
	var replies []string
	for completed := 0; completed < 3; {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerAgent && item.Text != nil {
			replies = append(replies, *item.Text)
		}
		if result, ok := frame.Payload.(protocol.Result); ok && result.Text != nil {
			replies = append(replies, *result.Text)
		}
		if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
			completed++
		}
	}
	if !reflect.DeepEqual(replies, []string{firstReply + strings.Repeat("b", 12000), queuedReply, "Another reply."}) || calls.Load() != 4 {
		t.Fatalf("queued input did not receive its own reply: reply count=%d calls=%d summaries=%d", len(replies), calls.Load(), summaries.Load())
	}
	t.Logf("provider requests=%d summaries=%d; three separate replies retained prior reasoning, tool call and raw result", calls.Load(), summaries.Load())
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		var items, pairs int
		if err := tx.QueryRowContext(h.ctx, `SELECT
            (SELECT count(*) FROM conv_event WHERE conversation_id=? AND scaffold_kind='provider_item'),
            (SELECT count(*) FROM ev_rel r JOIN conv_event e ON e.id=r.event_id WHERE e.conversation_id=? AND r.kind='pairs_with')`, conversation, conversation).Scan(&items, &pairs); err != nil {
			return err
		}
		if items != 3 || pairs != 1 {
			t.Errorf("ledger transcript: items=%d pairs=%d, want 3 and 1", items, pairs)
		}
		var nodes, queuedCoverage int
		if err := tx.QueryRowContext(h.ctx, `SELECT
            (SELECT count(*) FROM sum_node WHERE conversation_id=? AND status='ready'),
            (SELECT count(*) FROM sum_cover c JOIN conv_event e ON e.id=c.event_id WHERE e.conversation_id=? AND e.speaker_kind='person' AND e.body IN (?,?))`, conversation, conversation, queuedInput, "Another queued input").Scan(&nodes, &queuedCoverage); err != nil {
			return err
		}
		if nodes != 1 || queuedCoverage != 0 {
			t.Errorf("summary proof: nodes=%d queued coverage=%d, want 1 and 0", nodes, queuedCoverage)
		}
		for _, key := range []string{"archive", "first", "queued", "another"} {
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
	core = start(true)
	c, conversation = conversationClient(t, h)
	admit("after-restart", "Recall the catalog after restart.")
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
			break
		}
	}
	t.Logf("restart provider requests=%d; prior tool transcript retained exactly once", calls.Load())
	if calls.Load() != 5 || summaries.Load() != 1 {
		t.Fatalf("restart provider requests=%d summaries=%d, want 5 and 1", calls.Load(), summaries.Load())
	}
	c.Close()
	h.stopCore(core)
}
