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

func TestM2RecordsIndexInstalledCore(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../m2/records-index.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Title, Note, Search, Outside string
		Tools                        []string
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var saved coreexec.RecordReceipt
	var root string // Set before Core can start making provider requests.
	var recordPath string
	var liveRows string
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
		for _, name := range scenario.Tools {
			if !names[name] {
				t.Errorf("missing installed tool %s", name)
			}
		}
		result := func(want string, out any) {
			var item struct{ Type, Output string }
			var got struct {
				Code   string
				Output json.RawMessage
			}
			if len(req.Input) == 0 {
				t.Error("no provider input")
				return
			}
			if err := json.Unmarshal(req.Input[len(req.Input)-1], &item); err != nil {
				t.Error(err)
			}
			if err := json.Unmarshal([]byte(item.Output), &got); err != nil {
				t.Error(err)
			}
			if item.Type != "function_call_output" || got.Code != want {
				t.Errorf("tool result %s", item.Output)
			}
			if err := json.Unmarshal(got.Output, out); err != nil {
				t.Error(err)
			}
		}
		query := func(id string) {
			emitPersonaCall(w, id, "query_records", coreexec.RecordQuery{Search: scenario.Search, Kind: "memo", Limit: 10})
		}
		checkRows := func() string {
			var listed struct{ Rows []records.Row }
			result("ok", &listed)
			if len(listed.Rows) != 1 || listed.Rows[0].ID != saved.ID || listed.Rows[0].Revision != saved.Revision || listed.Rows[0].Note != scenario.Note {
				t.Errorf("query rows %+v", listed)
			}
			data, _ := json.Marshal(listed)
			return string(data)
		}
		outside := func(code string) {
			var got struct {
				Code    string
				Changes []records.OutsideChange `json:"outside_changes"`
				Total   int
			}
			result(code, &got)
			if got.Code != "records_changed" || got.Total != 1 || len(got.Changes) != 1 || got.Changes[0].Kind != "modified" || got.Changes[0].Validation != "invalid" {
				t.Errorf("missing outside notice %+v", got)
			}
		}
		switch n := calls.Add(1); n {
		case 1:
			emitPersonaCall(w, "save", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.Title, Note: scenario.Note})
		case 2:
			result("ok", &saved)
			query("search-live")
		case 3:
			liveRows = checkRows()
			emitResponse(w, "Saved and found the measurements.", true)
		case 4:
			query("search-rebuilt")
		case 5:
			if got := checkRows(); got != liveRows {
				t.Error("rebuilt query differs from live query")
			}
			// Change the published file while the actual Core is running.
			if err := os.WriteFile(recordPath, []byte(scenario.Outside), 0600); err != nil {
				t.Error(err)
			}
			emitPersonaCall(w, "outside-read", "read_record", coreexec.RecordRead{ID: saved.ID})
		case 6:
			outside("failed")
			query("outside-query")
		case 7:
			outside("failed")
			emitPersonaCall(w, "outside-write", "write_record", coreexec.RecordWrite{ID: saved.ID, Revision: saved.Revision, Kind: "memo", Title: scenario.Title, Note: "This must not overwrite the outside edit."})
		case 8:
			outside("denied")
			emitResponse(w, "The saved record changed outside LINA. The change has not been merged.", true)
		case 9:
			query("outside-after-restart")
		case 10:
			outside("failed")
			emitResponse(w, "The outside change is still waiting for reconciliation.", true)
		default:
			t.Errorf("unexpected provider call %d", n)
			emitResponse(w, "Unexpected.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	root = h.root
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "index-create", "Save and find the prototype measurements.")
	c.Close()
	h.stopCore(core)
	repo := filepath.Join(root, "versions/records")
	entries, err := os.ReadDir(filepath.Join(repo, "inbox"))
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	recordPath = filepath.Join(repo, "inbox", entries[0].Name())
	ref := filepath.Join(repo, ".git/refs/heads/records")
	before, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "canon/canon.sqlite")
	db, err := canon.OpenSQLite(h.ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Lose the derived data while Core is stopped; leave canonical files intact.
	for _, statement := range []string{"DELETE FROM records_edges", "DELETE FROM records_text", "DELETE FROM records_fts", "DELETE FROM records_index", "UPDATE records_index_state SET head='unbuilt'"} {
		if _, err = db.ExecContext(h.ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "index-restart", "Find the saved measurements again and check whether they can be revised.")
	c.Close()
	h.stopCore(core)
	// Outside changes must be reportable even after Core restarts.
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "outside-restart", "Check the saved measurements again.")
	c.Close()
	h.stopCore(core)
	if calls.Load() != 10 {
		t.Fatalf("calls=%d want 10", calls.Load())
	}
	after, err := os.ReadFile(ref)
	if err != nil || string(before) != string(after) {
		t.Fatal("outside change was committed", err)
	}
	content, err := os.ReadFile(recordPath)
	if err != nil || string(content) != scenario.Outside {
		t.Fatal("outside file was overwritten", err)
	}
	reader, err := canon.OpenSQLiteReader(h.ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var head, note string
	var matches int
	if err = reader.QueryRowContext(h.ctx, "SELECT head FROM records_index_state").Scan(&head); err != nil || head != strings.TrimSpace(string(before)) {
		t.Fatal(head, err)
	}
	if err = reader.QueryRowContext(h.ctx, "SELECT note FROM records_text WHERE id=?", saved.ID).Scan(&note); err != nil || note != scenario.Note {
		t.Fatal("outside text entered index", note, err)
	}
	if err = reader.QueryRowContext(h.ctx, "SELECT count(*) FROM records_fts WHERE records_fts MATCH ?", scenario.Search).Scan(&matches); err != nil || matches != 1 {
		t.Fatal("search index not restored", matches, err)
	}
}
