// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

func TestLedgerMemoryChecksRealAdmissionProvenance(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	for i, entry := range []guard.Entry{guard.EntryOwner, guard.EntryAgent, guard.EntryExternalParticipant, guard.EntryAutomation, guard.EntryForwarded, guard.EntrySystem, guard.EntryTool} {
		id := fmt.Sprintf("event-%d", i)
		in := ledger.Input{ID: id, ConversationID: "conversation", AgentID: "A", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "person: I like tea", Locator: "synthetic-source"}
		if entry == guard.EntryOwner {
			in.PersonID = "person"
		}
		if _, err := events.Admit(t.Context(), entry, in); err != nil {
			t.Fatal(err)
		}
		write := rememberInput("likes tea")
		write.Item.Evidence[0].ID = id
		receipt, err := flow.Remember(t.Context(), write)
		if entry == guard.EntryOwner {
			if err != nil || !receipt.Remembered() {
				t.Fatalf("owner refused: %v", err)
			}
		} else if !errors.Is(err, memory.ErrPersonEvidence) || receipt.Remembered() {
			t.Fatalf("entry %v impersonated person: %+v %v", entry, receipt, err)
		}
	}
	write := rememberInput("not admitted")
	write.Item.Evidence[0].ID = "missing"
	if receipt, err := flow.Remember(t.Context(), write); err == nil || receipt.Remembered() {
		t.Fatal("missing event accepted")
	}
	hits, err := flow.store.Recall(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, memory.Query{At: 100})
	if err != nil || len(hits) != 1 || hits[0].Evidence[0].Quote != "person: I like tea" {
		t.Fatalf("canonical evidence: %+v %v", hits, err)
	}
}
func TestRecallPersonReadsOwnLedgerAndExcludesSecretSpans(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	for _, agent := range []string{"A", "B"} {
		_, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: agent, ConversationID: agent, AgentID: agent, PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "I like tea, api_key=synthetic-secret-value"})
		if err != nil {
			t.Fatal(err)
		}
	}
	scope := memory.RecallScope{Agent: "A", Person: "person"}
	answer, err := flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What drink?", Query: "tea", At: 100})
	if err != nil || len(answer.Sources) != 1 || answer.Sources[0].ID != "event:A" {
		t.Fatalf("ledger scope: %+v %v", answer, err)
	}
	if strings.Contains(answer.Sources[0].Text, "synthetic-secret-value") || strings.Contains(answer.Sources[0].Text, "[secret]") {
		t.Fatal("secret span entered recall")
	}
	scope.Agent = "C"
	answer, err = flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What drink?", Query: "tea", At: 100})
	if err != nil || len(answer.Sources) != 0 {
		t.Fatalf("foreign ledger read: %+v %v", answer, err)
	}
}

func TestRecallPersonKeepsOtherSpeakersOutOfPersonEvidence(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	base := time.Unix(10, 0)
	for _, in := range []struct {
		entry guard.Entry
		input ledger.Input
	}{
		{guard.EntryOwner, ledger.Input{ID: "person-event", ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: base, RecordedAt: base, TimeQuality: "observed", Body: "I like tea", Locator: "synthetic-source"}},
		{guard.EntryAgent, ledger.Input{ID: "agent-event", ConversationID: "conversation", AgentID: "A", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: base.Add(time.Second), RecordedAt: base.Add(time.Second), TimeQuality: "observed", Body: "You said you like tea", Locator: "synthetic-source"}},
	} {
		if _, err := events.Admit(t.Context(), in.entry, in.input); err != nil {
			t.Fatal(err)
		}
	}
	scope := memory.RecallScope{Agent: "A", Person: "person"}
	answer, err := flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What drink?", Query: "tea", At: 100})
	if err != nil || len(answer.Sources) != 1 || answer.Sources[0].ID != "event:person-event" {
		t.Fatalf("person evidence: %+v %v", answer, err)
	}
	// A memory item stays eligible; only event candidates take the restriction.
	write := rememberInput("likes tea")
	write.Item.Evidence[0].ID = "person-event"
	if _, err := flow.Remember(t.Context(), write); err != nil {
		t.Fatal(err)
	}
	answer, err = flow.RecallPerson(t.Context(), scope, PersonRecall{Question: "What drink?", Query: "tea", At: 100})
	if err != nil || len(answer.Sources) != 1 || !strings.HasPrefix(answer.Sources[0].ID, "memory:") {
		t.Fatalf("memory evidence: %+v %v", answer, err)
	}
}

type sourceMaterials struct {
	bodies map[ledger.Material]string
	reads  int
}

