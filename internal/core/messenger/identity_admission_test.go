// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/identity"
)

// TestAdmittedPageRecordsTheSenderIdentity proves admission observes each author
// inside the page's own transaction: an external participant's phone links it to
// the people record in one commit, and the person's own account is never linked
// as another person.
func TestAdmittedPageRecordsTheSenderIdentity(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{{
		Record:      "person-anna",
		Names:       []string{"Anna Kim"},
		Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340001", Source: "iMessage profile"}},
	}}
	f := newFixture(t)
	sender := identity.Open(f.db, "person", people, f.removal).WithClock(func() time.Time { return f.now })
	f.store = f.store.WithIdentities(sender)

	source := messenger.Source{Person: "person", Platform: "slack", Account: "account-1", Workspace: "T-1",
		Mode: messenger.ModeOfficial, Adapter: "slack", State: messenger.StateConnected, ConnectedAt: f.now}
	if err := f.store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	external := messenger.Message{ID: "m-1", Route: messenger.RouteParticipant, At: f.now, Text: "hello",
		Author: messenger.Author{ID: "U-anna", Name: "Anna", Identifiers: []messenger.Identifier{
			{Kind: "phone", Value: "+82 10-1234-0001", Source: "Slack profile"},
		}}}
	self := messenger.Message{ID: "m-2", Route: messenger.RoutePerson, At: f.now, Text: "hi",
		Author: messenger.Author{ID: "U-me", Name: "Me"}}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{external, self}}); err != nil {
		t.Fatal(err)
	}

	links, err := sender.Links(t.Context(), identity.Identity{Platform: "slack", Scope: "T-1", User: "U-anna"})
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Record != "person-anna" || links[0].Class != identity.Exact || !links[0].Usable(f.now) {
		t.Fatalf("links = %+v", links)
	}
	if links[0].Signals[0].Source != "Slack profile" {
		t.Fatalf("signal = %+v", links[0].Signals)
	}
	if got, err := sender.Links(t.Context(), identity.Identity{Platform: "slack", Scope: "T-1", User: "U-me"}); err != nil || len(got) != 0 {
		t.Fatalf("the person's own account got links: %+v err=%v", got, err)
	}
	// The workspace is the identity scope, so the same user id in another
	// workspace is another identity.
	var scopes int
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM identity_sender WHERE scope='T-1'").Scan(&scopes)
	}); err != nil {
		t.Fatal(err)
	}
	if scopes != 2 {
		t.Fatalf("identities in the workspace = %d, want 2", scopes)
	}
}
