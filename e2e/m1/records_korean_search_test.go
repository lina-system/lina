// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2KoreanRecordSearchInstalledTUI(t *testing.T) {
	raw, err := os.ReadFile("../m2/records-korean-search.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Title, Note, Updated, Noun, Attached string }
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	steps := []struct{ input, tool, search string }{
		{"여행 메모를 저장해줘", "write_record", ""},
		{"연필을 검색해줘", "query_records", scenario.Attached},
		{"메모를 모두 보여줘", "query_records", ""},
		{"연필 검색해줘", "query_records", scenario.Noun},
		{"연필 두 자루로 메모를 수정해줘", "write_record", ""},
		{"수정한 메모에서 연필 검색해줘", "query_records", scenario.Noun},
		{"재시작 후 연필 검색해줘", "query_records", scenario.Noun},
		{"저장한 메모를 읽어줘", "read_record", ""},
	}
	var calls atomic.Int32
	var saved coreexec.RecordReceipt // Only the serial provider loop accesses it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := int(calls.Add(1)) - 1
		if n/2 >= len(steps) {
			t.Errorf("unexpected provider call %d", n)
			emitResponse(w, "Unexpected.", true)
			return
		}
		step := steps[n/2]
		if n%2 == 0 {
			if !strings.Contains(string(mustJSON(t, request.Input)), step.input) {
				t.Error("typed request did not reach provider", step.input)
			}
			var args any
			switch step.tool {
			case "write_record":
				note := scenario.Note
				if saved.ID != "" {
					note = scenario.Updated
				}
				args = coreexec.RecordWrite{ID: saved.ID, Revision: saved.Revision, Kind: "memo", Title: scenario.Title, Note: note}
			case "query_records":
				args = coreexec.RecordQuery{Search: step.search, Kind: "memo", Limit: 10}
			case "read_record":
				args = coreexec.RecordRead{ID: saved.ID}
			}
			emitPersonaCall(w, fmt.Sprintf("record-%d", n), step.tool, args)
			return
		}
		var item struct{ Type, Output string }
		var result struct {
			Code   string
			Output json.RawMessage
		}
		if len(request.Input) == 0 {
			t.Error("missing tool result")
			return
		}
		if err := json.Unmarshal(request.Input[len(request.Input)-1], &item); err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
			t.Error(err)
		}
		if item.Type != "function_call_output" || result.Code != "ok" {
			t.Errorf("%s: %s", step.tool, item.Output)
		}
		note := scenario.Note
		if n/2 >= 4 {
			note = scenario.Updated
		}
		if step.tool == "query_records" {
			var got struct{ Rows []records.Row }
			if err := json.Unmarshal(result.Output, &got); err != nil {
				t.Error(err)
			}
			if len(got.Rows) != 1 || got.Rows[0].ID != saved.ID || got.Rows[0].Revision != saved.Revision || got.Rows[0].Title != scenario.Title || got.Rows[0].Note != note {
				t.Errorf("search=%q step=%d: rows=%+v; want saved memo", step.search, n/2, got.Rows)
			}
		} else {
			var got coreexec.RecordReceipt
			if err := json.Unmarshal(result.Output, &got); err != nil {
				t.Error(err)
			}
			if got.ID == "" || len(got.Revision) != 40 || !strings.Contains(got.Markdown, note) || (saved.ID != "" && got.ID != saved.ID) {
				t.Errorf("invalid receipt: %+v", got)
			}
			if step.tool == "read_record" && got != saved {
				t.Error("restart changed saved memo")
			}
			if step.tool == "write_record" && got.Revision == saved.Revision {
				t.Error("write did not commit a new revision")
			}
			saved = got
		}
		emitResponse(w, fmt.Sprintf("Record step %d checked.", n/2), true)
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, _ := conversationClient(t, h)
	terminal, _ := h.startConversationTUI(100, 30, "TERM=xterm-256color")
	for i, step := range steps {
		if i == 6 {
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			c.Close()
			h.stopCore(core)
			core = start(true)
			c, _ = conversationClient(t, h)
			terminal = h.startScreenTUI("TERM=xterm-256color")
			waitScreen(t, h, terminal, "Record step 5 checked.")
		}
		waitComposerReady(t, h, terminal)
		h.keys(terminal, step.input+"\r")
		for {
			frame, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
				break
			}
		}
		waitScreen(t, h, terminal, fmt.Sprintf("Record step %d checked.", i))
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	c.Close()
	h.stopCore(core)
	if got := calls.Load(); got != int32(2*len(steps)) {
		t.Fatalf("provider calls=%d want=%d", got, 2*len(steps))
	}
}
