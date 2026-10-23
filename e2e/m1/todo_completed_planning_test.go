// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
)

func TestM2CompletedTodoPlanningInstalledCore(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../m2/todo-completed-planning.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Title, Note string
		records.Todo
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var saved, updated coreexec.RecordReceipt // Owned by the serial provider loop.
	receipts := make(chan coreexec.RecordReceipt, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		result := func(code string, out any) bool {
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
			if err := json.Unmarshal([]byte(item.Output), &got); err != nil {
				t.Error(err)
				return false
			}
			if item.Type != "function_call_output" || got.Code != code {
				t.Errorf("tool result: %s; want %s", item.Output, code)
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
			emitPersonaCall(w, "capture", "write_record", coreexec.RecordWrite{Kind: "todo", Title: scenario.Title, Note: scenario.Note, Todo: &records.Todo{Assignee: scenario.Assignee}})
		case 2:
			if result("ok", &saved) {
				receipts <- saved
			}
			emitResponse(w, "Saved.", true)
		case 3:
			emitPersonaCall(w, "read-completed", "read_record", coreexec.RecordRead{ID: saved.ID})
		case 4:
			if !result("ok", &saved) {
				emitResponse(w, "Read failed.", true)
				return
			}
			if !strings.Contains(saved.Markdown, `state: "completed"`) {
				t.Error("completion fixture was not loaded")
			}
			next := scenario.Todo
			next.State = "completed"
			emitPersonaCall(w, "edit-planning", "write_record", coreexec.RecordWrite{ID: saved.ID, Revision: saved.Revision, Kind: "todo", Title: scenario.Title, Note: scenario.Note, Todo: &next})
		case 5:
			if !result("ok", &updated) {
				emitResponse(w, "Planning edit failed.", true)
				return
			}
			if updated.ID != saved.ID || updated.Revision == saved.Revision {
				t.Error("planning edit did not save a new version")
			}
			emitPersonaCall(w, "query-completed", "query_records", coreexec.RecordQuery{Kind: "todo", View: "board", Limit: 10})
		case 6:
			var got struct{ Rows []records.Row }
			if result("ok", &got) {
				want := scenario.Todo
				want.State = "completed"
				if len(got.Rows) != 1 || got.Rows[0].ID != saved.ID || got.Rows[0].Revision != updated.Revision || !reflect.DeepEqual(got.Rows[0].Todo, &want) {
					t.Errorf("planning fields or completion lost: %+v", got.Rows)
				}
			}
			emitResponse(w, "Updated the completed task.", true)
		case 7:
			emitPersonaCall(w, "read-after-restart", "read_record", coreexec.RecordRead{ID: saved.ID})
		case 8:
			var got coreexec.RecordReceipt
			if result("ok", &got) && got != updated {
				t.Error("restart changed saved planning fields or completion", got)
			}
			emitResponse(w, "The completed task is saved.", true)
		default:
			t.Errorf("unexpected provider call %d", n)
			emitResponse(w, "Unexpected request.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "capture", "Save the workshop report task.")
	c.Close()
	h.stopCore(core)
	var created coreexec.RecordReceipt
	select {
	case created = <-receipts:
	default:
		t.Fatal("capture did not produce a receipt")
	}

	// Seed accepted completion through the trusted repository while Core is
	// stopped. The model must never manufacture this transition itself.
	db, closeStore := installedAcceptanceStore(t, h)
	repo, err := records.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	record, revision, err := repo.Read(h.ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	actor := records.Actor{}
	front, _, _ := strings.Cut(string(record.Data), "\n---\n")
	for _, line := range strings.Split(front, "\n") {
		key, value, _ := strings.Cut(line, ": ")
		switch key {
		case "person_id":
			err = json.Unmarshal([]byte(value), &actor.Person)
		case "agent_id":
			err = json.Unmarshal([]byte(value), &actor.Agent)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	completed := strings.Replace(string(record.Data), `state: "open"`, `state: "completed"`, 1)
	if completed == string(record.Data) {
		t.Fatal("missing open fixture state")
	}
	if _, err = repo.Commit(h.ctx, records.Change{ID: record.ID, Path: record.Path, Base: revision, Data: []byte(completed)}, actor); err != nil {
		t.Fatal(err)
	}
	repo.Close()
	closeStore()

	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "planning", "Update the tags, priority and checklist of the completed task.")
	c.Close()
	h.stopCore(core)
	if calls.Load() != 6 {
		t.Fatalf("planning edit did not reach query: provider calls=%d", calls.Load())
	}
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "restart", "Read the saved task again.")
	c.Close()
	h.stopCore(core)
	if calls.Load() != 8 {
		t.Fatal("restart did not read the saved task", calls.Load())
	}
}
