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
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestDoneEvidenceInstalledCore(t *testing.T) {
	t.Parallel()
	const reply = "The task is complete."
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "complete-todo", "write_record", map[string]any{
				"kind": "todo", "title": "Check the document", "note": "",
				"todo": map[string]any{"state": "completed"},
			})
		case 2:
			var item struct{ Output string }
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			var got struct {
				Code   string
				Output struct{ Markdown string }
			}
			if err := json.Unmarshal([]byte(item.Output), &got); err != nil || got.Code != "ok" || !strings.Contains(got.Output.Markdown, `state: "claimed"`) {
				t.Errorf("completion without evidence must be claimed: %s (%v)", item.Output, err)
			}
			emitResponse(w, reply, true)
		default:
			t.Error("unexpected model call")
			emitResponse(w, reply, true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "claim-todo", "Record the task completion.")
	c.Close()
	h.stopCore(core)
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var body string
	if err = db.QueryRowContext(h.ctx, "SELECT body FROM conv_event WHERE conversation_id=? AND speaker_kind='agent' AND scaffold_kind IS NULL ORDER BY recorded_at DESC LIMIT 1", conversation).Scan(&body); err != nil || body != reply {
		t.Fatalf("Core rewrote the reply: %q (%v)", body, err)
	}
	var breaches int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM done_completions WHERE state='claimed'").Scan(&breaches); err != nil || breaches != 1 {
		t.Fatalf("claimed completion breach count: %d (%v)", breaches, err)
	}
}

