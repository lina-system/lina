// SPDX-License-Identifier: Apache-2.0
package replay_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestRecallResultReferencesAreRevalidated(t *testing.T) {
	for _, kind := range []string{"event", "memory-scope", "memory-revision", "summary", "record", "record-citation"} {
		t.Run(kind, func(t *testing.T) {
			db, source, _ := fixture(t)
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			event, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "outside", ConversationID: "earlier", AgentID: "agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "observatory violet"})
			if err != nil {
				t.Fatal(err)
			}
			hit := recall.Result{Hit: recall.Hit{Kind: recall.Event, ID: event.ID, Revision: "0"}, Excerpt: "observatory violet"}
			change := func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO retract_block(item_id,person,holder,event_id,conversation_id) VALUES('removal','person','agent','outside','')")
				return err
			}
			switch kind {
			case "memory-scope", "memory-revision":
				item, err := coreexec.NewMemory(db, nil, nil).Remember(t.Context(), memory.Write{Person: "person", Agent: "agent", Item: memory.Item{Kind: memory.Fact, Content: event.Body.Text(), Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: event.ID, Precision: memory.Exact}}}})
				if err != nil {
					t.Fatal(err)
				}
				hit.Kind, hit.ID, hit.Revision = recall.Memory, item.ID, "1"
				change = func(tx *canon.Tx) error {
					if kind == "memory-scope" {
						_, err := tx.ExecContext(t.Context(), "UPDATE memory_item SET scope='private',record=json_set(record,'$.scope','private') WHERE id=?", item.ID)
						return err
					}
					_, err := tx.ExecContext(t.Context(), "UPDATE memory_item SET record=json_set(record,'$.revision',2) WHERE id=?", item.ID)
					return err
				}
			case "summary":
				summaries := compaction.New(db)
				frontier, err := summaries.Frontier(t.Context(), "earlier")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := summaries.Publish(t.Context(), compaction.Proposal{Expected: frontier, ID: "node", Cover: []compaction.Coverage{{EventID: event.ID}}, Output: compaction.Output{Text: "observatory violet", Model: "fixture", PromptVersion: "1", BuiltAt: at, Dropped: "none"}}); err != nil {
					t.Fatal(err)
				}
				hit.Kind, hit.ID, hit.Revision = recall.Summary, "node", "1"
			case "record", "record-citation":
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					if _, err := tx.ExecContext(t.Context(), "INSERT INTO records_index VALUES('record','inbox/record.md','blob','r1')"); err != nil {
						return err
					}
					_, err := tx.ExecContext(t.Context(), "INSERT INTO records_text VALUES('record','person','memo','active','observatory','violet')")
					return err
				}); err != nil {
					t.Fatal(err)
				}
				hit.Kind, hit.ID, hit.Revision = recall.Record, "record", "r1"
				if kind == "record" {
					change = func(tx *canon.Tx) error {
						_, err := tx.ExecContext(t.Context(), "UPDATE records_index SET revision='r2' WHERE id='record'")
						return err
					}
				} else {
					hit.Citations = []canon.Citation{{TargetKind: "event", TargetID: event.ID, TargetRevision: "0"}}
				}
			}
			id := seed(t, db, "input", guard.EntryOwner)
			call := provider.ToolCall{ID: "recall-call", Name: "recall", Arguments: `{"query":"observatory"}`}
			body, _ := json.Marshal(struct {
				Results []recall.Result `json:"results"`
			}{[]recall.Result{hit}})
			raw, _ := json.Marshal(call)
			result, _ := json.Marshal(loop.Result{ID: call.ID, Code: loop.OK, Output: body})
			if err := db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call,result) VALUES(?,0,0,?,?,?)", id, call.ID, raw, result)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			scope := replay.Scope{Person: "person", Agent: "agent"}
			ep, err := source.Load(t.Context(), scope, id)
			if err != nil {
				t.Fatalf("visible result excluded: %v", err)
			}
			if err := source.ValidateEpisode(t.Context(), ep); err != nil {
				t.Fatalf("visible read refused before dispatch: %v", err)
			}
			if err := db.Write(t.Context(), change); err != nil {
				t.Fatal(err)
			}
			if err := source.ValidateEpisode(t.Context(), ep); err == nil {
				t.Error("loaded episode retained revoked tool source")
			}
			if _, err := source.Load(t.Context(), scope, id); err == nil {
				t.Error("revoked recall source loaded")
			}
			_, manifest, err := source.Episodes(t.Context(), scope, selection())
			if err != nil || manifest.Exported != 0 || manifest.LeftOut != 1 {
				t.Fatalf("revoked recall exported: %+v %v", manifest, err)
			}
		})
	}
}

