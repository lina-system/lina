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

func TestRecalledCommonNameCitesItsSourceAgent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cited   string
		allowed bool
	}{
		{"name evidence", "foreign", true},
		{"other foreign event", "foreign-other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, source, _ := fixture(t)
			at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			for _, id := range []string{"foreign", "foreign-other"} {
				if _, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: "foreign-chat", AgentID: "foreign-agent", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "Call the computer Birch " + id}); err != nil {
					t.Fatal(err)
				}
			}
			item, err := coreexec.NewMemory(db, nil, nil).Remember(t.Context(), memory.Write{Agent: "foreign-agent", Person: "person", Item: memory.Item{Kind: memory.Name, Subject: memory.Subject{Kind: memory.World, ID: "computer"}, Content: "Birch", Name: &memory.NameValue{Current: "Birch"}, Basis: memory.Stated, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "foreign", Precision: memory.Exact}}}})
			if err != nil {
				t.Fatal(err)
			}
			id := seed(t, db, "input", guard.EntryOwner)
			hit := recall.Result{Hit: recall.Hit{Kind: recall.Memory, ID: item.ID, Revision: "1"}, Excerpt: "Birch", Citations: []canon.Citation{{TargetKind: "event", TargetID: tc.cited, TargetRevision: "0"}}}
			body, _ := json.Marshal(struct {
				Results []recall.Result `json:"results"`
			}{[]recall.Result{hit}})
			call := provider.ToolCall{ID: "recall-call", Name: "recall", Arguments: `{"query":"Birch"}`}
			raw, _ := json.Marshal(call)
			result, _ := json.Marshal(loop.Result{ID: call.ID, Code: loop.OK, Output: body})
			if err := db.Write(t.Context(), func(tx *canon.Tx) error {
				_, err := tx.ExecContext(t.Context(), "INSERT INTO turn_calls(turn_id,round,ordinal,call_id,call,result) VALUES(?,0,0,?,?,?)", id, call.ID, raw, result)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			_, err = source.Load(t.Context(), replay.Scope{Person: "person", Agent: "agent"}, id)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, load error=%v", tc.allowed, err)
			}
		})
	}
}
