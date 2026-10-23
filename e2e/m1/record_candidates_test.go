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

func TestM2RecordCandidatesInstalledCore(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../m2/record-candidates.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Quotation, Title, Collision string
		ForgedQuotation             string `json:"forged_quotation"`
		ForgedTitle                 string `json:"forged_title"`
		MissingSource               string `json:"missing_source"`
		MissingTitle                string `json:"missing_title"`
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var added, forged, missing records.Candidate
	var saved, occupied coreexec.RecordReceipt
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
				t.Error("missing tool result")
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
		propose := func(id, source, title, quote string) {
			emitPersonaCall(w, id, "propose_candidate", records.CandidateInput{Source: source, Title: title, Quotation: quote, Confidence: 0.99})
		}
		switch n := calls.Add(1); n {
		case 1:
			names := map[string]bool{}
			for _, tool := range req.Tools {
				names[tool.Name] = true
			}
			if !names["propose_candidate"] || !names["list_candidates"] {
				t.Error("missing candidate tools")
			}
			emitPersonaCall(w, "occupy-path", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.Collision})
		case 2:
			result(&occupied)
			propose("verified", "", scenario.Title, scenario.Quotation)
		case 3:
			result(&added)
			if added.Status != "added" || added.RecordID == "" || added.Undo == "" || added.Source == "" || added.SourceRevision == "" {
				t.Error("missing verified receipt", added)
			}
			propose("forged", added.Source, scenario.ForgedTitle, scenario.ForgedQuotation)
		case 4:
			result(&forged)
			if forged.Status != "pending" || forged.RecordID != "" || forged.Undo != "" {
				t.Error("forged quote adopted", forged)
			}
			propose("missing", scenario.MissingSource, scenario.MissingTitle, "Please confirm the equipment booking.")
		case 5:
			result(&missing)
			if missing.Status != "pending" || missing.RecordID != "" {
				t.Error("missing source adopted", missing)
			}
			emitPersonaCall(w, "read-added", "read_record", coreexec.RecordRead{ID: added.RecordID})
		case 6:
			result(&saved)
			emitResponse(w, "Added the verified commitment. The other two remain candidates.", true)
		case 7:
			propose("duplicate", "", scenario.Title, scenario.Quotation)
		case 8:
			var c records.Candidate
			result(&c)
			if c.Status != "pending" || c.RecordID != "" {
				t.Error("open equivalent added", c)
			}
			emitPersonaCall(w, "undo-added", "undo_record", coreexec.RecordAction{ID: added.RecordID, Revision: saved.Revision, Handle: added.Undo})
		case 9:
			var undone records.Applied
			result(&undone)
			if undone.ID != added.RecordID || undone.Revision == saved.Revision {
				t.Error("undo failed", undone)
			}
			emitResponse(w, "Undid the automatic addition.", true)
		case 10:
			propose("replay", added.Source, scenario.Title, scenario.Quotation)
		case 11:
			var c records.Candidate
			result(&c)
			if c.Number != added.Number || c.Status != "rejected" {
				t.Error("undone candidate returned", c)
			}
			emitPersonaCall(w, "list-candidates", "list_candidates", coreexec.CandidateQuery{})
		case 12:
			var list struct{ Candidates []records.Candidate }
			result(&list)
			found := map[int64]records.Candidate{}
			for _, c := range list.Candidates {
				found[c.Number] = c
			}
			if len(list.Candidates) != 4 || found[forged.Number].Status != "pending" || found[missing.Number].Status != "pending" || found[added.Number].Status != "rejected" {
				t.Error("restart lost candidates", list)
			}
			emitPersonaCall(w, "list-todos", "query_records", coreexec.RecordQuery{Kind: "todo", Limit: 100})
		case 13:
			var list struct{ Rows []records.Row }
			result(&list)
			if len(list.Rows) != 1 || list.Rows[0].ID != added.RecordID || list.Rows[0].State != "canceled" {
				t.Error("candidate side effects", list)
			}
			emitPersonaCall(w, "read-occupied", "read_record", coreexec.RecordRead{ID: occupied.ID})
		case 14:
			var got coreexec.RecordReceipt
			result(&got)
			if got != occupied {
				t.Error("candidate addition or undo changed the occupied record", got)
			}
			emitResponse(w, "The three pending candidates remain available. The undone addition stays rejected.", true)
		default:
			t.Errorf("unexpected provider call %d", n)
			emitResponse(w, "Unexpected request.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "candidate-source", strings.Repeat("　", 6000)+scenario.Quotation)
	admittedTurn(t, h, c, conversation, "candidate-duplicate", scenario.Quotation)
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "candidate-restart", "Show the retained candidates.")
	c.Close()
	h.stopCore(core)
	if calls.Load() != 14 {
		t.Fatal("provider calls", calls.Load())
	}
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var material string
	if err = db.QueryRowContext(h.ctx, "SELECT material_id FROM conv_event WHERE id=?", added.Source).Scan(&material); err != nil || material == "" {
		t.Fatal("large source was not resolved through retained material", material, err)
	}
	var notices int
	if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM conv_event WHERE scaffold_kind='record_change'").Scan(&notices); err != nil || notices != 2 {
		t.Fatal("automatic addition and undo notices", notices, err)
	}
}
