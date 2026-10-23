// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/records"
	resultkit "github.com/thisisjun786/lina/kit/result"
)

// Alias provenance is fixture data admitted through the ledger while Core is
// stopped. Adoption, completion/cancellation and replay proposals use live tools.
func seedCandidateAliases(t *testing.T, h *harness, source string) {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.Epoch()
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	events := ledger.New(db, nil)
	original, err := events.Get(h.ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"candidate-replay", "candidate-view"} {
		in := ledger.Input{ID: id, Locator: id, PersonID: original.PersonID, AgentID: original.AgentID, ConversationID: original.ConversationID, Surface: original.Surface, Body: original.Body.Text(), OccurredAt: original.OccurredAt, RecordedAt: original.RecordedAt, TimeQuality: "observed"}
		if id == "candidate-replay" {
			in.ReplayOf = source
		} else {
			in.Relations = []ledger.Relation{{Kind: "view_of", TargetID: source}}
		}
		if _, err = events.Admit(h.ctx, guard.EntryOwner, in); err != nil {
			t.Fatal(err)
		}
	}
}

func TestM2RecordCandidateReplayAfterFinish(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../m2/record-candidate-replay.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Quotation, Title string
		States           []string
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, state := range scenario.States {
		t.Run(state, func(t *testing.T) {
			var calls atomic.Int32
			var added, replay records.Candidate
			var saved, finished coreexec.RecordReceipt
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct{ Input []json.RawMessage }
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
					if err := json.Unmarshal(req.Input[len(req.Input)-1], &item); err != nil {
						t.Error(err)
						return
					}
					if err := json.Unmarshal([]byte(item.Output), &got); err != nil || item.Type != "function_call_output" || got.Code != "ok" {
						t.Errorf("tool result %s: %v", item.Output, err)
						return
					}
					if err := json.Unmarshal(got.Output, out); err != nil {
						t.Error(err)
					}
				}
				propose := func(id, source string) {
					emitPersonaCall(w, id, "propose_candidate", records.CandidateInput{Source: source, Quotation: scenario.Quotation, Title: scenario.Title, Confidence: 1})
				}
				switch n := calls.Add(1); n {
				case 1:
					propose("adopt", "")
				case 2:
					result(&added)
					if added.Status != "added" || added.RecordID == "" || added.Undo == "" {
						t.Error("missing adoption", added)
					}
					emitPersonaCall(w, "read", "read_record", coreexec.RecordRead{ID: added.RecordID})
				case 3:
					result(&saved)
					var proof *resultkit.CompletionRequest
					if state == "completed" {
						proof = &resultkit.CompletionRequest{WorkKind: "document", Evidence: []resultkit.EvidenceReference{{Kind: "source_reopened", CallID: "read"}}}
					}
					emitPersonaCall(w, "finish", "write_record", coreexec.RecordWrite{ID: saved.ID, Revision: saved.Revision, Kind: "todo", Title: scenario.Title, Completion: proof, Todo: &records.Todo{State: state, Source: added.Source}})
				case 4:
					result(&finished)
					if finished.ID != added.RecordID || finished.Revision == saved.Revision || !strings.Contains(finished.Markdown, `state: "`+state+`"`) {
						t.Error("task did not finish", finished)
					}
					emitResponse(w, "Saved the task state.", true)
				case 5:
					propose("replay", "candidate-replay")
				case 6:
					result(&replay)
					if replay.Status != "pending" || replay.RecordID != "" || replay.Undo != "" || replay.Notice != "" {
						t.Error("replay repeated finished adoption", replay)
					}
					propose("view", "candidate-view")
				case 7:
					var view records.Candidate
					result(&view)
					if view.Status != "pending" || view.RecordID != "" || view.Undo != "" || view.Notice != "" || view.Number == replay.Number {
						t.Error("view repeated finished adoption", view)
					}
					emitPersonaCall(w, "list", "query_records", coreexec.RecordQuery{Kind: "todo", Limit: 100})
				case 8:
					var list struct{ Rows []records.Row }
					result(&list)
					if len(list.Rows) != 1 || list.Rows[0].ID != added.RecordID || list.Rows[0].State != state {
						t.Error("replay changed records", list)
					}
					emitPersonaCall(w, "read-finished", "read_record", coreexec.RecordRead{ID: added.RecordID})
				case 9:
					var got coreexec.RecordReceipt
					result(&got)
					if got != finished {
						t.Error("replay changed the saved version", got)
					}
					emitResponse(w, "The finished task remains unchanged.", true)
				default:
					t.Errorf("unexpected provider call %d", n)
					emitResponse(w, "Unexpected request.", true)
				}
			}))
			defer server.Close()
			h, start := installedConversationHarness(t, server.URL)
			core := start(true)
			c, conversation := conversationClient(t, h)
			admittedTurn(t, h, c, conversation, "source", scenario.Quotation)
			c.Close()
			h.stopCore(core)
			seedCandidateAliases(t, h, added.Source)
			core = start(true)
			c, conversation = conversationClient(t, h)
			admittedTurn(t, h, c, conversation, "replay-proposals", "Check the retained candidates.")
			c.Close()
			h.stopCore(core)
			if calls.Load() != 9 {
				t.Fatal("provider calls", calls.Load())
			}
			db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var notices, operations int
			if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM conv_event WHERE scaffold_kind='record_change'").Scan(&notices); err != nil || notices != 1 {
				t.Fatal("replay emitted an addition notice", notices, err)
			}
			if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM record_operations").Scan(&operations); err != nil || operations != 1 {
				t.Fatal("replay published an undo operation", operations, err)
			}
		})
	}
}
