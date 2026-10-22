// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/retract"
)

func TestRelationshipWriterConfirmsEvidenceAndKeepsEmotionSeparate(t *testing.T) {
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
	before, err := store.State(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	scope := PersonaScope{Agent: a.ID, Person: "person", Conversation: "chat", Revision: a.Revision}
	var first, latest string
	for i, text := range []string{"We are study partners.", "We completed our first project together."} {
		id := []string{"first", "second"}[i]
		_, err = ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: "chat", AgentID: a.ID, PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(int64(10+i), 0), RecordedAt: time.Unix(int64(10+i), 0), TimeQuality: "observed", Body: text})
		if err != nil {
			t.Fatal(err)
		}
		scope.Input = id
		r, err := flow.RecordRelationship(t.Context(), scope, RelationshipChange{Quote: text})
		if err != nil || !r.Remembered() {
			t.Fatalf("relationship write: %+v %v", r, err)
		}
		latest = r.ID
		if i == 0 {
			first = r.ID
		}
		hits, err := memory.New(db).CurrentRelationship(t.Context(), memory.RecallScope{Agent: a.ID, Person: "person"}, 100)
		if err != nil || len(hits) != 1 || hits[0].Content != text || hits[0].Holder != memory.Holder(a.ID) || hits[0].Evidence[0].ID != id {
			t.Fatalf("relationship projection: %+v %v", hits, err)
		}
		if i == 1 && hits[0].Supersedes != first {
			t.Fatal("relationship history not linked")
		}
		repeat, err := flow.RecordRelationship(t.Context(), scope, RelationshipChange{Quote: text})
		if err != nil || repeat.ID != r.ID {
			t.Fatalf("duplicate relationship: %+v %v", repeat, err)
		}
	}
	if r, err := flow.RecordRelationship(t.Context(), scope, RelationshipChange{Quote: "We are best friends."}); err == nil || r.Remembered() {
		t.Fatal("invented relationship accepted")
	}
	foreign := scope
	foreign.Person = "other"
	if r, err := flow.RecordRelationship(t.Context(), foreign, RelationshipChange{Quote: "We completed our first project together."}); err == nil || r.Remembered() {
		t.Fatal("foreign relationship accepted")
	}
	after, err := store.State(t.Context(), a.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("relationship changed persona emotion")
	}
	removal, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer removal.Close()
	if err := removal.Forget(t.Context(), retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: latest}}}); err != nil {
		t.Fatal(err)
	}
	hits, err := memory.New(db).CurrentRelationship(t.Context(), memory.RecallScope{Agent: a.ID, Person: "person"}, 100)
	if err != nil || len(hits) != 0 {
		t.Fatalf("retracted relationship or superseded view returned: %+v %v", hits, err)
	}
	history, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: a.ID, Person: "person"}, memory.Query{At: 100, History: true})
	if err != nil || len(history) != 0 {
		t.Fatalf("forgotten relationship history returned: %+v %v", history, err)
	}
	if r, err := flow.RecordRelationship(t.Context(), scope, RelationshipChange{Quote: "We completed our first project together."}); err == nil || r.Remembered() {
		t.Fatal("retracted evidence reused")
	}
}

func TestRelationshipDeletionNeverRevivesPredecessor(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "permanent", true: "trash-expiry"}[expire], func(t *testing.T) {
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
			var previous, current string
			for i, quote := range []string{"We are study partners.", "We are colleagues."} {
				id := []string{"before", "replacement"}[i]
				_, err := ledger.New(db, nil).Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: "chat", AgentID: a.ID, PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(int64(10+i), 0), RecordedAt: time.Unix(int64(10+i), 0), TimeQuality: "observed", Body: quote})
				if err != nil {
					t.Fatal(err)
				}
				result, err := flow.RecordRelationship(t.Context(), PersonaScope{Agent: a.ID, Person: "person", Conversation: "chat", Input: id, Revision: a.Revision}, RelationshipChange{Quote: quote})
				if err != nil {
					t.Fatal(err)
				}
				previous, current = current, result.ID
			}
			now := time.Unix(100, 0)
			removal, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: root, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			defer removal.Close()
			request := retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: current}}}
			if expire {
				if err := removal.Forget(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				now = now.Add(retract.TrashRetention)
				if err := removal.Expire(t.Context(), "person"); err != nil {
					t.Fatal(err)
				}
			} else if err := removal.Delete(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			scope := memory.RecallScope{Agent: a.ID, Person: "person", ExternalModel: true}
			hits, err := memory.New(db).CurrentRelationship(t.Context(), scope, now.Unix())
			if err != nil || len(hits) != 0 {
				t.Fatalf("erased replacement resurrected predecessor: %+v %v", hits, err)
			}
			history, err := memory.New(db).Recall(t.Context(), scope, memory.Query{At: now.Unix(), History: true})
			if err != nil || len(history) != 0 {
				t.Fatalf("historical predecessor: %+v %v", history, err)
			}
			view, err := removal.Snapshot(t.Context(), "person", a.ID)
			if err != nil || view.Visible(retract.Ref{Kind: retract.Memory, ID: previous}) {
				t.Fatalf("predecessor was not retracted: %v", err)
			}
		})
	}
}
