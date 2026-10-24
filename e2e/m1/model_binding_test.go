// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Real tools start two independent runs. A risky target choice remains a
// durable card through unrelated work and a Core process restart.
func TestM2ModelBindingInstalledCore(t *testing.T) {
	t.Parallel()
	var foreground atomic.Int32
	started := make(chan struct{}, 8)
	var mu sync.Mutex
	var first, second, question string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Instructions string
			Input        []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(request.Instructions, "self-contained background brief") {
			select {
			case started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		mu.Lock()
		defer mu.Unlock()
		start := func(name, goal string, index int) {
			raw, _ := json.Marshal(runs.Brief{Goal: goal, Scope: "Read the skill inventory", NotTouch: "Writes", Done: "Inventory checked", Result: "summary", Allow: []string{"skill_index"}, Checks: []runs.Check{{Name: "inventory", Kind: runs.Claimed}}})
			var args map[string]any
			_ = json.Unmarshal(raw, &args)
			args["input_binding"] = map[string]any{"request": index}
			emitPersonaCall(w, name, "start_run", args)
		}
		switch foreground.Add(1) {
		case 1:
			emitPersonaCall(w, "split", "bind_input", map[string]any{"bindings": []binding.Choice{{Request: 0, Quote: "Analyze Alpha.", Target: binding.Target{Class: "new", Kind: "run"}}, {Request: 1, Quote: "Analyze Beta.", Target: binding.Target{Class: "new", Kind: "run"}}}})
		case 2:
			assertAskResult(t, request.Input, "split", "ok", "")
			start("alpha", "Alpha analysis", 0)
		case 3:
			var receipt runs.Receipt
			_ = json.Unmarshal(assertAskResult(t, request.Input, "alpha", "ok", ""), &receipt)
			first = receipt.ID
			start("beta", "Beta analysis", 1)
		case 4:
			var receipt runs.Receipt
			_ = json.Unmarshal(assertAskResult(t, request.Input, "beta", "ok", ""), &receipt)
			second = receipt.ID
			if first == "" || second == "" || first == second {
				t.Error("two run receipts missing")
			}
			emitResponse(w, "Both analyses have started.", true)
		case 5:
			a, b := binding.Target{Class: "existing", ID: first, Kind: "run"}, binding.Target{Class: "existing", ID: second, Kind: "run"}
			emitPersonaCall(w, "tie", "bind_input", map[string]any{"bindings": []binding.Choice{{Quote: "Change its format.", Target: a, Tied: []binding.Target{a, b}, Action: "steer"}}})
		case 6:
			var batch struct {
				Bindings []binding.Choice `json:"bindings"`
			}
			_ = json.Unmarshal(assertAskResult(t, request.Input, "tie", "ok", ""), &batch)
			if len(batch.Bindings) != 1 {
				t.Error("tie binding missing")
			} else {
				question = batch.Bindings[0].Question
			}
			emitPersonaCall(w, "premature", "follow_work", map[string]string{"id": first, "text": "Use a table."})
		case 7:
			assertAskResult(t, request.Input, "premature", "denied", "input_target_pending")
			emitResponse(w, "Which analysis should use a table?", true)
		case 8:
			emitPersonaCall(w, "independent", "write_record", map[string]any{"id": "", "revision": "", "kind": "memo", "title": "Prime numbers", "note": "Two is prime."})
		case 9:
			assertAskResult(t, request.Input, "independent", "ok", "")
			emitResponse(w, "Two is a prime number.", true)
		case 10:
			emitPersonaCall(w, "progress", "read_work_progress", map[string]string{"id": second})
		case 11:
			assertAskResult(t, request.Input, "progress", "ok", "")
			emitResponse(w, "The analysis is still running.", true)
		case 12:
			emitPersonaCall(w, "chosen", "follow_work", map[string]string{"id": second, "text": "Use a table."})
		case 13:
			assertAskResult(t, request.Input, "chosen", "ok", "")
			emitResponse(w, "The selected analysis received the format change.", true)
		default:
			t.Error("unexpected model round")
			emitResponse(w, "Finished.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() { h.stopCore(core) }()
	c, conversation := conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	admittedTurn(t, h, c, conversation, "two-analyses", "Analyze Alpha. Analyze Beta.")
	for range 2 {
		select {
		case <-started:
		case <-h.ctx.Done():
			t.Fatal("background analyses did not start")
		}
	}
	admittedTurn(t, h, c, conversation, "tie", "Change its format.")
	admittedTurn(t, h, c, conversation, "independent", "Tell me a prime number.")
	inspect := func(wantResolved bool) waiting.ConversationQuestion {
		t.Helper()
		db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mu.Lock()
		qid, a, b := question, first, second
		mu.Unlock()
		var raw, resolution []byte
		if err = db.QueryRowContext(h.ctx, `SELECT q.record,r.record FROM conversation_questions q LEFT JOIN conversation_question_resolutions r ON r.question=q.id WHERE q.id=?`, qid).Scan(&raw, &resolution); err != nil {
			t.Fatal(err)
		}
		var q waiting.ConversationQuestion
		if json.Unmarshal(raw, &q) != nil || q.Binding == nil || len(q.Choices) != 2 || (len(resolution) > 0) != wantResolved {
			t.Fatalf("question persistence: %s resolution=%s", raw, resolution)
		}
		for _, id := range []string{a, b} {
			if err = db.QueryRowContext(h.ctx, "SELECT record FROM background_runs WHERE id=?", id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var record runs.Record
			_ = json.Unmarshal(raw, &record)
			want := 0
			if wantResolved && id == b {
				want = 1
			}
			if len(record.FollowUps) != want {
				t.Fatalf("run %s has %d steers, want %d", id, len(record.FollowUps), want)
			}
		}
		var requests int
		if err = db.QueryRowContext(h.ctx, `SELECT json_array_length(body,'$.bindings') FROM conv_event WHERE id LIKE 'binding-choice:%:split'`).Scan(&requests); err != nil || requests != 2 {
			t.Fatalf("split=%d %v", requests, err)
		}
		return q
	}
	inspect(false)
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h, protocol.Capability{Name: "waiting", Version: "1.0"})
	topic := "waiting"
	if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := c.Receive(h.ctx)
	snapshot, ok := frame.Payload.(protocol.Snapshot)
	if err != nil || !ok {
		t.Fatalf("waiting: %+v %v", frame, err)
	}
	mu.Lock()
	qid := question
	mu.Unlock()
	found := false
	for _, item := range snapshot.Items {
		var q protocol.Question
		if item.Kind == protocol.KindQuestion && json.Unmarshal(item.Payload, &q) == nil && q.Card != nil && q.Card.ID == qid && q.Card.Outcome == nil {
			found = true
		}
	}
	if !found {
		t.Fatal("target question missing from Waiting for me after restart")
	}
	admittedTurn(t, h, c, conversation, "status", "How is Beta doing?")
	q := inspect(false)
	revision, value := "1", q.Choices[1]
	values := protocol.FormValues{"answer": {String: &value}}
	if _, err = c.AnswerQuestion(h.ctx, protocol.QuestionAnswer{CardID: &qid, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err = c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("answer turn: %+v %v", frame, err)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	inspect(true)
	if foreground.Load() != 13 {
		t.Fatalf("foreground rounds=%d", foreground.Load())
	}
}