func TestExportRejectsTransitiveProtectedSources(t *testing.T) {
	for _, scope := range []memory.Scope{memory.Local, memory.Private, memory.NeverLeaves} {
		for _, copied := range []bool{false, true} {
			name := string(scope)
			if copied {
				name += "/view-of-replay"
			}
			t.Run(name, func(t *testing.T) {
				db, source, _ := fixture(t)
				id := seed(t, db, "input", guard.EntryOwner)
				evidenceID := "input"
				if copied {
					at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
					for _, eventID := range []string{"original", "copy"} {
						in := ledger.Input{ID: eventID, ConversationID: "earlier", PersonID: "person", AgentID: "agent", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "protected fact"}
						if eventID == "copy" {
							in.ReplayOf = "original"
						}
						if _, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, in); err != nil {
							t.Fatal(err)
						}
					}
					if err := db.Write(t.Context(), func(tx *canon.Tx) error {
						_, err := tx.ExecContext(t.Context(), "INSERT INTO ev_rel(event_id,target_id,kind) VALUES('input','copy','view_of')")
						return err
					}); err != nil {
						t.Fatal(err)
					}
					evidenceID = "original"
				}
				readScope := replay.Scope{Person: "person", Agent: "agent"}
				ep, err := source.Load(t.Context(), readScope, id)
				if err != nil {
					t.Fatal(err)
				}
				mem := coreexec.NewMemory(db, nil, nil)
				item, err := mem.Remember(t.Context(), memory.Write{Person: "person", Agent: "agent", Item: memory.Item{Kind: memory.Fact, Content: "Source memory.", Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: evidenceID, Precision: memory.Exact}}}})
				if err != nil {
					t.Fatal(err)
				}
				err = db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := memory.Insert(t.Context(), tx, memory.Write{Person: "person", Agent: "agent", Item: memory.Item{Kind: memory.Fact, Content: "Sensitive umbrella classification.", Scope: scope, Subject: memory.Subject{Kind: memory.World, ID: "observatory"}, Basis: memory.Derived, Evidence: []memory.Evidence{{Kind: memory.MemoryItem, ID: item.ID, Revision: item.Revision, Precision: memory.Exact}}}}, func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
						return memory.Origin{Person: "person", Agent: "agent", Speaker: guard.Person, Scope: memory.Local}, nil
					})
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				allowed := scope == memory.Local
				if err := source.ValidateEpisode(t.Context(), ep); (err == nil) != allowed {
					t.Errorf("dispatch allowed=%v, err=%v", allowed, err)
				}
				if _, err := source.ReadEvent(t.Context(), readScope, "input"); (err == nil) != allowed {
					t.Errorf("read allowed=%v, err=%v", allowed, err)
				}
				if _, err := source.Load(t.Context(), readScope, id); (err == nil) != allowed {
					t.Errorf("load allowed=%v, err=%v", allowed, err)
				}
				_, manifest, err := source.Episodes(t.Context(), readScope, selection())
				if err != nil || (manifest.Exported == 1) != allowed || !allowed && manifest.LeftOut != 1 {
					t.Errorf("export allowed=%v: %+v, err=%v", allowed, manifest, err)
				}
			})
		}
	}
}
