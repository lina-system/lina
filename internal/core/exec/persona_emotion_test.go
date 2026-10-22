// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func TestEmotionWriterRequiresCanonicalPersonEvidence(t *testing.T) {
	for _, entry := range []guard.Entry{guard.EntryOwner, guard.EntryAgent, guard.EntryTool, guard.EntryForwarded, guard.EntryAutomation} {
		db := memoryDB(t)
		store, err := agents.Open(t.TempDir(), db)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		a, err := store.Create(t.Context(), agents.Create{Name: "Fixture", PersonID: "person"})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		in := ledger.Input{ID: "event", ConversationID: "chat", AgentID: a.ID, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Locator: "fixture", Body: "We finished the test run."}
		if entry == guard.EntryOwner {
			in.PersonID = "person"
		}
		if _, err := ledger.New(db, nil).Admit(t.Context(), entry, in); err != nil {
			t.Fatal(err)
		}
		state, err := store.State(t.Context(), a.ID)
		if err != nil {
			t.Fatal(err)
		}
		flow := NewPersona(db, store, nil)
		flow.now = func() time.Time { return now }
		scope := PersonaScope{Agent: a.ID, Person: "person", Conversation: "chat", Input: "event", Revision: a.Revision, StateRevision: agents.StateRevision(state)}
		receipt, err := flow.RecordEmotion(t.Context(), scope, EmotionChange{Quote: in.Body, Valence: 0.8, Arousal: 0.4})
		if entry == guard.EntryOwner {
			if err != nil || !receipt.Recorded {
				t.Fatalf("confirmed event refused: %+v %v", receipt, err)
			}
			state, err = store.State(t.Context(), a.ID)
			if err != nil || state.Affect == nil || state.Affect.Short["chat"].EventID != "event" {
				t.Fatalf("no canonical event reference: %+v %v", state, err)
			}
		} else if err == nil || receipt.Recorded {
			t.Fatalf("unconfirmed emotion accepted: %+v %v", receipt, err)
		}
	}
}

func TestSensitivePersonaInputSuppressesGriefHealthAndTaggedSpans(t *testing.T) {
	for _, text := range []string{"My mother died.", "I need help with a medical diagnosis.", "지금 응급 상황이야", "친구의 장례식에 다녀왔어"} {
		event := ledger.Event{}
		masked, err := secrets.Mask(text)
		if err != nil {
			t.Fatal(err)
		}
		event.Body = masked
		if !SensitivePersonaInput(event) {
			t.Fatalf("sensitive input not suppressed: %q", text)
		}
	}
	if SensitivePersonaInput(ledger.Event{}) {
		t.Fatal("neutral empty input suppressed")
	}
	event := ledger.Event{Spans: []secrets.Span{{Sensitivity: "secret"}}}
	if !SensitivePersonaInput(event) {
		t.Fatal("sensitive admission tag ignored")
	}
}
