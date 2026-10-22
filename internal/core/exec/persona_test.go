// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"fmt"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestPersonaChangeRequiresThisPersonsConfirmedRequest(t *testing.T) {
	for _, entry := range []guard.Entry{guard.EntryOwner, guard.EntryAgent, guard.EntryTool, guard.EntryExternalParticipant, guard.EntryForwarded, guard.EntryAutomation} {
		t.Run(fmt.Sprint(entry), func(t *testing.T) {
			db := memoryDB(t)
			store, err := agents.Open(t.TempDir(), db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			a, err := store.Create(t.Context(), agents.Create{Name: "Fixture", PersonID: "person", Layers: agents.Layers{Core: "fixture core"}})
			if err != nil {
				t.Fatal(err)
			}
			in := ledger.Input{ID: "source", ConversationID: "conversation", AgentID: a.ID, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Locator: "fixture", Body: "talk more casually"}
			if entry == guard.EntryOwner {
				in.PersonID = "person"
			}
			if _, err := ledger.New(db, nil).Admit(t.Context(), entry, in); err != nil {
				t.Fatal(err)
			}
			flow := NewPersona(db, store, nil)
			scope := PersonaScope{Agent: a.ID, Person: "person", Conversation: "conversation", Input: "source", Revision: 1}
			receipt, err := flow.Change(t.Context(), scope, VoiceChange{Quote: "talk more casually", UserVoice: "fixture casual voice"})
			if entry == guard.EntryOwner {
				if err != nil || receipt.Revision != 2 || receipt.Notice == "" || receipt.Undo == nil || receipt.Undo.Revision != 2 {
					t.Fatalf("owner request: %+v %v", receipt, err)
				}
			} else if err == nil || receipt.Notice != "" {
				t.Fatalf("unconfirmed source adopted: %+v %v", receipt, err)
			}
		})
	}
}

func TestPersonaChangeRejectsForeignScopeFabricatedQuoteAndSecret(t *testing.T) {
	db := memoryDB(t)
	store, err := agents.Open(t.TempDir(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, err := store.Create(t.Context(), agents.Create{Name: "Fixture", PersonID: "person"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "source", ConversationID: "conversation", AgentID: a.ID, PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "talk more casually"})
	if err != nil {
		t.Fatal(err)
	}
	flow := NewPersona(db, store, nil)
	good := PersonaScope{Agent: a.ID, Person: "person", Conversation: "conversation", Input: "source", Revision: 1}
	for _, kind := range []string{"agent", "person", "conversation", "missing", "quote", "secret"} {
		scope := good
		change := VoiceChange{Quote: "talk more casually", UserVoice: "fixture casual voice"}
		switch kind {
		case "agent":
			scope.Agent = "foreign"
		case "person":
			scope.Person = "foreign"
		case "conversation":
			scope.Conversation = "foreign"
		case "missing":
			scope.Input = "missing"
		case "quote":
			change.Quote = "not in the input"
		case "secret":
			change.UserVoice = "password=synthetic-secret-value"
		}
		if receipt, err := flow.Change(t.Context(), scope, change); err == nil || receipt.Notice != "" {
			t.Fatalf("%s was accepted: %+v %v", kind, receipt, err)
		}
	}
	current, err := store.Get(t.Context(), a.ID)
	if err != nil || current.Revision != 1 {
		t.Fatalf("rejected request mutated persona: %+v %v", current, err)
	}
}
