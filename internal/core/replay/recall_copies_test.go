// SPDX-License-Identifier: Apache-2.0
package replay_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/replay"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestRecordedRecallOfProtectedCopiesIsLeftOut(t *testing.T) {
	for _, tc := range []struct {
		name      string
		replay    bool
		protected bool
	}{
		{"public view", false, false},
		{"view of protected event", false, true},
		{"replay copy of protected event", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, source, _ := fixture(t)
			at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			admit := func(id, replayOf string) {
				if _, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: "other-chat", AgentID: "agent", PersonID: "person", ReplayOf: replayOf, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "The aurora private fact."}); err != nil {
					t.Fatal(err)
				}
			}
			admit("original", "")
			if tc.replay {
				admit("derived", "original")
			} else {
				admit("derived", "")
				if err := db.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `INSERT INTO ev_rel(event_id,target_id,kind) VALUES('derived','original','view_of')`)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			id := seed(t, db, "input", guard.EntryOwner)
			hit := recall.Result{Hit: recall.Hit{Kind: recall.Event, ID: "derived", Revision: "0"}, Excerpt: "The aurora private fact."}
			body, _ := json.Marshal(struct {
				Results []recall.Result `json:"results"`
			}{[]recall.Result{hit}})
			call := provider.ToolCall{ID: "recall-call", Name: "recall", Arguments: `{"query":"aurora"}`}
			raw, _ := json.Marshal(call)
			result, _ := json.Marshal(loop.Result{ID: call.ID, Code: loop.OK, Output: body})
			if err := db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call,result) VALUES(?,0,0,?,?,?)", id, call.ID, raw, result)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if tc.protected {
				if _, err := coreexec.NewMemory(db, nil, nil).Remember(t.Context(), memory.Write{Agent: "agent", Person: "person", Item: memory.Item{Kind: memory.Fact, Subject: memory.Subject{Kind: memory.Person, ID: "person"}, Content: "The aurora private fact.", Scope: memory.NeverLeaves, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "original", Precision: memory.Exact}}}}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := source.Load(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, id)
			if (err == nil) == tc.protected {
				t.Fatalf("protected=%v, load error=%v", tc.protected, err)
			}
		})
	}
}
