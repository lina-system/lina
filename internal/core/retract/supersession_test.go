// SPDX-License-Identifier: Apache-2.0
package retract_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func TestSupersessionRemovalAcrossMemoryWriters(t *testing.T) {
	for _, writer := range []string{"remember", "insert", "extraction", "promotion"} {
		for _, permanent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/permanent-%t", writer, permanent), func(t *testing.T) {
				db, removals, flow, root, close := memoryFixture(t)
				var ids []string
				snapshot := filepath.Join(t.TempDir(), "canon.sqlite")
				for i := range 3 {
					event, text := fmt.Sprintf("event-%d", i), fmt.Sprintf("synthetic chain preference %d", i)
					_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: event, ConversationID: event, PersonID: "person-1", AgentID: "agent-1", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: text})
					if err != nil {
						t.Fatal(err)
					}
					in := memoryWrite(event, text)
					if len(ids) > 0 {
						in.Item.Supersedes = ids[len(ids)-1]
					}
					resolve := func(ctx context.Context, tx *canon.Tx, ref memory.Evidence) (memory.Origin, error) {
						var o memory.Origin
						err := tx.QueryRowContext(ctx, "SELECT person_id,agent_id,speaker_kind,body FROM conv_event WHERE id=?", ref.ID).Scan(&o.Person, &o.Agent, &o.Speaker, &o.Quote)
						o.ID, o.Scope = ref.ID, memory.Local
						return o, err
					}
					var id string
					if writer == "remember" {
						receipt, err := flow.Remember(t.Context(), in)
						if err != nil {
							t.Fatal(err)
						}
						id = receipt.ID
					} else {
						err = db.Write(t.Context(), func(tx *canon.Tx) error {
							var item memory.Item
							var err error
							switch writer {
							case "insert":
								item, err = memory.Insert(t.Context(), tx, in, resolve)
							case "promotion":
								in.Item.Basis = memory.Imported
								item, err = memory.Promote(t.Context(), tx, in, "synthetic-confirmation", resolve)
							case "extraction":
								batch := memory.ExtractionBatch{Agent: in.Agent, Person: in.Person, Utterances: []memory.Utterance{{Events: in.Item.Evidence}}}
								if err = memory.ApplyExtraction(t.Context(), tx, batch, []memory.Item{in.Item}, resolve); err == nil {
									err = tx.QueryRowContext(t.Context(), "SELECT id FROM memory_item WHERE content=?", text).Scan(&item.ID)
								}
							}
							id = item.ID
							return err
						})
						if err != nil {
							t.Fatal(err)
						}
					}
					ids = append(ids, id)
					if i == 0 {
						snapshotCanon(t, filepath.Join(root, "canon.sqlite"), snapshot)
					}
				}
				request := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: ids[2]}}}
				if permanent {
					if err := removals.Delete(t.Context(), request); err != nil {
						t.Fatal(err)
					}
				} else if err := removals.Forget(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				check := func() {
					t.Helper()
					hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Person: "person-1", Agent: "agent-1"}, memory.Query{At: 100, History: true})
					if err != nil || len(hits) != 0 {
						t.Errorf("chain still recallable: %+v %v", hits, err)
					}
					for i, id := range ids {
						err := db.Read(t.Context(), func(tx *canon.Tx) error {
							var raw []byte
							if err := tx.QueryRowContext(t.Context(), "SELECT provenance FROM retract_target WHERE id=?", id).Scan(&raw); err != nil {
								return err
							}
							var target retract.Target
							if err := json.Unmarshal(raw, &target); err != nil {
								return err
							}
							if len(target.Events) != 1 || target.Events[0] != fmt.Sprintf("event-%d", i) {
								t.Errorf("missing chain evidence: %+v", target)
							}
							return nil
						})
						if err != nil {
							t.Errorf("chain target %d missing: %v", i, err)
						}
					}
				}
				check()
				list, err := os.ReadFile(filepath.Join(root, "backups/removals"))
				if err != nil {
					t.Fatal(err)
				}
				close()
				restored := filepath.Join(t.TempDir(), "state")
				if err := os.MkdirAll(filepath.Join(restored, "backups"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(restored, "backups/removals"), list, 0600); err != nil {
					t.Fatal(err)
				}
				snapshotCanon(t, snapshot, filepath.Join(restored, "canon.sqlite"))
				db, _, _, _, _ = memoryAt(t, restored)
				check()
			})
		}
	}
}

func TestSupersessionRecoveryAndOverlappingTargets(t *testing.T) {
	for _, state := range []string{"active", "trashed", "erased"} {
		t.Run(state, func(t *testing.T) {
			db, s, flow, _, _ := memoryFixture(t)
			first := seedMemory(t, db, flow, "first", "first", "synthetic first preference")
			seedMemory(t, db, flow, "second", "second", "synthetic second evidence")
			next := memoryWrite("second", "synthetic second preference")
			next.Item.Supersedes = first.ID
			second, err := flow.Remember(t.Context(), next)
			if err != nil {
				t.Fatal(err)
			}
			original := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: first.ID}}}
			switch state {
			case "trashed":
				if err := s.Forget(t.Context(), original); err != nil {
					t.Fatal(err)
				}
			case "erased":
				if err := s.Delete(t.Context(), original); err != nil {
					t.Fatal(err)
				}
			}
			latest := retract.Request{Person: "person-1", Targets: []retract.Ref{{Kind: retract.Memory, ID: second.ID}}}
			both := latest
			if state == "active" {
				both.Targets = append(both.Targets, retract.Ref{Kind: retract.Memory, ID: first.ID})
			}
			if err := s.Forget(t.Context(), both); err != nil {
				t.Fatal(err)
			}
			if err := s.Restore(t.Context(), latest); err != nil {
				t.Fatal(err)
			}
			hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Person: "person-1", Agent: "agent-1"}, memory.Query{ID: first.ID, At: 100, History: true})
			if err != nil || len(hits) != 0 {
				t.Fatalf("recovery revived superseded fact: %+v %v", hits, err)
			}
			hits, err = memory.New(db).Recall(t.Context(), memory.RecallScope{Person: "person-1", Agent: "agent-1"}, memory.Query{ID: second.ID, At: 100})
			if err != nil || len(hits) != 1 {
				t.Fatalf("recovery lost requested fact: %+v %v", hits, err)
			}
			if err := s.Delete(t.Context(), latest); err != nil {
				t.Fatal(err)
			}
		})
	}
}
