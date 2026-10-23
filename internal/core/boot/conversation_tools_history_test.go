// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestConversationToolHistoryKeepsCompletePairs(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	queuedContextEvent(t, r, "first", "Earlier input", guard.EntryOwner, "")
	var batch []ledger.Admission
	for i := range 101 {
		call := fmt.Sprintf("call-%d", i)
		for _, result := range []bool{false, true} {
			id, entry := call, guard.EntryAgent
			body := fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":"skill_index","arguments":"{}"}`, call)
			if result {
				id, entry = "output-"+call, guard.EntryTool
				body = fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":"catalog"}`, call)
			}
			batch = append(batch, ledger.Admission{Entry: entry, Input: ledger.Input{
				ID: id, ConversationID: r.conversation, AgentID: agent, ParentID: "first",
				Surface: ledger.Surface{Kind: "tui"}, Locator: "tool:skill_index:" + call,
				ScaffoldKind: "provider_item", Body: body,
				OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed",
			}})
		}
	}
	if _, err := r.journal().AdmitBatch(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	current := queuedContextEvent(t, r, "current", "Use the prior results", guard.EntryOwner, "")
	items, _, _, events, err := r.modelContext(t.Context(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 204 || len(events) != 204 {
		t.Fatalf("history lost tool pairs or older input: items=%d events=%d, want 204", len(items), len(events))
	}
	for i, admission := range batch {
		if string(items[i+1]) != admission.Input.Body {
			t.Fatalf("item %d changed: %s", i, items[i+1])
		}
	}
	display, err := r.historyItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(display) != 2 || display[0].eventID != "first" || display[1].eventID != "current" {
		t.Fatalf("raw tool transcript leaked into conversation display: %v", display)
	}
}

func TestConversationTranscriptOrdersCallsAndReceipts(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	input := queuedContextEvent(t, r, "input", "Use tools", guard.EntryOwner, "")
	var inboxID int64
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		receipt, err := inbox.Insert(t.Context(), tx, inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "test", NativeID: "input"}})
		inboxID = receipt.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	store := turn.New(r.db)
	if err := store.Claim(t.Context(), "transcript", r.conversation, inboxID); err != nil {
		t.Fatal(err)
	}
	journal := store.Journal("transcript")
	items := []provider.Item{
		[]byte(`{"type":"reasoning","encrypted_content":"opaque"}`),
		[]byte(`{"type":"function_call","call_id":"first","name":"read","arguments":"{}"}`),
		[]byte(`{"type":"custom_tool_call","call_id":"second","name":"patch","input":"patch text"}`),
	}
	// Item finalization and parallel receipt completion need not be in wire order.
	for _, i := range []int{2, 0, 1} {
		if err := journal.Item(t.Context(), 0, int64(i), items[i]); err != nil {
			t.Fatal(err)
		}
	}
	calls := []provider.ToolCall{{ID: "first", Name: "read", Arguments: "{}"}, {ID: "second", Name: "patch", Arguments: "patch text", Custom: true}}
	if err := journal.Calls(t.Context(), 0, calls); err != nil {
		t.Fatal(err)
	}
	results := []loop.Result{{ID: "first", Code: loop.OK, Output: json.RawMessage(`{"text":"file content"}`)}, {ID: "second", Code: loop.Denied}}
	for _, i := range []int{1, 0} {
		if err := journal.Result(t.Context(), 0, i, results[i]); err != nil {
			t.Fatal(err)
		}
	}
	reply := ledger.Admission{Entry: guard.EntryAgent, Input: ledger.Input{
		ID: "transcript:2:0", ParentID: input.ID, ConversationID: r.conversation, AgentID: agent,
		Surface: input.Surface, OccurredAt: time.Now(), RecordedAt: time.Now(), TimeQuality: "observed", Body: "Finished.",
	}}
	if err := journal.Item(t.Context(), 1, 0, []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Finished."}]}`)); err != nil {
		t.Fatal(err)
	}
	batch, err := conversationTranscript(t.Context(), r.db, "transcript", input, []ledger.Admission{reply})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 6 {
		t.Fatalf("transcript entries=%d, want 6", len(batch))
	}
	events, err := r.journal().AdmitBatch(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	history, err := assemble.History(events, r.conversation, agent)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if !bytes.Equal(history[i], items[i]) {
			t.Fatalf("provider item %d changed: %s", i, history[i])
		}
	}
	for i, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		var item struct {
			Type, Output string
			CallID       string `json:"call_id"`
		}
		if err := json.Unmarshal(history[i+3], &item); err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(results[i])
		if item.Type != kind || item.CallID != calls[i].ID || item.Output != string(want) {
			t.Fatalf("receipt changed order, kind or bytes: %+v", item)
		}
		e := events[i+3]
		if e.SpeakerKind != guard.Tool || e.Origin.Locator == "" || len(e.Relations) != 1 || e.Relations[0].TargetID != events[i+1].ID || e.Relations[0].Kind != "pairs_with" {
			t.Fatalf("receipt lost provenance/pair: %+v", e)
		}
	}
	if events[5].ID != reply.Input.ID || events[5].Body.Text() != reply.Input.Body {
		t.Fatal("reply moved or changed")
	}
}

func TestConversationRuntimeTranscriptPrecedesReply(t *testing.T) {
	r, _, agent, _ := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	input := queuedContextEvent(t, r, "input", "Read a file", guard.EntryOwner, "")
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		receipt, err := inbox.Insert(t.Context(), tx, inbox.Input{Key: inbox.Key{Conversation: r.conversation, Source: "test", NativeID: "input"}})
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO turn_runs(id,conversation,inbox_id,state) VALUES('runtime',?,?,'active')`, r.conversation, receipt.ID); err != nil {
			return err
		}
		// Runtime bridges journal a call/result before the final reported message,
		// but do not receive a matching function-call ItemDone from the runtime.
		call, _ := json.Marshal(provider.ToolCall{ID: "read", Name: "read_file", Arguments: `{"path":"example.txt"}`})
		result, _ := json.Marshal(loop.Result{ID: "read", Code: loop.OK, Output: json.RawMessage(`{"text":"public","password":"synthetic-private-value"}`)})
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call,result) VALUES('runtime',0,0,'read',?,?)`, call, result); err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), `INSERT INTO turn_items(turn_id,round,ordinal,item) VALUES('runtime',0,0,?)`, []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Read complete."}]}`))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reply := ledger.Admission{Entry: guard.EntryAgent, Input: ledger.Input{
		ID: "runtime:1:0", ParentID: input.ID, ConversationID: r.conversation, AgentID: agent, Surface: input.Surface,
		OccurredAt: time.Now(), RecordedAt: time.Now(), TimeQuality: "observed", Body: "Read complete.",
	}}
	batch, err := conversationTranscript(t.Context(), r.db, "runtime", input, []ledger.Admission{reply})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 3 || batch[0].Input.ScaffoldKind != "provider_item" || batch[1].Entry != guard.EntryTool || batch[2].Input.ID != reply.Input.ID {
		t.Fatalf("runtime call/result must precede its reply: %+v", batch)
	}
	events, err := r.journal().AdmitBatch(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	items, err := assemble.History(events, r.conversation, agent)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range items {
		if !json.Valid(item) || bytes.Contains(item, []byte("synthetic-private-value")) {
			t.Fatalf("invalid or unmasked history item %d: %s", i, item)
		}
	}
	if !bytes.Contains(items[1], []byte("public")) || len(events[1].Spans) == 0 {
		t.Fatal("masking lost public content or secret flag")
	}
}
