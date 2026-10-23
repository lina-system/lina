// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The installed Core executes record tools, binds a live option answer in the
// same turn, and preserves its receipt and focus across a process restart.
func TestM2InputBindingInstalledCore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var recordID, questionID string
	checkBinding := func(items []json.RawMessage, rule string) {
		t.Helper()
		found := false
		for _, raw := range items {
			var item struct{ Content string }
			_ = json.Unmarshal(raw, &item)
			if strings.Contains(html.UnescapeString(item.Content), `"decider":"`+rule+`"`) && strings.Contains(html.UnescapeString(item.Content), `"id":"`+recordID+`"`) {
				found = true
			}
		}
		if !found {
			t.Errorf("request missing %s binding for the saved to-do", rule)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "create", "write_record", map[string]any{"id": "", "revision": "", "kind": "todo", "title": "Garden", "note": "Plan the beds."})
		case 2:
			var result struct{ ID string }
			_ = json.Unmarshal(assertAskResult(t, request.Input, "create", "ok", ""), &result)
			recordID = result.ID
			if recordID == "" {
				t.Error("to-do was not created")
			}
			emitResponse(w, "The to-do is saved.", true)
		case 3:
			checkBinding(request.Input, "exact-reference")
			emitPersonaCall(w, "ask", "ask_question", map[string]any{"question": "Which format?", "recommendation": "Use a table", "choices": []string{"table", "list"}, "default": "table"})
		case 4:
			var q waiting.ConversationQuestion
			_ = json.Unmarshal(assertAskResult(t, request.Input, "ask", "ok", ""), &q)
			if q.ID == "" {
				t.Error("question missing")
			}
			questionID = q.ID
			close(ready)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			emitResponse(w, "Choose a format.", true)
		case 5:
			checkBinding(request.Input, "offered-option")
			emitPersonaCall(w, "resolve", "resolve_question", map[string]any{"id": questionID, "kind": "answered", "quote": "2", "value": "list"})
		case 6:
			var q waiting.ConversationQuestion
			_ = json.Unmarshal(assertAskResult(t, request.Input, "resolve", "ok", ""), &q)
			if q.Resolution == nil || q.Resolution.Value != "list" {
				t.Error("option did not resolve question")
			}
			emitPersonaCall(w, "read-live", "read_record", map[string]any{"id": recordID})
		case 7:
			if !strings.Contains(string(assertAskResult(t, request.Input, "read-live", "ok", "")), "Garden") {
				t.Error("wrong to-do read")
			}
			emitResponse(w, "The to-do is checked.", true)
		case 8:
			checkBinding(request.Input, "reply-or-card")
			emitPersonaCall(w, "read-restored", "read_record", map[string]any{"id": recordID})
		case 9:
			if !strings.Contains(string(assertAskResult(t, request.Input, "read-restored", "ok", "")), "Garden") {
				t.Error("to-do did not survive restart")
			}
			emitResponse(w, "The to-do is still available.", true)
		default:
			t.Error("unexpected model call")
			emitResponse(w, "Finished.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	c, conversation := conversationClient(t, h)
	defer c.Close()
	admittedTurn(t, h, c, conversation, "create", "Create a to-do")
	if calls.Load() != 2 {
		t.Fatalf("creation turn did not execute tools: calls=%d", calls.Load())
	}
	key, text := "ask", "Garden"
	if _, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-h.ctx.Done():
		t.Fatal("question barrier missing")
	}
	key, text = "option", "2"
	responseID, err := c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text})
	if err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("admission: %+v %v", frame, err)
		}
		if frame.ReplyTo != nil && *frame.ReplyTo == responseID {
			break
		}
	}
	unblock()
	waitIdle := func() {
		t.Helper()
		for {
			frame, err := c.Receive(h.ctx)
			if err != nil || frame.Error != nil {
				t.Fatalf("turn: %+v %v", frame, err)
			}
			if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
				return
			}
		}
	}
	waitIdle()
	if calls.Load() != 7 {
		t.Fatalf("live calls=%d", calls.Load())
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	defer c.Close()
	// Retry the already consumed steering id after a real Core restart.
	responseID, err = c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text})
	if err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("retry: %+v %v", frame, err)
		}
		if frame.ReplyTo != nil && *frame.ReplyTo == responseID {
			break
		}
	}
	key, text = "card", "Check this"
	if _, err = c.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &text, Card: &recordID}); err != nil {
		t.Fatal(err)
	}
	waitIdle()
	if calls.Load() != 9 {
		t.Fatalf("restored calls=%d", calls.Load())
	}
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var persons, bindings, missing, completed int
	for _, q := range []struct {
		sql string
		out *int
	}{
		{`SELECT count(*) FROM turn_runs WHERE state='completed'`, &completed},
		{`SELECT count(*) FROM conv_event WHERE speaker_kind='person'`, &persons},
		{`SELECT count(*) FROM conv_event WHERE scaffold_kind='input_binding'`, &bindings},
		{`SELECT count(*) FROM conv_event p WHERE p.speaker_kind='person' AND (SELECT count(*) FROM conv_event b WHERE b.parent_id=p.id AND b.scaffold_kind='input_binding')<>1`, &missing},
	} {
		if err = db.QueryRowContext(h.ctx, q.sql).Scan(q.out); err != nil {
			t.Fatal(err)
		}
	}
	if persons != 4 || bindings != persons || missing != 0 || completed != 3 {
		t.Fatalf("person=%d bindings=%d missing=%d completed=%d", persons, bindings, missing, completed)
	}
}
