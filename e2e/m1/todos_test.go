// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
)

func TestM2TodosInstalledCore(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../m2/todos.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Day, Zone, Overdue, Future, Ambiguous, Title, Dependent, Undated string }
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var first, second, updated, undated coreexec.RecordReceipt
	var live []records.Row
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct{ Name string }
			Input []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		names := map[string]bool{}
		for _, tool := range req.Tools {
			names[tool.Name] = true
		}
		for _, name := range []string{"write_record", "query_records", "read_record"} {
			if !names[name] {
				t.Error("missing installed tool", name)
			}
		}
		result := func(want string, out any) {
			t.Helper()
			var item struct{ Type, Output string }
			var got struct {
				Code   string
				Output json.RawMessage
			}
			if len(req.Input) == 0 {
				t.Error("empty input")
				return
			}
			if err := json.Unmarshal(req.Input[len(req.Input)-1], &item); err != nil {
				t.Error(err)
			}
			if err := json.Unmarshal([]byte(item.Output), &got); err != nil {
				t.Error(err)
			}
			if item.Type != "function_call_output" || got.Code != want {
				t.Errorf("result=%s want=%s", item.Output, want)
			}
			if err := json.Unmarshal(got.Output, out); err != nil {
				t.Error(err)
			}
		}
		query := func(id, view string) {
			emitPersonaCall(w, id, "query_records", coreexec.RecordQuery{View: view, Day: scenario.Day, Zone: scenario.Zone, Limit: 100})
		}
		denied := func(code string) {
			var got struct{ Code string }
			result("failed", &got)
			if got.Code != code {
				t.Errorf("refusal=%+v want=%s", got, code)
			}
		}
		checkToday := func() []records.Row {
			var got struct{ Rows []records.Row }
			result("ok", &got)
			byID := map[string]records.Row{}
			for _, row := range got.Rows {
				byID[row.ID] = row
			}
			if len(got.Rows) != 2 || !byID[first.ID].Overdue || byID[first.ID].Revision != first.Revision || !byID[second.ID].Blocked || byID[second.ID].Revision != second.Revision {
				t.Errorf("today rows=%+v", got.Rows)
			}
			return got.Rows
		}
		switch n := calls.Add(1); n {
		case 1:
			emitPersonaCall(w, "create-first", "write_record", coreexec.RecordWrite{Kind: "todo", Title: scenario.Title, Todo: &records.Todo{When: scenario.Overdue}})
		case 2:
			result("ok", &first)
			emitPersonaCall(w, "create-dependent", "write_record", coreexec.RecordWrite{Kind: "todo", Title: scenario.Dependent, Todo: &records.Todo{Deadline: scenario.Day, DeadlineSource: "mail:workshop", BlockedBy: []string{first.ID}, Important: true, Tags: []string{"workshop"}, Checklist: []records.ChecklistItem{{Text: "Attach the report"}}}})
		case 3:
			result("ok", &second)
			emitPersonaCall(w, "ambiguous-date", "write_record", coreexec.RecordWrite{ID: first.ID, Revision: first.Revision, Kind: "todo", Title: scenario.Title, Todo: &records.Todo{When: scenario.Ambiguous}})
		case 4:
			denied("ambiguous_date")
			emitPersonaCall(w, "dependency-cycle", "write_record", coreexec.RecordWrite{ID: first.ID, Revision: first.Revision, Kind: "todo", Title: scenario.Title, Todo: &records.Todo{When: scenario.Overdue, BlockedBy: []string{second.ID}}})
		case 5:
			denied("dependency_cycle")
			query("today-live", "today")
		case 6:
			live = checkToday()
			emitResponse(w, "Saved the tasks. The date needs clarification; the dependency cycle was refused.", true)
		case 7:
			query("today-rebuilt", "today")
		case 8:
			if got := checkToday(); !reflect.DeepEqual(got, live) {
				t.Error("restart changed today rows")
			}
			emitPersonaCall(w, "reschedule", "write_record", coreexec.RecordWrite{ID: first.ID, Revision: first.Revision, Kind: "todo", Title: scenario.Title, Todo: &records.Todo{When: scenario.Future}})
		case 9:
			result("ok", &updated)
			if updated.ID != first.ID || updated.Revision == first.Revision {
				t.Error("missing saved revision", updated)
			}
			query("upcoming", "upcoming")
		case 10:
			var got struct{ Rows []records.Row }
			result("ok", &got)
			if len(got.Rows) != 1 || got.Rows[0].ID != first.ID || got.Rows[0].Overdue {
				t.Error("rescheduling did not move view", got)
			}
			emitPersonaCall(w, "capture", "write_record", coreexec.RecordWrite{Kind: "todo", Title: scenario.Undated})
		case 11:
			result("ok", &undated)
			query("anytime", "anytime")
		case 12:
			var got struct{ Rows []records.Row }
			result("ok", &got)
			if len(got.Rows) != 1 || got.Rows[0].ID != undated.ID || got.Rows[0].State != "open" {
				t.Error("sentence-only capture hidden", got)
			}
			emitPersonaCall(w, "read-dependent", "read_record", coreexec.RecordRead{ID: second.ID})
		case 13:
			var got coreexec.RecordReceipt
			result("ok", &got)
			if got != second {
				t.Error("rescheduling changed deadline or dependent", got)
			}
			emitResponse(w, "Moved the planned start. The deadline is unchanged; the undated task is in anytime.", true)
		default:
			t.Errorf("unexpected provider call %d", n)
			emitResponse(w, "Unexpected request.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "todos-create", "Save the workshop tasks and show today's list.")
	c.Close()
	h.stopCore(core)
	dbPath := filepath.Join(h.root, "canon/canon.sqlite")
	db, err := canon.OpenSQLite(h.ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(h.ctx, "DELETE FROM records_todo"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "todos-restart", "Show today's list again, reschedule the start and add the spare-parts task.")
	c.Close()
	h.stopCore(core)
	if calls.Load() != 13 {
		t.Fatal("provider calls", calls.Load())
	}
	reader, err := canon.OpenSQLiteReader(h.ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count int
	if err = reader.QueryRowContext(h.ctx, "SELECT count(*) FROM records_todo").Scan(&count); err != nil || count != 3 {
		t.Fatal("rejected writes created records", count, err)
	}
	var when, deadline string
	if err = reader.QueryRowContext(h.ctx, "SELECT when_value FROM records_todo WHERE id=?", first.ID).Scan(&when); err != nil || when != scenario.Future {
		t.Fatal(when, err)
	}
	if err = reader.QueryRowContext(h.ctx, "SELECT deadline FROM records_todo WHERE id=?", second.ID).Scan(&deadline); err != nil || deadline != scenario.Day {
		t.Fatal(deadline, err)
	}
}
