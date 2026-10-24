// SPDX-License-Identifier: Apache-2.0
package replay_test

import (
	"encoding/json"
	"strings"
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

// A recorded recall of a replay or view must be refused once any event in that
// copy chain is private or never-leaves evidence, including one hop further
// along the chain. An event outside the chain stays readable.
func TestRecordedCopyOfProtectedEvidenceIsRefused(t *testing.T) {
	for _, memoryScope := range []string{"private", "never-leaves"} {
		for _, link := range []string{"replay_of", "view_of", "chain", "reverse"} {
			t.Run(memoryScope+"/"+link, func(t *testing.T) {
				db, source, _ := fixture(t)
				at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				admit := func(id, body, replay string, rels ...ledger.Relation) ledger.Event {
					t.Helper()
					event, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{
						ID: id, ConversationID: "earlier", AgentID: "agent", PersonID: "person",
						Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed",
						Body: body, ReplayOf: replay, Relations: rels,
					})
					if err != nil {
						t.Fatal(err)
					}
					return event
				}
				original := admit("original", "aurora private fact", "")
				var copy ledger.Event
				switch link {
				case "view_of":
					copy = admit("copy", "a view of the aurora private fact", "", ledger.Relation{TargetID: original.ID, Kind: "view_of"})
				case "chain":
					mid := admit("mid", "a view of the aurora private fact", "", ledger.Relation{TargetID: original.ID, Kind: "view_of"})
					copy = admit("copy", "a replay of the aurora private fact", mid.ID)
				default:
					copy = admit("copy", "a replay of the aurora private fact", original.ID)
				}
				recorded, cited := copy, original
				if link == "reverse" {
					// Evidence names the copy. The recorded reference is the parent.
					recorded, cited = original, copy
				}
				other := admit("other", "public observatory note", "")
				turnID := seed(t, db, "input", guard.EntryOwner)
				hit := recall.Result{Hit: recall.Hit{Kind: recall.Event, ID: recorded.ID, Revision: "0"}, Excerpt: recorded.Body.Text()}
				payload, _ := json.Marshal(struct {
					Results []recall.Result `json:"results"`
				}{[]recall.Result{hit}})
				call := provider.ToolCall{ID: "recall-call", Name: "recall", Arguments: `{"query":"aurora"}`}
				raw, _ := json.Marshal(call)
				result, _ := json.Marshal(loop.Result{ID: call.ID, Code: loop.OK, Output: payload})
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), "INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call,result) VALUES(?,0,0,?,?,?)", turnID, call.ID, raw, result)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				scope := replay.Scope{Person: "person", Agent: "agent"}
				episode, err := source.Load(t.Context(), scope, turnID)
				if err != nil || len(episode.Calls) != 1 || !strings.Contains(string(episode.Calls[0].Result.Output), "aurora private fact") {
					t.Fatalf("copy excerpt not exported before restriction: %v %+v", err, episode.Calls)
				}
				if got, err := source.ReadEvent(t.Context(), scope, other.ID); err != nil || got.Text != "public observatory note" {
					t.Fatalf("unrelated event before restriction: %v %+v", err, got)
				}
				record := `{"evidence":[{"kind":"event","id":"` + cited.ID + `"}]}`
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `INSERT INTO memory_item(id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
 VALUES('protected','person','all','person','person','synthetic','synthetic',?,'active',0,0,'',0,?)`, memoryScope, record)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := source.Load(t.Context(), scope, turnID); err != replay.Exclusion("restricted_content") {
					t.Fatalf("protected copy exported: %v", err)
				}
				_, manifest, err := source.Episodes(t.Context(), scope, selection())
				if err != nil || manifest.Exported != 0 || manifest.LeftOut != 1 || manifest.Reasons["restricted_content"] != 1 {
					t.Fatalf("protected copy exported: %+v %v", manifest, err)
				}
				if _, err := source.ReadEvent(t.Context(), scope, copy.ID); err != replay.Exclusion("restricted_content") {
					t.Fatalf("copy still readable: %v", err)
				}
				if _, err := source.ReadEvent(t.Context(), scope, original.ID); err != replay.Exclusion("restricted_content") {
					t.Fatalf("original still readable: %v", err)
				}
				if got, err := source.ReadEvent(t.Context(), scope, other.ID); err != nil || got.Text != "public observatory note" {
					t.Fatalf("unrelated event hidden: %v %+v", err, got)
				}
			})
		}
	}
}
