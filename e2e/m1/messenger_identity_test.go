// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/identity"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/retract"
)

// TestM1SenderIdentityInInstalledCore proves sender identity inside a running
// Core: the sync loop admits a participant's record and the matcher links the
// identity to the people record the person keeps, flags a family phone as
// ambiguous once, and a repair retracts the item the link produced while only
// that identity's messages are queued again.
func TestM1SenderIdentityInInstalledCore(t *testing.T) {
	t.Parallel()

	h := newHarness(t, "")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bundle := makeInstallBundle(t, t.TempDir(), executable, executable)
	provenance, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"canon", "system/bin", "system/core"} {
		if err := os.MkdirAll(filepath.Join(h.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/bin/manifest.json"), provenance, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, "system/core/main.json"), []byte(`{"main_id":"messenger-host"}`), 0600); err != nil {
		t.Fatal(err)
	}
	person := "person:messenger-host"
	start := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	source := messenger.Source{Person: person, Platform: "fixture", Account: "account-1", Workspace: "T-1",
		Mode: messenger.ModeOfficial, Adapter: "fixture", State: messenger.StateConnected, ConnectedAt: start}

	db, closeStore := installedAcceptanceStore(t, h)
	store := messenger.Open(db, nil, nil)
	if err := store.Connect(h.ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChats(h.ctx, source, []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	repository, err := records.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	actor := records.Actor{Person: person, Agent: "agent"}
	for _, record := range []struct{ id, path, title, phone string }{
		{"person-anna", "areas/people/anna.md", "Anna Kim", "+821012340001"},
		{"person-family-a", "areas/people/family-a.md", "Family, parent", "+821055550001"},
		{"person-family-b", "areas/people/family-b.md", "Family, child", "+821055550001"},
	} {
		data := []byte("---\nid: \"" + record.id + "\"\nkind: person\nstate: active\nperson_id: \"" + person +
			"\"\nnames: [\"" + record.title + "\"]\n---\n\n# " + record.title +
			"\n\n## Contacts\n\n- phone " + record.phone + " — iMessage profile\n")
		if _, err := repository.Commit(h.ctx, records.Change{ID: record.id, Path: record.path, Data: data}, actor); err != nil {
			t.Fatal(err)
		}
	}
	repository.Close()
	closeStore()

	adapter := &scriptedAdapter{pages: map[string][]scriptedPage{
		"": {{page: messenger.Page{
			Messages: []messenger.Message{
				senderMessage("m-1", messenger.RouteParticipant, start, "hello", "U-anna", "Anna", "+821012340001"),
				senderMessage("m-2", messenger.RouteParticipant, start.Add(time.Minute), "family", "+821055550001", "Family", "+821055550001"),
				senderMessage("m-3", messenger.RouteParticipant, start.Add(2*time.Minute), "later", "U-later", "Later", "+821011110009"),
			},
			Next: "c1", HasMore: true,
		}}},
		"c1": {{page: messenger.Page{Next: "c1"}}},
	}}
	stop := runMessengerCore(t, h, adapter)
	adapter.waitFor(t, "c1")
	stop()

	db, closeStore = installedAcceptanceStore(t, h)
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.Read(h.ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(h.ctx, query, args...).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM identity_sender WHERE platform='fixture' AND scope='T-1'`); n != 3 {
		t.Fatalf("identities = %d, want 3", n)
	}
	if n := count(`SELECT count(*) FROM identity_link WHERE record='person-anna' AND class='exact' AND state='active'`); n != 1 {
		t.Fatal("the participant's phone did not link to the people record")
	}
	if n := count(`SELECT count(*) FROM identity_link WHERE user='+821055550001'`); n != 0 {
		t.Fatal("a family phone was linked anyway")
	}
	if n := count(`SELECT count(*) FROM identity_question WHERE user='+821055550001' AND state='open'`); n != 1 {
		t.Fatal("the family phone was not asked about exactly once")
	}
	if n := count(`SELECT count(*) FROM identity_link WHERE user='U-later'`); n != 0 {
		t.Fatal("an identity with no people record got a link")
	}

	// The person adds the people record after the messages arrived. Core's next
	// pass links the known identity without a new message.
	repository = repositoryAt(t, h, db)
	late := []byte("---\nid: \"person-later\"\nkind: person\nstate: active\nperson_id: \"" + person +
		"\"\nnames: [\"Later Person\"]\n---\n\n# Later Person\n\n## Contacts\n\n- phone +821011110009 — iMessage profile\n")
	if _, err := repository.Commit(h.ctx, records.Change{ID: "person-later", Path: "areas/people/later.md", Data: late}, actor); err != nil {
		t.Fatal(err)
	}
	repository.Close()
	closeStore()
	empty := &scriptedAdapter{pages: map[string][]scriptedPage{"c1": {{page: messenger.Page{Next: "c1"}}}}}
	stop = runMessengerCore(t, h, empty)
	empty.waitFor(t, "c1")
	stop()
	db, closeStore = installedAcceptanceStore(t, h)
	defer closeStore()
	if n := count(`SELECT count(*) FROM identity_link WHERE user='U-later' AND record='person-later' AND state='active'`); n != 1 {
		t.Fatal("a people record created after the messages did not link the identity")
	}

	// Repair inside Core's store: one item the link produced is retracted, and
	// only that identity's messages are queued again.
	removals, err := retract.Open(h.ctx, retract.Config{Canon: db, Root: h.root})
	if err != nil {
		t.Fatal(err)
	}
	defer removals.Close()
	sender := identity.Open(db, person, e2ePeople{store: repositoryAt(t, h, db)}, removals)
	links, err := sender.Links(h.ctx, identity.Identity{Platform: "fixture", Scope: "T-1", User: "U-anna"})
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || !links[0].Usable(start.Add(time.Hour)) {
		t.Fatalf("links = %+v", links)
	}
	event := "identity-e2e-item"
	if _, err := ledger.New(db, nil).Admit(h.ctx, guard.EntryOwner, ledger.Input{
		ID: event, ConversationID: "native:agent", AgentID: "agent", PersonID: person,
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: start, RecordedAt: start,
		TimeQuality: "recorded", Body: "synthetic fact", Locator: event,
	}); err != nil {
		t.Fatal(err)
	}
	var item memory.Item
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		var err error
		item, err = memory.Insert(h.ctx, tx, memory.Write{Agent: "agent", Person: person, Item: memory.Item{
			Kind: memory.Fact, Content: "synthetic fact", Person: person, Basis: memory.Derived,
			Subject:  memory.Subject{Kind: memory.ThirdParty, ID: "person-anna"},
			Evidence: []memory.Evidence{{Kind: memory.Event, ID: event, Precision: memory.Exact}},
		}}, func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
			return memory.Origin{Person: person, Agent: "agent", Speaker: guard.Person, Scope: memory.Private, Quote: "synthetic fact"}, nil
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := sender.RecordArtifact(h.ctx, links[0].ID, "item", item.ID); err != nil {
		t.Fatal(err)
	}
	repair, err := sender.Remove(h.ctx, links[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if repair.Items != 1 || !repair.Requeued {
		t.Fatalf("repair = %+v", repair)
	}
	if n := count("SELECT count(*) FROM retract_visible_memory WHERE id=?", item.ID); n != 0 {
		t.Fatal("the item citing the removed link stayed visible")
	}
	if n := count("SELECT count(*) FROM identity_requeue WHERE user='U-anna'"); n != 1 {
		t.Fatalf("requeues for the repaired identity = %d", n)
	}
	if n := count("SELECT count(*) FROM identity_requeue"); n != 1 {
		t.Fatal("another identity's messages were queued")
	}
}

// e2ePeople adapts the records repository to sender identity in this test.
type e2ePeople struct{ store *records.Store }

func (p e2ePeople) People(ctx context.Context, person string) ([]identity.Person, error) {
	list, err := p.store.People(ctx, person)
	if err != nil {
		return nil, err
	}
	people := make([]identity.Person, 0, len(list))
	for _, record := range list {
		known := identity.Person{Record: record.ID}
		if record.Title != "" {
			known.Names = append(known.Names, record.Title)
		}
		known.Names = append(known.Names, record.Names...)
		for _, contact := range record.Contacts {
			known.Identifiers = append(known.Identifiers, identity.SeenIdentifier{Kind: identity.Kind(contact.Kind), Value: contact.Value, Source: contact.Source})
		}
		people = append(people, known)
	}
	return people, nil
}

func repositoryAt(t *testing.T, h *harness, db *canon.Store) *records.Store {
	t.Helper()
	repository, err := records.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repository.Close() })
	return repository
}

// senderMessage builds a participant's message with the identifiers the
// platform shows for them.
func senderMessage(id string, route messenger.Route, at time.Time, text, author, name, phone string) messenger.Message {
	return messenger.Message{ID: id, Route: route, At: at, Text: text, Author: messenger.Author{
		ID: author, Name: name,
		Identifiers: []messenger.Identifier{{Kind: "phone", Value: phone, Source: "fixture profile"}},
	}}
}
