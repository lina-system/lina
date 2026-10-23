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
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
)

func TestM2RecordRecurrencePromotionInstalledCore(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../m2/record-recurrence-promotion.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Memo, Note, Recurring, Overdue, Rule, First string }
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var memo, recurring, overdue coreexec.RecordReceipt
	var promoted records.Applied
	var restored coreexec.RecordReceipt
	var completion records.Applied
	checkFuture := func(receipt coreexec.RecordReceipt) {
		t.Helper()
		var repeat records.Repeat
		for _, line := range strings.Split(receipt.Markdown, "\n") {
			if raw, ok := strings.CutPrefix(line, "repeat: "); ok {
				if err := json.Unmarshal([]byte(raw), &repeat); err != nil {
					t.Error(err)
				}
			}
		}
		next, err := time.Parse(time.DateOnly, repeat.Next)
		if err != nil || !next.After(time.Now()) || repeat.Start != scenario.First || !strings.Contains(receipt.Markdown, `state: "open"`) {
			t.Errorf("recurrence not advanced: %+v %v", repeat, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []json.RawMessage
			Tools []struct{ Name string }
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		result := func(out any) {
			t.Helper()
			var item struct{ Type, Output string }
			var got struct {
				Code   string
				Output json.RawMessage
			}
			if len(req.Input) == 0 {
				t.Error("missing result")
				return
			}
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &item)
			_ = json.Unmarshal([]byte(item.Output), &got)
			if item.Type != "function_call_output" || got.Code != "ok" {
				t.Errorf("tool result %s", item.Output)
				return
			}
			if err := json.Unmarshal(got.Output, out); err != nil {
				t.Error(err)
			}
		}
		create := func(id, title string) {
			emitPersonaCall(w, id, "write_record", coreexec.RecordWrite{Kind: "todo", Title: title, Todo: &records.Todo{Repeat: &records.Repeat{Rule: scenario.Rule, Next: scenario.First}}})
		}
		switch n := calls.Add(1); n {
		case 1:
			names := map[string]bool{}
			for _, tool := range req.Tools {
				names[tool.Name] = true
			}
			if !names["promote_memo"] || !names["undo_record"] {
				t.Error("missing record action tools")
			}
			emitPersonaCall(w, "capture-memo", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.Memo, Note: scenario.Note})
		case 2:
			result(&memo)
			emitPersonaCall(w, "promote", "promote_memo", coreexec.RecordAction{ID: memo.ID, Revision: memo.Revision})
		case 3:
			result(&promoted)
			if promoted.ID != memo.ID || promoted.Revision == memo.Revision || promoted.Undo == "" || !strings.Contains(promoted.Markdown, scenario.Note) {
				t.Error("promotion receipt", promoted)
			}
			emitPersonaCall(w, "undo-promotion", "undo_record", coreexec.RecordAction{ID: memo.ID, Revision: promoted.Revision, Handle: promoted.Undo})
		case 4:
			result(&restored)
			if restored.ID != memo.ID || restored.Markdown != memo.Markdown || restored.Revision == memo.Revision {
				t.Error("undo receipt", restored)
			}
			create("capture-repeat", scenario.Recurring)
		case 5:
			result(&recurring)
			emitPersonaCall(w, "complete-repeat", "write_record", coreexec.RecordWrite{ID: recurring.ID, Revision: recurring.Revision, Kind: "todo", Title: scenario.Recurring, Todo: &records.Todo{State: "completed", Repeat: &records.Repeat{Rule: scenario.Rule, Next: scenario.First}}})
		case 6:
			result(&completion)
			if completion.ID != recurring.ID || completion.Revision == recurring.Revision {
				t.Error("recurrence identity", completion)
			}
			checkFuture(coreexec.RecordReceipt{ID: completion.ID, Revision: completion.Revision, Markdown: completion.Markdown})
			create("capture-overdue", scenario.Overdue)
		case 7:
			result(&overdue)
			emitResponse(w, "Converted the memo and undid it. The recurring task advanced.", true)
		case 8:
			emitPersonaCall(w, "read-overdue", "read_record", coreexec.RecordRead{ID: overdue.ID})
		case 9:
			var advanced coreexec.RecordReceipt
			result(&advanced)
			if advanced.ID != overdue.ID || advanced.Revision == overdue.Revision {
				t.Error("restart did not advance overdue occurrence", advanced)
			}
			checkFuture(advanced)
			emitPersonaCall(w, "read-restored", "read_record", coreexec.RecordRead{ID: memo.ID})
		case 10:
			var got coreexec.RecordReceipt
			result(&got)
			if got != restored {
				t.Error("restart changed restored memo", got)
			}
			emitPersonaCall(w, "list-todos", "query_records", coreexec.RecordQuery{Kind: "todo", Limit: 100})
		case 11:
			var got struct{ Rows []records.Row }
			result(&got)
			if len(got.Rows) != 2 {
				t.Error("recurrence created instances", got)
			}
			emitResponse(w, "The overdue occurrence advanced during startup. Both recurring tasks keep one record.", true)
		default:
			t.Errorf("unexpected provider call %d", n)
			emitResponse(w, "Unexpected request.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "record-actions", "Capture a memo, promote it, undo it and complete a recurring task.")
	c.Close()
	h.stopCore(core)
	core = start(true)
	if calls.Load() != 7 {
		t.Fatal("startup used a model to advance recurrence", calls.Load())
	}
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "record-actions-restart", "Read the recurring tasks and restored memo.")
	c.Close()
	h.stopCore(core)
	if calls.Load() != 11 {
		t.Fatal("provider calls", calls.Load())
	}
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var notices int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM conv_event WHERE scaffold_kind='record_change'").Scan(&notices); err != nil || notices != 2 {
		t.Fatal("durable notices", notices, err)
	}
	var occurrences, completions int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*),coalesce(sum(json_extract(record,'$.occurrence.reason')='completed'),0) FROM work_ledger WHERE json_extract(record,'$.kind')='record_occurrence'").Scan(&occurrences, &completions); err != nil || occurrences != 2 || completions != 1 {
		t.Fatal("recurrence ledger", occurrences, completions, err)
	}
}
