// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
)

func TestM2TodoTimestampsInstalledCore(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../m2/todo-timestamps.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Day, Zone      string
		Invalid, Valid []records.Todo
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	type operation struct {
		name, tool, code string
		args             func() any
		check            func(json.RawMessage)
	}
	var operations []operation
	refused := func(raw json.RawMessage) {
		var got struct{ Code string }
		if err := json.Unmarshal(raw, &got); err != nil || got.Code != "ambiguous_date" {
			t.Errorf("date refusal: %s (%v)", raw, err)
		}
	}
	for i, todo := range scenario.Invalid {
		operations = append(operations, operation{
			name: fmt.Sprintf("invalid-%d", i), tool: "write_record", code: "failed",
			args:  func() any { return coreexec.RecordWrite{Kind: "todo", Title: "Invalid date", Todo: &todo} },
			check: refused,
		})
	}
	saved := map[string]coreexec.RecordReceipt{} // Owned by the serial provider loop.
	want := map[string]records.Todo{}
	for i, todo := range scenario.Valid {
		title := fmt.Sprintf("Scheduled task %d", i)
		operations = append(operations, operation{
			name: fmt.Sprintf("valid-%d", i), tool: "write_record", code: "ok",
			args: func() any { return coreexec.RecordWrite{Kind: "todo", Title: title, Todo: &todo} },
			check: func(raw json.RawMessage) {
				var receipt coreexec.RecordReceipt
				if err := json.Unmarshal(raw, &receipt); err != nil || receipt.ID == "" || receipt.Revision == "" {
					t.Errorf("missing saved receipt: %s (%v)", raw, err)
					return
				}
				saved[title] = receipt
				todo.State = "open"
				if todo.Repeat != nil {
					// Capture preserves the supplied date and records the series
					// anchor for later occurrence-only rescheduling.
					repeat := *todo.Repeat
					repeat.Start = repeat.Next
					todo.Repeat = &repeat
				}
				want[receipt.ID] = todo
			},
		})
	}
	operations = append(operations, operation{
		name: "invalid-edit", tool: "write_record", code: "failed",
		args: func() any {
			r := saved["Scheduled task 0"]
			return coreexec.RecordWrite{ID: r.ID, Revision: r.Revision, Kind: "todo", Title: "Scheduled task 0", Todo: &scenario.Invalid[0]}
		},
		check: refused,
	})
	for _, view := range []string{"board", "list", "today", "week", "upcoming"} {
		operations = append(operations, operation{
			name: view, tool: "query_records", code: "ok",
			args: func() any {
				day := scenario.Day
				if view == "upcoming" {
					day = "2026-10-21"
				}
				return coreexec.RecordQuery{View: view, Day: day, Zone: scenario.Zone, Limit: 100}
			},
			check: func(raw json.RawMessage) {
				var got struct{ Rows []records.Row }
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Error(err)
					return
				}
				actual := map[string]records.Todo{}
				for _, row := range got.Rows {
					if row.Todo == nil || row.Revision != saved[row.Title].Revision || row.Overdue {
						t.Errorf("%s changed the saved task: %+v", view, row)
						continue
					}
					actual[row.ID] = *row.Todo
				}
				if len(got.Rows) != len(scenario.Valid) || !reflect.DeepEqual(actual, want) {
					t.Errorf("%s lost valid tasks or published refused tasks: %s", view, raw)
				}
			},
		})
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if n/2 >= len(operations) {
			t.Errorf("unexpected provider call %d", n)
			emitResponse(w, "Unexpected request.", true)
			return
		}
		op := operations[n/2]
		if n%2 == 0 {
			emitPersonaCall(w, op.name, op.tool, op.args())
			return
		}
		var req struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Input) == 0 {
			t.Errorf("missing tool response: %v", err)
		} else {
			var item struct{ Type, Output string }
			var result struct {
				Code   string
				Output json.RawMessage
			}
			if err := json.Unmarshal(req.Input[len(req.Input)-1], &item); err != nil {
				t.Error(err)
			} else if err := json.Unmarshal([]byte(item.Output), &result); err != nil || item.Type != "function_call_output" || result.Code != op.code {
				t.Errorf("%s: result=%s; want %s (%v)", op.name, item.Output, op.code, err)
			} else {
				op.check(result.Output)
			}
		}
		emitResponse(w, "Checked.", true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	for i, op := range operations {
		admittedTurn(t, h, c, conversation, op.name, "Check the task date.")
		if got := int(calls.Load()); got != (i+1)*2 {
			t.Fatalf("%s did not return its tool result: calls=%d", op.name, got)
		}
	}
	c.Close()
	h.stopCore(core)
}
