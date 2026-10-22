// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestPersonaSettingFillUsesConfirmedCurrentInput(t *testing.T) {
	for _, entry := range []guard.Entry{guard.EntryOwner, guard.EntryTool, guard.EntryForwarded} {
		db := memoryDB(t)
		store, err := agents.Open(t.TempDir(), db)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		if err := store.UpdatePreset(t.Context(), agents.Preset{ID: "fixture", BlankSettings: []string{"likes"}}, "person"); err != nil {
			t.Fatal(err)
		}
		a, err := store.Create(t.Context(), agents.Create{Name: "Fixture", PersonID: "person", Preset: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		input := ledger.Input{ID: "confirmed", ConversationID: "conversation", AgentID: a.ID, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Locator: "fixture", Body: "Your favorite color is violet."}
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
		r, err := NewPersona(db, store, nil).FillSetting(t.Context(), scope, SettingFill{Quote: input.Body, Name: "likes", Value: "violet"})
		if entry == guard.EntryOwner {
			if err != nil || !r.Filled {
				t.Fatalf("confirmed fill failed: %+v %v", r, err)
			}
			state, err = store.State(t.Context(), a.ID)
			if err != nil || state.Settings["likes"] != "violet" || state.SettingSources["likes"] != input.ID || r.Revision != agents.StateRevision(state) {
				t.Fatalf("wrong persona record: %+v %v", state, err)
			}
		} else if err == nil || r.Filled {
			t.Fatalf("unconfirmed fill accepted: %+v %v", r, err)
		}
	}
}