func TestDoneEvidenceReferencesInstalledCore(t *testing.T) {
	t.Parallel()
	const reply = "The checked document is complete."
	var calls atomic.Int32
	var source coreexec.RecordReceipt
	var missing, verified result.Card
	proof := func(kind, call string) *result.CompletionRequest {
		return &result.CompletionRequest{WorkKind: "document", Evidence: []result.EvidenceReference{{Kind: kind, CallID: call}}}
	}
	proposal := func(completion *result.CompletionRequest) result.PresentRequest {
		return result.PresentRequest{Type: result.DraftType, Title: "Document result", Summary: "The document is complete.", Body: json.RawMessage(`{"blocks":[{"kind":"paragraph","text":"The source contains 42."}],"from_summary":null}`), Completion: completion}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		read := func(want string, dst any) {
			t.Helper()
			var item struct{ Output string }
			if len(request.Input) == 0 {
				t.Error("missing tool result")
				return
			}
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			var out struct {
				Code   string
				Output json.RawMessage
			}
			if err := json.Unmarshal([]byte(item.Output), &out); err != nil || out.Code != want {
				t.Errorf("tool result: %s (%v)", item.Output, err)
				return
			}
			if dst != nil {
				if err := json.Unmarshal(out.Output, dst); err != nil {
					t.Error(err)
				}
			}
		}
		checkCard := func(dst *result.Card, state string) {
			read("ok", dst)
			if dst.Completion == nil || dst.Completion.State != state || dst.Title != "Document result" || dst.Summary != "The document is complete." {
				t.Errorf("card: %+v", dst)
			}
		}
		switch n := calls.Add(1); n {
		case 1:
			emitPersonaCall(w, "source", "write_record", coreexec.RecordWrite{Kind: "document", Title: "Primary source", Note: "The source contains 42."})
		case 2:
			read("ok", &source)
			emitPersonaCall(w, "reopen", "read_record", coreexec.RecordRead{ID: source.ID})
		case 3:
			read("ok", nil)
			emitPersonaCall(w, "complete", "write_record", coreexec.RecordWrite{Kind: "todo", Title: "Check the document", Todo: &records.Todo{State: "completed"}, Completion: proof("source_reopened", "reopen")})
		case 4:
			var todo coreexec.RecordReceipt
			read("ok", &todo)
			if !strings.Contains(todo.Markdown, `state: "completed"`) || !strings.Contains(todo.Markdown, `"state":"done"`) {
				t.Errorf("observed completion: %s", todo.Markdown)
			}
			emitPersonaCall(w, "missing", "present_result", proposal(&result.CompletionRequest{WorkKind: "document"}))
		case 5:
			checkCard(&missing, "claimed")
			emitPersonaCall(w, "verified", "present_result", proposal(proof("source_reopened", "reopen")))
		case 6:
			checkCard(&verified, "done")
			emitPersonaCall(w, "wrong-kind", "present_result", proposal(proof("receipt", "reopen")))
		case 7:
			var card result.Card
			checkCard(&card, "claimed")
			emitResponse(w, reply, true)
		case 8:
			emitPersonaCall(w, "read-verified", "read_result", result.ReadRequest{ID: verified.ID})
		case 9:
			var value result.Result
			read("ok", &value)
			if value.Completion == nil || value.Completion.State != "done" || len(value.Completion.Evidence) != 1 || value.Completion.Evidence[0].TurnID == "" {
				t.Errorf("missing retained chain: %+v", value)
			}
			emitPersonaCall(w, "failed-read", "read_record", coreexec.RecordRead{ID: "absent"})
		case 10:
			read("failed", nil)
			emitPersonaCall(w, "failed-proof", "present_result", proposal(proof("source_reopened", "failed-read")))
		case 11:
			var card result.Card
			checkCard(&card, "claimed")
			emitResponse(w, reply, true)
		case 12:
			emitPersonaCall(w, "query-restarted", "query_records", coreexec.RecordQuery{Kind: "todo", View: "list", Limit: 10})
		case 13:
			var rows struct{ Rows []records.Row }
			read("ok", &rows)
			if len(rows.Rows) != 1 || rows.Rows[0].Todo.Completion == nil || rows.Rows[0].Todo.Completion.State != "done" {
				t.Errorf("restarted todo: %+v", rows)
			}
			emitPersonaCall(w, "read-restarted", "read_result", result.ReadRequest{ID: missing.ID})
		case 14:
			var value result.Result
			read("ok", &value)
			if value.Completion == nil || value.Completion.State != "claimed" || value.Completion.Label != "not verified" {
				t.Errorf("restarted claim: %+v", value)
			}
			emitResponse(w, reply, true)
		default:
			t.Errorf("unexpected call %d", n)
			emitResponse(w, "Unexpected.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	frames := admittedTurn(t, h, c, conversation, "references", "Read the source and record the results.")
	frames = append(frames, admittedTurn(t, h, c, conversation, "references-more", "Check the remaining evidence.")...)
	claimedCards, doneCards := 0, 0
	for _, frame := range frames {
		if card, ok := frame.Payload.(protocol.Result); ok && card.Completion != nil {
			if card.Completion.State == protocol.CompletionStateClaimed {
				claimedCards++
				if card.Completion.Label != "not verified" {
					t.Errorf("card label: %+v", card.Completion)
				}
			} else {
				doneCards++
			}
		}
	}
	if claimedCards != 3 || doneCards != 1 {
		t.Errorf("wire cards: claimed=%d done=%d", claimedCards, doneCards)
	}
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "references-restart", "Read the recorded results again.")
	c.Close()
	h.stopCore(core)
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var breaches int
	if err = db.QueryRowContext(h.ctx, "SELECT sum(breaches) FROM agent_done_breaches").Scan(&breaches); err != nil || breaches != 3 {
		t.Fatalf("breaches after restart: %d %v", breaches, err)
	}
	var replies int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM conv_event WHERE speaker_kind='agent' AND scaffold_kind IS NULL AND body=?", reply).Scan(&replies); err != nil || replies != 3 {
		t.Fatalf("reply rewritten: %d %v", replies, err)
	}
	if calls.Load() != 14 {
		t.Fatalf("provider calls: %d", calls.Load())
	}
}
