// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/protocol"
	"html"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAskSettingsInstalledCore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var questionID, recordID, recordRevision string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			preferences := false
			for _, raw := range request.Input {
				var item struct{ Content string }
				if json.Unmarshal(raw, &item) == nil && strings.Contains(html.UnescapeString(item.Content), `"kind":"working_preference"`) && strings.Contains(html.UnescapeString(item.Content), `"per_turn":1`) && strings.Contains(html.UnescapeString(item.Content), `"unanswered":"default"`) {
					preferences = true
				}
			}
			if !preferences {
				t.Error("ask values absent from ContextPacket")
			}
			found := false
			for _, raw := range request.Tools {
				var tool struct{ Name string }
				_ = json.Unmarshal(raw, &tool)
				found = found || tool.Name == "ask_question"
			}
			if !found {
				t.Error("running Core does not offer ask_question")
			}
			emitPersonaCall(w, "first", "ask_question", map[string]any{"question": "Which draft format?", "recommendation": "Use a table", "choices": []string{"table", "list"}, "default": "table"})
		case 2:
			raw := assertAskResult(t, request.Input, "first", "ok", "")
			var q waiting.ConversationQuestion
			if json.Unmarshal(raw, &q) != nil || q.ID == "" {
				t.Error("question receipt missing identity")
			}
			questionID = q.ID
			emitPersonaCall(w, "excess", "ask_question", map[string]any{"question": "Which title?", "recommendation": "Use Summary", "default": "Summary"})
		case 3:
			assertAskResult(t, request.Input, "excess", "invalid_arguments", "question_limit")
			emitResponse(w, "I recommend a table.", true)
		case 4:
			emitPersonaCall(w, "related", "resolve_question", map[string]any{"id": questionID, "kind": "related", "quote": "Why a table?"})
		case 5:
			var q waiting.ConversationQuestion
			_ = json.Unmarshal(assertAskResult(t, request.Input, "related", "ok", ""), &q)
			if q.Resolution != nil {
				t.Error("related follow-up applied a default")
			}
			emitResponse(w, "A table makes the comparison easier to read.", true)
		case 6:
			emitPersonaCall(w, "too-early", "write_record", map[string]any{"id": "", "revision": "", "kind": "memo", "title": "Premature", "note": "Must not be saved"})
		case 7:
			assertAskResult(t, request.Input, "too-early", "invalid_arguments", "pending_question_needs_resolution")
			emitPersonaCall(w, "pass", "resolve_question", map[string]any{"id": questionID, "kind": "default_applied", "quote": "Also tell me the time."})
		case 8:
			var q waiting.ConversationQuestion
			_ = json.Unmarshal(assertAskResult(t, request.Input, "pass", "ok", ""), &q)
			if q.Resolution == nil || q.Resolution.Kind != "default_applied" || q.Resolution.Value != "table" {
				t.Error("stored recommendation was not applied")
			}
			emitPersonaCall(w, "draft", "write_record", map[string]any{"id": "", "revision": "", "kind": "memo", "title": "Format draft", "note": "| Format | Status |\n| --- | --- |\n| table | Selected |"})
		case 9:
			var result struct{ ID, Revision string }
			_ = json.Unmarshal(assertAskResult(t, request.Input, "draft", "ok", ""), &result)
			if result.ID == "" || result.Revision == "" {
				t.Error("default did not produce a saved result")
			}
			recordID, recordRevision = result.ID, result.Revision
			emitPersonaCall(w, "read-draft", "read_record", map[string]any{"id": recordID})
		case 10:
			raw := assertAskResult(t, request.Input, "read-draft", "ok", "")
			if !strings.Contains(string(raw), "table") || !strings.Contains(string(raw), recordRevision) {
				t.Errorf("draft readback: %s", raw)
			}
			emitResponse(w, "The table draft is saved and checked.", true)
		default:
			t.Error("unexpected model request")
			emitResponse(w, "Failed.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	c, conversation := conversationClient(t, h)
	defer c.Close()
	admittedTurn(t, h, c, conversation, "ask-format", "Prepare a draft; ask about its format.")
	if calls.Load() != 3 {
		t.Fatalf("first turn requests=%d", calls.Load())
	}
	assertQuestionState := func(closed bool) {
		t.Helper()
		db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var questions, resolutions, notices int
		for query, target := range map[string]*int{
			"SELECT count(*) FROM conversation_questions":                            &questions,
			"SELECT count(*) FROM conversation_question_resolutions":                 &resolutions,
			"SELECT count(*) FROM conv_event WHERE scaffold_kind='question_default'": &notices,
		} {
			if err = db.QueryRowContext(h.ctx, query).Scan(target); err != nil {
				t.Fatal(err)
			}
		}
		expected := 0
		if closed {
			expected = 1
		}
		if questions != 1 || resolutions != expected || notices != expected {
			t.Fatalf("questions=%d resolutions=%d notices=%d closed=%v", questions, resolutions, notices, closed)
		}
		if closed {
			var raw []byte
			if err = db.QueryRowContext(h.ctx, "SELECT record FROM conversation_question_resolutions WHERE question=?", questionID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var d waiting.QuestionResolution
			_ = json.Unmarshal(raw, &d)
			var speaker, person string
			if err = db.QueryRowContext(h.ctx, "SELECT speaker_kind,person_id FROM conv_event WHERE id=?", d.Input).Scan(&speaker, &person); err != nil {
				t.Fatal(err)
			}
			if speaker != "person" || person != c.Welcome.PersonID || d.Kind != "default_applied" || d.Value != "table" {
				t.Fatalf("resolution attribution: %+v", d)
			}
		}
	}
	assertQuestionState(false)
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	defer c.Close()
	// A fresh waiting subscription restores the durable card after restart.
	topic := "waiting"
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := c.Receive(h.ctx)
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if err != nil || !ok {
		t.Fatalf("question restore: %+v %v", frame, err)
	}
	found := false
	for _, item := range snapshot.Items {
		var q protocol.Question
		if item.Kind == protocol.KindQuestion && json.Unmarshal(item.Payload, &q) == nil && q.Card != nil && q.Card.ID == questionID && q.Card.Outcome == nil {
			found = true
		}
	}
	if !found {
		t.Fatal("pending question card missing after restart")
	}
	admittedTurn(t, h, c, conversation, "related-format", "Why a table?")
	assertQuestionState(false)
	frames := admittedTurn(t, h, c, conversation, "move-on", "Also tell me the time.")
	notice := false
	for _, frame := range frames {
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerSystem && item.Text != nil && strings.Contains(*item.Text, "table") {
			notice = true
		}
	}
	if !notice {
		t.Error("default notice did not reach the conversation")
	}
	assertQuestionState(true)
	c.Close()
	h.stopCore(core)
	core = start(true)
	// Read a fresh subscription directly to inspect the restored default notice.
	restored, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "question-history", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	historyTopic := "conversation:" + conversation
	if _, err = restored.Subscribe(h.ctx, protocol.Subscribe{Topic: &historyTopic}); err != nil {
		t.Fatal(err)
	}
	frame, err = restored.Receive(h.ctx)
	snapshot, ok = frame.Payload.(protocol.Snapshot)
	if err != nil || !ok {
		t.Fatalf("restored history: %+v %v", frame, err)
	}
	found = false
	for _, entry := range snapshot.Items {
		var item protocol.ItemDone
		if entry.Kind == protocol.KindItemDone && json.Unmarshal(entry.Payload, &item) == nil && item.Speaker != nil && *item.Speaker == protocol.ConversationSpeakerSystem && item.Text != nil && strings.Contains(*item.Text, "table") {
			found = true
		}
	}
	if !found {
		t.Fatal("default notice lost after restart")
	}
	assertQuestionState(true)
	if calls.Load() != 10 {
		t.Fatalf("requests=%d, want 10", calls.Load())
	}
}

func assertAskResult(t *testing.T, items []json.RawMessage, id, code, detail string) json.RawMessage {
	t.Helper()
	for _, raw := range items {
		var wire struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		}
		if json.Unmarshal(raw, &wire) != nil {
			continue
		}
		if wire.Type != "function_call_output" || wire.CallID != id {
			continue
		}
		var result struct {
			Code   string
			Output json.RawMessage
		}
		if err := json.Unmarshal([]byte(wire.Output), &result); err != nil || result.Code != code {
			t.Errorf("%s result=%s, want %s (%v)", id, wire.Output, code, err)
		}
		if detail != "" {
			var body struct{ Code string }
			_ = json.Unmarshal(result.Output, &body)
			if body.Code != detail {
				t.Errorf("%s detail=%s, want %s", id, result.Output, detail)
			}
		}
		return result.Output
	}
	t.Errorf("missing tool result %s", id)
	return nil
}