func (s *sourceMaterials) Prepare(_ context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	ref := ledger.Material{ID: fmt.Sprintf("source-%d", len(s.bodies)), Revision: 1}
	return sourceMaterialWrite{store: s, ref: ref, body: body.Text()}, nil
}

type sourceMaterialWrite struct {
	store *sourceMaterials
	ref   ledger.Material
	body  string
}

func (w sourceMaterialWrite) Insert(context.Context, *canon.Tx) (ledger.Material, error) {
	w.store.bodies[w.ref] = w.body
	return w.ref, nil
}
func (w sourceMaterialWrite) Close() error { return nil }
func (s *sourceMaterials) Read(_ context.Context, ref ledger.Material) (string, error) {
	s.reads++
	body, ok := s.bodies[ref]
	if !ok {
		return "", ledger.ErrBytesUnavailable
	}
	return body, nil
}
func TestRememberLargeSourceAndReplayKeepsOneLineage(t *testing.T) {
	db := memoryDB(t)
	materials := &sourceMaterials{bodies: map[ledger.Material]string{}}
	events := ledger.New(db, materials)
	flow := NewMemory(db, materials, nil)
	input := ledger.Input{ID: "original", ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: strings.Repeat("likes tea ", 2000)}
	if _, err := events.Admit(t.Context(), guard.EntryOwner, input); err != nil {
		t.Fatal(err)
	}
	write := rememberInput("likes tea")
	write.Item.Evidence[0].ID = input.ID
	first, err := flow.Remember(t.Context(), write)
	if err != nil {
		t.Fatal(err)
	}
	if materials.reads != 1 {
		t.Fatalf("material was not prefetched once: %d", materials.reads)
	}
	input.ID = "replay"
	input.ReplayOf = "original"
	input.Body = "likes tea"
	if _, err := events.Admit(t.Context(), guard.EntryOwner, input); err != nil {
		t.Fatal(err)
	}
	write.Item.Evidence[0].ID = input.ID
	second, err := flow.Remember(t.Context(), write)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Revision != first.Revision {
		t.Fatalf("replay became independent evidence: %+v %+v", first, second)
	}
	hits, err := flow.store.Recall(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, memory.Query{At: 100})
	if err != nil || len(hits) != 1 || len(hits[0].Evidence) != 1 || hits[0].Evidence[0].ID != "original" {
		t.Fatalf("lineage: %+v %v", hits, err)
	}
}

