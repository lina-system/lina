// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func TestNameWriterUsesConfirmedPersonAndPreservesCorrection(t *testing.T) {
	db := memoryDB(t)
	root := t.TempDir()
	store, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, err := store.Create(t.Context(), agents.Create{Name: "Fixture", PersonID: "person"})
	if err != nil {
		t.Fatal(err)
	}
	flow := NewPersona(db, store, nil)
	scope := PersonaScope{Agent: a.ID, Person: "person", Conversation: "chat", Revision: a.Revision}
	var first string
	for i, current := range []string{"Cedar", "Birch"} {
		quote := "The computer is " + current + "."
		if i == 1 {
			quote = "Use Birch instead of Cedar."
		}
		eventID := "event-" + current
		_, err = ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: eventID, ConversationID: "chat", AgentID: a.ID, PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(int64(10+i), 0), RecordedAt: time.Unix(int64(10+i), 0), TimeQuality: "observed", Body: quote})
		if err != nil {
			t.Fatal(err)
		}
		scope.Input = eventID
		receipt, err := flow.RememberName(t.Context(), scope, NameChange{Subject: "computer", Current: current, Quote: quote})
		if err != nil || !receipt.Remembered() {
			t.Fatalf("name write: %+v %v", receipt, err)
		}
		if i == 0 {
			first = receipt.ID
		} else if receipt.ID != first || receipt.Revision != 2 {
			t.Fatalf("not a revision: %+v", receipt)
		}
	}
	quote := "Use Birch instead of Cedar."
	for _, in := range []NameChange{{Subject: "computer", Current: "Invented", Quote: quote}, {Subject: "computer", Current: "Birch", Quote: quote, RetiredAliases: []string{"Invented"}}, {Subject: "other device", Current: "Birch", Quote: quote}, {Subject: "computer", Current: "Birch", Quote: "Invented evidence"}} {
		if receipt, err := flow.RememberName(t.Context(), scope, in); err == nil || receipt.Remembered() {
			t.Fatal("invented name accepted")
		}
	}
	foreign := scope
	foreign.Person = "other"
	if receipt, err := flow.RememberName(t.Context(), foreign, NameChange{Subject: "computer", Current: "Birch", Quote: quote}); err == nil || receipt.Remembered() {
		t.Fatal("foreign person accepted")
	}
	scope.Input = "event-Cedar"
	if receipt, err := flow.RememberName(t.Context(), scope, NameChange{Subject: "computer", Current: "Cedar", Quote: "The computer is Cedar."}); err == nil || receipt.Remembered() {
		t.Fatal("older input undid newer correction")
	}
	removal, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer removal.Close()
	if err := removal.Forget(t.Context(), retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: first}}}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{a.ID, "another"} {
		hits, err := memory.New(db).Names(t.Context(), memory.RecallScope{Agent: agent, Person: "person"}, 100)
		if err != nil || len(hits) != 0 {
			t.Fatalf("forgotten name exposed: %+v %v", hits, err)
		}
	}
	scope.Input = "event-Birch"
	if receipt, err := flow.RememberName(context.Background(), scope, NameChange{Subject: "computer", Current: "Birch", Quote: quote}); err == nil || receipt.Remembered() {
		t.Fatal("forgotten name resurrected from excluded evidence")
	}
}