func TestQuestionAnswerInstalledCore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	questions := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "ask", "ask_question", map[string]any{"question": "Which format?", "recommendation": "Use a table", "choices": []string{"table", "list"}, "default": "table"})
		case 2:
			var q waiting.ConversationQuestion
			_ = json.Unmarshal(assertAskResult(t, request.Input, "ask", "ok", ""), &q)
			questions <- q.ID
			emitResponse(w, "I recommend a table.", true)
		case 3:
			found := false
			for _, raw := range request.Input {
				var item struct{ Content string }
				if json.Unmarshal(raw, &item) == nil && strings.Contains(item.Content, "Which format?") && strings.Contains(item.Content, "list") {
					found = true
				}
			}
			if !found {
				t.Error("card answer did not reach resumed model turn")
			}
			emitPersonaCall(w, "save-answer", "write_record", map[string]any{"id": "", "revision": "", "kind": "memo", "title": "Chosen format", "note": "list"})
		case 4:
			assertAskResult(t, request.Input, "save-answer", "ok", "")
			emitResponse(w, "The list draft is saved.", true)
		default:
			t.Error("duplicate answer delivery")
			emitResponse(w, "Failed.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	defer c.Close()
	admittedTurn(t, h, c, conversation, "draft", "Prepare a draft")
	id := <-questions
	if id == "" {
		t.Fatal("no question identity")
	}
	revision, value := "1", "list"
	values := protocol.FormValues{"answer": {String: &value}}
	answer := protocol.QuestionAnswer{CardID: &id, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}
	if _, err := c.AnswerQuestion(h.ctx, answer); err != nil {
		t.Fatal(err)
	}
	answered, finished := false, false
	for !answered || !finished {
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("answer delivery: %+v %v", frame, err)
		}
		if a, ok := frame.Payload.(protocol.QuestionAnswer); ok {
			answered = a.Outcome != nil && *a.Outcome == protocol.DecisionOutcomeResolved
		}
		if turn, ok := frame.Payload.(protocol.TurnState); ok && turn.Active != nil && !*turn.Active {
			finished = true
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("resumed requests=%d", calls.Load())
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	defer c.Close()
	if calls.Load() != 4 {
		t.Fatal("completed answer replayed after restart")
	}
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var deliveries int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM inbox i JOIN turn_runs t ON t.inbox_id=i.id WHERE i.source='question-answer' AND t.state='completed'").Scan(&deliveries); err != nil || deliveries != 1 {
		t.Fatalf("durable answered turn=%d %v", deliveries, err)
	}
}