func TestRecallPersonRanksOnlyItemsAboutThePerson(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	_, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "event", ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "my friends drink tea"})
	if err != nil {
		t.Fatal(err)
	}
	person, err := flow.Remember(t.Context(), rememberInput("likes tea"))
	if err != nil {
		t.Fatal(err)
	}
	others := map[string]bool{}
	for i := range RecallItemLimit {
		write := rememberInput(fmt.Sprintf("friend %d likes tea tea tea", i))
		write.Item.Subject = memory.Subject{Kind: memory.ThirdParty, ID: fmt.Sprintf("friend-%d", i)}
		receipt, err := flow.Remember(t.Context(), write)
		if err != nil {
			t.Fatal(err)
		}
		others["memory:"+receipt.ID] = true
	}
	world := rememberInput("the tea shop sells tea")
	world.Item.Subject = memory.Subject{Kind: memory.World, ID: "tea-shop"}
	receipt, err := flow.Remember(t.Context(), world)
	if err != nil {
		t.Fatal(err)
	}
	others["memory:"+receipt.ID] = true
	answer, err := flow.RecallPerson(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, PersonRecall{Question: "What drink?", Query: "tea", At: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range answer.Sources {
		if others[source.ID] {
			t.Fatalf("item about someone else used as person evidence: %+v", answer.Sources)
		}
	}
	if len(answer.Sources) != 1 || answer.Sources[0].ID != "memory:"+person.ID {
		t.Fatalf("person item: %+v", answer.Sources)
	}
}

func TestRecallPersonCountsRememberedEventOnce(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	_, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "event", ConversationID: "conversation", AgentID: "A", PersonID: "person", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: "likes tea"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Remember(t.Context(), rememberInput("likes tea")); err != nil {
		t.Fatal(err)
	}
	answer, err := flow.RecallPerson(t.Context(), memory.RecallScope{Agent: "A", Person: "person"}, PersonRecall{Question: "What drink?", Query: "tea", At: 100})
	if err != nil || len(answer.Sources) != 1 {
		t.Fatalf("source counted through memory and ledger: %+v %v", answer, err)
	}
}

func TestRecallReadsMaterialBackedEvent(t *testing.T) {
	db := memoryDB(t)
	materials := &sourceMaterials{bodies: map[ledger.Material]string{}}
	events := ledger.New(db, materials)
	body := "observatory " + strings.Repeat("telescope ", 2000)
	event, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "large", ConversationID: "conversation", PersonID: "person", AgentID: "A", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if event.Material.ID == "" {
		t.Fatal("fixture not material-backed")
	}
	read := func(ctx context.Context, id string) (string, error) {
		e, err := events.Get(ctx, id)
		return e.Body.DerivedText(), err
	}
	for _, available := range []bool{true, false} {
		var reader recall.ReadEvent
		if available {
			reader = read
		}
		hits, err := recall.Search(t.Context(), db, reader, recall.Scope{Person: "person", Agent: "A"}, recall.Query{Text: "observatory"})
		if err != nil || len(hits) != 1 {
			t.Fatalf("available=%v hits=%d err=%v", available, len(hits), err)
		}
		if hits[0].Unavailable == available || available && !strings.Contains(hits[0].Excerpt, "observatory") {
			t.Fatalf("material result: %+v", hits[0])
		}
	}
}

func TestRecallPersonFiltersBeforeLimit(t *testing.T) {
	db := memoryDB(t)
	events := ledger.New(db, nil)
	flow := NewMemory(db, nil, nil)
	admit := func(id string, entry guard.Entry, body string) {
		in := ledger.Input{ID: id, Locator: id, ConversationID: "conversation", AgentID: "A", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: body}
		if entry == guard.EntryOwner {
			in.PersonID = "person"
		}
		if _, err := events.Admit(t.Context(), entry, in); err != nil {
			t.Fatal(err)
		}
	}
	admit("person-source", guard.EntryOwner, "tea "+strings.Repeat("context ", 100))
	admit("memory-source", guard.EntryOwner, "tea preference "+strings.Repeat("context ", 100))
	write := rememberInput("tea preference")
	write.Item.Evidence[0].ID = "memory-source"
	good, err := flow.Remember(t.Context(), write)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < RecallItemLimit+2; i++ {
		id := fmt.Sprintf("other-%d", i)
		entry := []guard.Entry{guard.EntryAgent, guard.EntryTool, guard.EntryForwarded, guard.EntryAutomation}[i%4]
		admit(id, entry, fmt.Sprintf("tea other%d", i))
		write := rememberInput(fmt.Sprintf("tea world%d", i))
		write.Item.Subject = memory.Subject{Kind: memory.World, ID: id}
		write.Item.Evidence[0].ID = "person-source"
		if _, err := flow.Remember(t.Context(), write); err != nil {
			t.Fatal(err)
		}
	}
	answer, err := flow.RecallPerson(t.Context(), memory.RecallScope{Person: "person", Agent: "A"}, PersonRecall{Question: "What tea?", Query: "tea", At: 100})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, source := range answer.Sources {
		ids[source.ID] = true
	}
	if len(ids) != 2 || !ids["event:person-source"] || !ids["memory:"+good.ID] {
		t.Fatalf("person evidence displaced by other sources: %v", ids)
	}
}

func TestRecallPersonReadsQueuedMaterialBody(t *testing.T) {
	db := memoryDB(t)
	materials := &sourceMaterials{bodies: map[ledger.Material]string{}}
	events := ledger.New(db, materials)
	flow := NewMemory(db, materials, nil)
	body := "observatory " + strings.Repeat("telescope ", 2000)
	event, err := events.Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "large", ConversationID: "conversation", PersonID: "person", AgentID: "A", Surface: ledger.Surface{Kind: "tui"}, OccurredAt: time.Unix(10, 0), RecordedAt: time.Unix(10, 0), TimeQuality: "observed", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if event.Material.ID == "" {
		t.Fatal("fixture not material-backed")
	}
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(t.Context(), "DELETE FROM recall_doc WHERE kind='event' AND source_id=?", event.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(t.Context(), "INSERT OR IGNORE INTO recall_pending(kind, source_id) VALUES('event',?)", event.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	answer, err := flow.RecallPerson(t.Context(), memory.RecallScope{Person: "person", Agent: "A"}, PersonRecall{Question: "Where?", Query: "observatory", At: 100})
	if err != nil || len(answer.Sources) != 1 || answer.Sources[0].ID != "event:large" || !strings.Contains(answer.Sources[0].Text, "observatory") {
		t.Fatalf("queued material person recall: %+v %v", answer, err)
	}
}
