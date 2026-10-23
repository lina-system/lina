// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestQuestionSteeringInstalledCore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	questions := make(chan string, 1)
	release := make(chan struct{})
	var questionID, recordID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "ask-format", "ask_question", map[string]any{"question": "Which format?", "recommendation": "Use a table", "choices": []string{"table", "list"}, "default": "table"})
		case 2:
			var q waiting.ConversationQuestion
			if err := json.Unmarshal(assertAskResult(t, request.Input, "ask-format", "ok", ""), &q); err != nil || q.ID == "" {
				t.Error("question receipt missing", err)
			}
			questionID = q.ID
			questions <- q.ID
			// Keep the asking turn active until the ordinary chat answer is admitted.
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			emitResponse(w, "I recommend a table.", true)
		case 3:
			foundAnswer, foundRelated := false, false
			for _, raw := range request.Input {
				var item struct{ Role, Content string }
				if json.Unmarshal(raw, &item) == nil && item.Role == "user" {
					foundAnswer = foundAnswer || strings.Contains(item.Content, "Use a list")
					foundRelated = foundRelated || strings.Contains(item.Content, "Why a table?")
				}
			}
			if !foundAnswer || !foundRelated {
				t.Error("both consumed inputs must reach the model")
			}
			emitPersonaCall(w, "earlier-related", "resolve_question", map[string]any{"id": questionID, "kind": "related", "quote": "Why a table?"})
		case 4:
			assertAskResult(t, request.Input, "earlier-related", "ok", "")
			emitPersonaCall(w, "premature-save", "write_record", map[string]any{"id": "", "revision": "", "kind": "memo", "title": "Premature draft", "note": "table"})
		case 5:
			assertAskResult(t, request.Input, "premature-save", "invalid_arguments", "pending_question_needs_resolution")
			emitPersonaCall(w, "answer-format", "resolve_question", map[string]any{"id": questionID, "kind": "answered", "quote": "Use a list", "value": "list"})
		case 6:
			var q waiting.ConversationQuestion
			_ = json.Unmarshal(assertAskResult(t, request.Input, "answer-format", "ok", ""), &q)
			if q.Resolution == nil || q.Resolution.Value != "list" {
				t.Error("live answer did not resolve the question")
			}
			emitPersonaCall(w, "save-list", "write_record", map[string]any{"id": "", "revision": "", "kind": "memo", "title": "Chosen draft", "note": "list"})
		case 7:
			var result struct{ ID string }
			_ = json.Unmarshal(assertAskResult(t, request.Input, "save-list", "ok", ""), &result)
			recordID = result.ID
			if recordID == "" {
				t.Error("no saved result")
			}
			emitPersonaCall(w, "read-list", "read_record", map[string]any{"id": recordID})
		case 8:
			if raw := assertAskResult(t, request.Input, "read-list", "ok", ""); !strings.Contains(string(raw), "list") {
				t.Errorf("saved draft readback: %s", raw)
			}
			emitResponse(w, "The list draft is saved and checked.", true)
		case 9:
			emitResponse(w, "The draft remains a list.", true)
		default:
			t.Error("unexpected model request")
			emitResponse(w, "Failed.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	defer c.Close()
	key, text := "draft", "Prepare a draft"
	if _, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-questions:
		if id == "" {
			t.Fatal("question missing")
		}
	case <-h.ctx.Done():
		t.Fatal("asking turn did not reach the provider barrier")
	}
	// Both inputs are durably admitted before the provider barrier releases,
	// so the loop consumes them together without a timing-dependent sleep.
	for _, input := range []struct{ key, text string }{{"live-related", "Why a table?"}, {"live-answer", "Use a list"}} {
		inputID, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &input.key, Text: &input.text})
		if err != nil {
			t.Fatal(err)
		}
		for {
			frame, err := c.Receive(h.ctx)
			if err != nil || frame.Error != nil {
				t.Fatalf("input admission: %+v %v", frame, err)
			}
			if frame.ReplyTo != nil && *frame.ReplyTo == inputID {
				break
			}
		}
	}
	close(release)
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("answer continuation: %+v %v", frame, err)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	if calls.Load() != 8 {
		t.Fatalf("requests=%d, want eight in one turn", calls.Load())
	}
	assertReceipt := func(turns int) {
		t.Helper()
		db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var raw []byte
		if err = db.QueryRowContext(h.ctx, "SELECT record FROM conversation_question_resolutions WHERE question=?", questionID).Scan(&raw); err != nil {
			t.Fatal("missing durable answer", err)
		}
		var decision waiting.QuestionResolution
		if err = json.Unmarshal(raw, &decision); err != nil {
			t.Fatal(err)
		}
		var body, person string
		if err = db.QueryRowContext(h.ctx, "SELECT body,person_id FROM conv_event WHERE id=?", decision.Input).Scan(&body, &person); err != nil {
			t.Fatal(err)
		}
		if decision.Kind != "answered" || decision.Value != "list" || body != "Use a list" || person != c.Welcome.PersonID {
			t.Fatalf("wrong answer provenance: %+v body=%q person=%q", decision, body, person)
		}
		var notices int
		if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM conv_event WHERE scaffold_kind='question_default'").Scan(&notices); err != nil || notices != 0 {
			t.Fatalf("contradictory defaults=%d error=%v", notices, err)
		}
		var completed int
		if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM turn_runs WHERE state='completed'").Scan(&completed); err != nil || completed != turns {
			t.Fatalf("completed turns=%d, want %d: %v", completed, turns, err)
		}
	}
	assertReceipt(1)
	topic := "waiting"
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := c.Receive(h.ctx)
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if err != nil || !ok {
		t.Fatalf("live cards: %+v %v", frame, err)
	}
	closed := false
	for _, item := range snapshot.Items {
		var q protocol.Question
		if item.Kind == protocol.KindQuestion && json.Unmarshal(item.Payload, &q) == nil && q.Card != nil && q.Card.ID == questionID {
			closed = q.Card.Outcome != nil && *q.Card.Outcome == protocol.DecisionOutcomeResolved
		}
	}
	if !closed {
		t.Fatal("live question card remained unresolved")
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	defer c.Close()
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err = c.Receive(h.ctx)
	snapshot, ok = frame.Payload.(protocol.Snapshot)
	if err != nil || !ok {
		t.Fatalf("restored cards: %+v %v", frame, err)
	}
	for _, item := range snapshot.Items {
		var q protocol.Question
		if item.Kind == protocol.KindQuestion && json.Unmarshal(item.Payload, &q) == nil && q.Card != nil && q.Card.ID == questionID && q.Card.Outcome == nil {
			t.Fatal("answered card reopened after restart")
		}
	}
	admittedTurn(t, h, c, conversation, "new-topic", "Tell me the time")
	assertReceipt(2)
	if calls.Load() != 9 {
		t.Fatalf("answer replayed after restart: requests=%d", calls.Load())
	}
}
