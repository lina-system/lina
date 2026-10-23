// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func TestNameWriterPreservesCanonicalInputOrder(t *testing.T) {
	for _, nanos := range []int64{0, 100} {
		t.Run(time.Duration(nanos).String(), func(t *testing.T) {
			db := memoryDB(t)
			store, err := agents.Open(t.TempDir(), db)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			flow := NewPersona(db, store, nil)
			var scopes []PersonaScope
			for _, name := range []string{"First", "Second"} {
				a, err := store.Create(t.Context(), agents.Create{Name: name, PersonID: "person"})
				if err != nil {
					t.Fatal(err)
				}
				scopes = append(scopes, PersonaScope{Agent: a.ID, Person: "person", Conversation: "chat:" + a.ID, Revision: a.Revision})
			}
			// Admit both corrections before either finishes. IDs deliberately sort
			// opposite to input order; the zero case has identical timestamps.
			names := []string{"Cedar", "Maple", "Birch"}
			ids := []string{"z-first", "y-older", "a-later"}
			for i, name := range names {
				scope := scopes[i%2]
				at := time.Unix(10, int64(i)*nanos)
				_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: ids[i], ConversationID: scope.Conversation, AgentID: scope.Agent, PersonID: scope.Person, Surface: ledger.Surface{Kind: "tui"}, OccurredAt: at, RecordedAt: at, TimeQuality: "observed", Body: "The computer is " + name + "."})
				if err != nil {
					t.Fatal(err)
				}
			}
			write := func(i int) (RememberReceipt, error) {
				scope := scopes[i%2]
				scope.Input = ids[i]
				return flow.RememberName(t.Context(), scope, NameChange{Subject: "computer", Current: names[i], Quote: "The computer is " + names[i] + "."})
			}
			first, err := write(0)
			if err != nil {
				t.Fatal(err)
			}
			latest, err := write(2)
			if err != nil || latest.ID != first.ID || latest.Revision != 2 {
				t.Fatalf("later correction: %+v %v", latest, err)
			}
			if receipt, err := write(1); !errors.Is(err, memory.ErrEvidence) || receipt.Remembered() {
				t.Fatalf("older same-second input overwrote later correction: %+v %v", receipt, err)
			}
			if repeated, err := write(2); err != nil || repeated.ID != latest.ID || repeated.Revision != latest.Revision {
				t.Fatalf("retry changed revision: %+v %v", repeated, err)
			}
			hits, err := memory.New(db).Names(t.Context(), memory.RecallScope{Agent: scopes[1].Agent, Person: "person"}, 100)
			if err != nil || len(hits) != 1 || hits[0].Revision != latest.Revision || hits[0].Name.Current != "Birch" || !slices.Equal(hits[0].Name.RetiredAliases, []string{"Cedar"}) || len(hits[0].Evidence) != 2 {
				t.Fatalf("stale write changed stored name: %+v %v", hits, err)
			}
		})
	}
}

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
