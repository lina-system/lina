// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

// The person answers a real target question. A later model call cannot bypass
// that choice by interpreting a literal task id as a session or task prefix.
func TestM2FollowWorkPrefixedTaskInstalledCore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			a := binding.Target{Class: "existing", ID: "chosen", Kind: "task"}
			b := binding.Target{Class: "existing", ID: "session:foo", Kind: "task"}
			emitPersonaCall(w, "choose", "bind_input", map[string]any{"bindings": []binding.Choice{{Quote: "Send it a format change.", Target: a, Tied: []binding.Target{a, b}, Action: "send"}}})
		case 2:
			assertAskResult(t, request.Input, "choose", "ok", "")
			emitResponse(w, "Which task should receive the change?", true)
		case 3:
			emitPersonaCall(w, "session-prefix", "follow_work", map[string]string{"id": "session:foo", "text": "Use a table."})
		case 4:
			assertAskResult(t, request.Input, "session-prefix", "denied", "input_target_pending")
			emitPersonaCall(w, "task-prefix", "follow_work", map[string]string{"id": "task:foo", "text": "Use a table."})
		case 5:
			assertAskResult(t, request.Input, "task-prefix", "denied", "input_target_pending")
			emitResponse(w, "The other tasks received no change.", true)
		default:
			t.Error("unexpected model round")
			emitResponse(w, "Finished.", true)
		}
	})))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	c, conversation := conversationClient(t, h)
	person, agent := c.Welcome.PersonID, strings.TrimPrefix(conversation, "main:")
	c.Close()
	h.stopCore(core)
	// Seed durable task ids while Core is stopped; the protocol and tools run
	// normally after restart, with no worker session named foo.
	func() {
		ctl, err := ctlfiles.Open(h.root)
		if err != nil {
			t.Fatal(err)
		}
		defer ctl.Close()
		if err = ctl.Acquire(); err != nil {
			t.Fatal(err)
		}
		epoch, err := ctl.Epoch()
		if err != nil {
			t.Fatal(err)
		}
		db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon", "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close(context.Background())
		for _, id := range []string{"chosen", "session:foo", "task:foo"} {
			if err := work.New(db).Create(h.ctx, work.Task{ID: id, PersonID: person, AgentID: agent, ConversationID: conversation, Title: "Task " + id, Done: "Done"}); err != nil {
				t.Fatal(err)
			}
		}
	}()
	core = start(true)
	c, conversation = conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	defer c.Close()
	admittedTurn(t, h, c, conversation, "choose-target", "Send it a format change.")
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err = db.QueryRowContext(h.ctx, "SELECT record FROM conversation_questions").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var q waiting.ConversationQuestion
	if err = json.Unmarshal(raw, &q); err != nil || q.Binding == nil || len(q.Choices) != 2 || len(q.Binding.Tied) != 2 || q.Binding.Tied[0].ID != "chosen" {
		t.Fatalf("target question: %s err=%v", raw, err)
	}
	revision, value := "1", q.Choices[0]
	values := protocol.FormValues{"answer": {String: &value}}
	if _, err = c.AnswerQuestion(h.ctx, protocol.QuestionAnswer{CardID: &q.ID, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("answer turn: %+v %v", frame, err)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	if calls.Load() != 5 {
		t.Fatalf("model rounds=%d, want 5", calls.Load())
	}
	var messages int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM work_steering").Scan(&messages); err != nil || messages != 0 {
		t.Fatalf("follow-up stored: messages=%d err=%v", messages, err)
	}
}
