// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/kit/lease"
)

func TestMemoryToolCannotOverrideBoundScope(t *testing.T) {
	db := memoryDB(t)
	flow := newMemory(db, source, nil, nil)
	binding := memory.Write{Agent: "A", Person: "person", ExplicitHolder: true, Item: memory.Item{Holder: "A", Scope: memory.NeverLeaves}}
	spec, handler := flow.RememberTool(binding)
	proposed := rememberInput("likes tea").Item
	for _, change := range []func(*memory.Item){func(i *memory.Item) { i.Holder = memory.All }, func(i *memory.Item) { i.Scope = memory.Local }} {
		item := proposed
		change(&item)
		raw, _ := json.Marshal(item)
		if _, err := spec.Schema(raw); err == nil {
			t.Fatal("model could override bound holder/scope")
		}
	}
	raw, _ := json.Marshal(proposed)
	input, err := spec.Schema(raw)
	if err != nil {
		t.Fatal(err)
	}
	l := lease.New(func() {})
	defer l.Release()
	if _, err := handler(t.Context(), l, input); err != nil {
		t.Fatal(err)
	}
	hits, err := memory.New(db).Recall(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, memory.Query{At: 100})
	if err != nil || len(hits) != 1 || hits[0].Holder != "A" || hits[0].Scope != memory.NeverLeaves {
		t.Fatalf("bound policy discarded: %+v %v", hits, err)
	}
}
func TestRecallPersonRequiresQueryBeforeLimitingSources(t *testing.T) {
	flow := newMemory(memoryDB(t), source, nil, nil)
	spec, _ := flow.RecallPersonTool(memory.RecallScope{Agent: "A", Person: "person"}, func() int64 { return 100 })
	if _, err := spec.Schema(json.RawMessage(`{"question":"What drink do I like?"}`)); err == nil {
		t.Fatal("question alone silently selects latest items")
	}
	if _, err := flow.RecallPerson(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, PersonRecall{Question: "What drink do I like?", At: 100}); err == nil {
		t.Fatal("empty-query API silently selects latest items")
	}
}
func TestAuxiliaryCannotReadNeverLeavesThroughMemoryOrRawEvidence(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	calls := 0
	flow := NewMemory(db, nil, func(_ context.Context, r RecallRequest) ([]Sentence, error) {
		calls++
		for _, source := range r.Sources {
			if strings.Contains(source.Text, "protected") {
				t.Fatalf("restricted source reached auxiliary: %+v", r)
			}
		}
		return nil, nil
	})
	for _, id := range []string{"protected", "allowed"} {
		_, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: id, ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "likes " + id})
		if err != nil {
			t.Fatal(err)
		}
	}
	in := rememberInput("likes protected")
	in.Item.Scope = memory.NeverLeaves
	in.Item.Evidence[0].ID = "protected"
	if _, err := flow.Remember(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	// A second local item over the same event must not bypass its evidence scope.
	in.Item.Scope = memory.Local
	in.Item.Content = "likes protected too"
	if _, err := flow.Remember(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	scope := memory.RecallScope{Agent: "A", Person: "person"}
	local, err := flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What do I like?", Query: "likes", At: 100})
	if err != nil || len(local.Sources) != 3 {
		t.Fatalf("local recall lost protected data: %+v %v", local, err)
	}
	remote, err := flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What do I like?", Query: "likes", At: 100, Depth: 1})
	if err != nil || calls != 1 || len(remote.Sources) != 1 || remote.Sources[0].ID != "event:allowed" {
		t.Fatalf("remote scope: %+v %v", remote, err)
	}
}

func TestRecallQueryFindsOlderMatchBeforeCap(t *testing.T) {
	flow := newMemory(memoryDB(t), source, nil, nil)
	old, err := flow.Remember(t.Context(), rememberInput("likes tea"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := flow.Remember(t.Context(), rememberInput(fmt.Sprintf("unrelated fact %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	result, err := flow.RecallPerson(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, PersonRecall{Question: "What drink do I like?", Query: "tea", At: 100})
	if err != nil || len(result.Sources) != 1 || result.Sources[0].ID != "memory:"+old.ID {
		t.Fatalf("older match excluded: %+v %v", result, err)
	}
}
func TestSameOwnerLineageCrossesConversationsButNotAgents(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	in := ledger.Input{ID: "original", ConversationID: "first", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "likes tea"}
	if _, err := events.Admit(t.Context(), guard.EntryOwner, in); err != nil {
		t.Fatal(err)
	}
	in.ID = "copy"
	in.ConversationID = "second"
	in.ReplayOf = "original"
	if _, err := events.Admit(t.Context(), guard.EntryOwner, in); err != nil {
		t.Fatal(err)
	}
	write := rememberInput("likes tea")
	write.Item.Evidence[0].ID = "copy"
	if receipt, err := flow.Remember(t.Context(), write); err != nil || !receipt.Remembered() {
		t.Fatalf("same owner replay across sessions refused: %+v %v", receipt, err)
	}
	in.ID = "foreign"
	in.AgentID = "B"
	if _, err := events.Admit(t.Context(), guard.EntryOwner, in); err != nil {
		t.Fatal(err)
	}
	write.Agent = "B"
	write.Item.Evidence[0].ID = "foreign"
	if receipt, err := flow.Remember(t.Context(), write); !errors.Is(err, memory.ErrEvidence) || receipt.Remembered() {
		t.Fatalf("cross-agent lineage accepted: %+v %v", receipt, err)
	}
}
