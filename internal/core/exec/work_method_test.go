// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestWorkMethodWriterRequiresConfirmedPersonEvidence(t *testing.T) {
	for _, mode := range []string{"owner", "tool", "forwarded", "wrong-quote", "wrong-conversation", "undeclared", "stale-state", "stale-persona"} {
		t.Run(mode, func(t *testing.T) {
			db := memoryDB(t)
			store, err := agents.Open(t.TempDir(), db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			preset := agents.Preset{ID: "creative", WorkMethod: `{"schema":1,"preset":"creative","reply":{"candidates":3},"fill":["reply.candidates"]}`}
			if err := store.UpdatePreset(t.Context(), preset, "person"); err != nil {
				t.Fatal(err)
			}
			a, err := store.Create(t.Context(), agents.Create{Name: "Fixture", PersonID: "person", Preset: "creative"})
			if err != nil {
				t.Fatal(err)
			}
			entry := guard.EntryOwner
			if mode == "tool" {
				entry = guard.EntryTool
			}
			if mode == "forwarded" {
				entry = guard.EntryForwarded
			}
			input := ledger.Input{ID: "confirmed", ConversationID: "conversation", AgentID: a.ID, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Locator: "fixture", Body: "Offer two candidates."}
			if entry == guard.EntryOwner {
				input.PersonID = "person"
			}
			if _, err := ledger.New(db, nil).Admit(t.Context(), entry, input); err != nil {
				t.Fatal(err)
			}
			state, err := store.State(t.Context(), a.ID)
			if err != nil {
				t.Fatal(err)
			}
			scope := PersonaScope{Agent: a.ID, Person: "person", Conversation: "conversation", Input: input.ID, Revision: 1, StateRevision: agents.StateRevision(state)}
			fill := WorkMethodFill{Quote: input.Body, Key: "reply.candidates", Value: json.RawMessage(`2`)}
			switch mode {
			case "wrong-quote":
				fill.Quote = "Invented preference."
			case "wrong-conversation":
				scope.Conversation = "other"
			case "undeclared":
				fill.Key = "ask.per_turn"
			case "stale-state":
				scope.StateRevision = "obsolete"
			case "stale-persona":
				preset.Core = "changed"
				if err := store.UpdatePreset(t.Context(), preset, "person"); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := NewPersona(db, store, nil).FillWorkMethod(t.Context(), scope, fill)
			after, stateErr := store.State(t.Context(), a.ID)
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			if mode != "owner" {
				if err == nil || receipt.Filled || len(after.WorkMethodFills) != 0 {
					t.Fatalf("unconfirmed/stale fill accepted: %+v %v", receipt, err)
				}
				return
			}
			if err != nil || !receipt.Filled || receipt.Revision != agents.StateRevision(after) || after.WorkMethodFills[fill.Key].Source != input.ID {
				t.Fatalf("confirmed fill failed: %+v %+v %v", receipt, after, err)
			}
			pin, err := store.Prepare(t.Context(), "next", a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := agents.ProjectWorkMethod(pin, after); string(got.Values[fill.Key]) != "2" {
				t.Fatal("filled value not effective", got)
			}
		})
	}
}
