// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
)

func TestM2ExhaustedRecordCompletionInstalledCore(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../m2/record-exhausted-completion.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Title, Note, Rule, First string }
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var saved, exhausted, completed, edited coreexec.RecordReceipt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		result := func(out any) bool {
			t.Helper()
			var item struct{ Type, Output string }
			var got struct {
				Code   string
				Output json.RawMessage
			}
			if len(req.Input) == 0 {
				t.Error("missing tool result")
				return false
			}
			if err := json.Unmarshal(req.Input[len(req.Input)-1], &item); err != nil {
				t.Error(err)
				return false
			}
			if err := json.Unmarshal([]byte(item.Output), &got); err != nil || item.Type != "function_call_output" || got.Code != "ok" {
				t.Errorf("tool result: %s: %v", item.Output, err)
				return false
			}
			if err := json.Unmarshal(got.Output, out); err != nil {
				t.Error(err)
				return false
			}
			return true
		}
		switch n := calls.Add(1); n {
		case 1:
			emitPersonaCall(w, "capture", "write_record", coreexec.RecordWrite{Kind: "todo", Title: scenario.Title, Todo: &records.Todo{Repeat: &records.Repeat{Rule: scenario.Rule, Next: scenario.First}}})
		case 2:
			result(&saved)
			emitResponse(w, "Saved.", true)
		case 3:
			emitPersonaCall(w, "read-exhausted", "read_record", coreexec.RecordRead{ID: saved.ID})
		case 4:
			if !result(&exhausted) {
				emitResponse(w, "Read failed.", true)
				return
			}
			repeat := records.RepeatFromFront(exhausted.Markdown)
			if repeat == nil || !repeat.Exhausted || !strings.Contains(exhausted.Markdown, `state: "open"`) {
				t.Error("startup did not exhaust the open series", exhausted)
			}
			emitPersonaCall(w, "complete", "write_record", coreexec.RecordWrite{ID: saved.ID, Revision: exhausted.Revision, Kind: "todo", Title: scenario.Title, Todo: &records.Todo{State: "completed", Repeat: &records.Repeat{Rule: scenario.Rule, Next: scenario.First}}})
		case 5:
			if !result(&completed) {
				emitResponse(w, "Completion failed.", true)
				return
			}
			before := records.RepeatFromFront(exhausted.Markdown)
			after := records.RepeatFromFront(completed.Markdown)
			if completed.ID != saved.ID || completed.Revision == exhausted.Revision || !strings.Contains(completed.Markdown, `state: "completed"`) || before == nil || after == nil || *before != *after {
				t.Error("completion changed the exhausted schedule", completed)
			}
			emitPersonaCall(w, "edit-note", "write_record", coreexec.RecordWrite{ID: saved.ID, Revision: completed.Revision, Kind: "todo", Title: scenario.Title, Note: scenario.Note})
		case 6:
			result(&edited)
			emitResponse(w, "Completed the inspection and saved its note.", true)
		case 7:
			emitPersonaCall(w, "read-restarted", "read_record", coreexec.RecordRead{ID: saved.ID})
		case 8:
			var got coreexec.RecordReceipt
			if result(&got) && got != edited {
				t.Error("restart changed completion", got)
			}
			emitResponse(w, "The inspection remains completed.", true)
		default:
			t.Errorf("unexpected provider call %d", n)
			emitResponse(w, "Unexpected request.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "exhausted-capture", "Save the recurring inspection.")
	c.Close()
	h.stopCore(core)
	core = start(true)
	if calls.Load() != 2 {
		t.Fatal("startup used a model to exhaust recurrence", calls.Load())
	}
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "exhausted-complete", "Complete the inspection and add its note.")
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "exhausted-restart", "Read the completed inspection.")
	c.Close()
	h.stopCore(core)
	if calls.Load() != 8 {
		t.Fatal("provider calls", calls.Load())
	}
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var events, completions int
	err = db.QueryRowContext(h.ctx, "SELECT count(*),coalesce(sum(json_extract(record,'$.occurrence.reason')='completed' AND json_extract(record,'$.occurrence.exhausted')=1),0) FROM work_ledger WHERE task_id=? AND json_extract(record,'$.kind')='record_occurrence'", "record:"+saved.ID).Scan(&events, &completions)
	if err != nil || events != 2 || completions != 1 {
		t.Fatalf("exhausted completion ledger: events=%d completed=%d, want 2 and 1: %v", events, completions, err)
	}
}
