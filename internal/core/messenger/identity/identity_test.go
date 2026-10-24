// SPDX-License-Identifier: Apache-2.0
package identity_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/messenger/identity"
	"github.com/thisisjun786/lina/internal/core/retract"
)

type fixture struct {
	db      *canon.Store
	events  *ledger.Store
	removal *retract.Store
	store   *identity.Store
	now     time.Time
}

func newFixture(t *testing.T, people identity.StaticPeople) *fixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	removal, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removal.Close() })
	f := &fixture{db: db, removal: removal, now: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	f.events = ledger.New(db, nil)
	f.store = identity.Open(db, "person", people, removal).WithClock(func() time.Time { return f.now })
	return f
}

func at(hour int) time.Time {
	return time.Date(2026, 10, 24, hour, 0, 0, 0, time.UTC)
}

func imessage(handle string) identity.Identity {
	return identity.Identity{Platform: "imessage", User: handle}
}

func discord(user string) identity.Identity {
	return identity.Identity{Platform: "discord", User: user}
}

func observe(t *testing.T, f *fixture, id identity.Identity, name string, identifiers ...identity.SeenIdentifier) {
	t.Helper()
	observation := identity.Observation{Identity: id, At: f.now}
	if name != "" {
		observation.Names = []identity.NameSeen{{Value: name, At: f.now}}
	}
	observation.Identifiers = identifiers
	if err := f.store.Observe(t.Context(), observation); err != nil {
		t.Fatal(err)
	}
}

func phone(value, source string) identity.SeenIdentifier {
	return identity.SeenIdentifier{Kind: identity.Phone, Value: value, Source: source}
}

func mail(value, source string) identity.SeenIdentifier {
	return identity.SeenIdentifier{Kind: identity.Email, Value: value, Source: source}
}

func links(t *testing.T, f *fixture, id identity.Identity) []identity.Link {
	t.Helper()
	links, err := f.store.Links(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return links
}

func usable(t *testing.T, f *fixture, id identity.Identity) []identity.Link {
	t.Helper()
	var out []identity.Link
	for _, link := range links(t, f, id) {
		if link.Usable(f.now) {
			out = append(out, link)
		}
	}
	return out
}

func (f *fixture) item(t *testing.T, subject, content string) memory.Item {
	t.Helper()
	return f.itemIn(t, subject, content, "native:agent")
}

func (f *fixture) itemIn(t *testing.T, subject, content, conversation string) memory.Item {
	t.Helper()
	event := "event-" + content
	if _, err := f.events.Admit(t.Context(), guard.EntryOwner, ledger.Input{
		ID: event, ConversationID: conversation, AgentID: "agent", PersonID: "person",
		Surface: ledger.Surface{Kind: "tui"}, OccurredAt: f.now, RecordedAt: f.now,
		TimeQuality: "recorded", Body: content, Locator: event,
	}); err != nil {
		t.Fatal(err)
	}
	var item memory.Item
	err := f.db.Write(t.Context(), func(tx *canon.Tx) error {
		var err error
		item, err = memory.Insert(t.Context(), tx, memory.Write{Agent: "agent", Person: "person", Item: memory.Item{
			Kind: memory.Fact, Content: content, Person: "person", Basis: memory.Derived,
			Subject:  memory.Subject{Kind: memory.ThirdParty, ID: subject},
			Evidence: []memory.Evidence{{Kind: memory.Event, ID: event, Precision: memory.Exact}},
		}}, func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
			return memory.Origin{Person: "person", Agent: "agent", Speaker: guard.Person, Scope: memory.Private, Quote: content}, nil
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func visible(t *testing.T, f *fixture, item string) bool {
	t.Helper()
	var found bool
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT EXISTS(SELECT 1 FROM retract_visible_memory WHERE id=? AND person='person')", item).Scan(&found)
	}); err != nil {
		t.Fatal(err)
	}
	return found
}

func TestExactLinkCarriesItsSignalsAndMaker(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{{
		Record: "person-anna",
		Names:  []string{"Anna Kim"},
		Identifiers: []identity.SeenIdentifier{
			{Kind: identity.Phone, Value: "+821012340001", Source: "iMessage profile"},
		},
	}}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821012340001"), "Anna Kim",
		phone("+82 10-1234-0001", "iMessage profile"))

	got := links(t, f, imessage("+821012340001"))
	if len(got) != 1 {
		t.Fatalf("links = %d, want 1", len(got))
	}
	link := got[0]
	if link.Class != identity.Exact || link.State != identity.Active || link.Record != "person-anna" {
		t.Fatalf("link = %+v", link)
	}
	if link.Maker != identity.MatcherVersion || link.Revision != 1 {
		t.Fatalf("maker/revision = %q/%d", link.Maker, link.Revision)
	}
	// The platform showed the number in the profile and as the iMessage handle.
	sources := map[string]bool{}
	for _, signal := range link.Signals {
		if signal.Kind != string(identity.Phone) || signal.Value != "+821012340001" {
			t.Fatalf("signal = %+v", signal)
		}
		sources[signal.Source] = true
	}
	if !sources["iMessage profile"] || !sources["imessage handle"] {
		t.Fatalf("signals = %+v", link.Signals)
	}
	if link.ValidFrom.IsZero() || !link.ValidThrough.IsZero() {
		t.Fatalf("validity = %v..%v", link.ValidFrom, link.ValidThrough)
	}
	if !link.Usable(f.now) {
		t.Fatal("an unambiguous exact link is not usable")
	}
}

func TestOneIdentityAcrossChatsKeepsEveryNameAndIdentifier(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{{Record: "person-anna", Identifiers: []identity.SeenIdentifier{
		{Kind: identity.Email, Value: "anna@example.com", Source: "Slack profile"},
	}}}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821012340001"), "Anna",
		phone("+821012340001", "iMessage profile"), mail("Anna@Example.com", "iMessage profile"))
	f.now = at(10)
	observe(t, f, imessage("+821012340001"), "Anna Kim", mail("anna@example.com", "Slack profile"))

	var identities, names, identifiers int
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM identity_sender").Scan(&identities); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM identity_name").Scan(&names); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM identity_identifier").Scan(&identifiers)
	}); err != nil {
		t.Fatal(err)
	}
	if identities != 1 || names != 2 {
		t.Fatalf("identities=%d names=%d, want 1 and 2", identities, names)
	}
	// The phone profile source, the phone as the platform handle, and the two
	// email sources; the lower-case slack email is the same row as the first.
	if identifiers != 4 {
		t.Fatalf("identifiers = %d, want 4", identifiers)
	}
	if len(usable(t, f, imessage("+821012340001"))) != 1 {
		t.Fatal("the shared email did not link the identity")
	}
}

func TestIdentifierNormalization(t *testing.T) {
	t.Parallel()
	phone, err := identity.NormalizePhone("+82 (10) 1234-0001")
	if err != nil || phone != "+821012340001" {
		t.Fatalf("phone = %q, %v", phone, err)
	}
	phone, err = identity.NormalizePhone("0082 10 1234 0001")
	if err != nil || phone != "+821012340001" {
		t.Fatalf("international prefix = %q, %v", phone, err)
	}
	for _, bad := range []string{"010-1234-0001", "+82 10 10", "call me", ""} {
		if _, err := identity.NormalizePhone(bad); err == nil {
			t.Fatalf("accepted a phone without a country code: %q", bad)
		}
	}
	email, err := identity.NormalizeEmail("  Anna.Kim@Example.COM ")
	if err != nil || email != "anna.kim@example.com" {
		t.Fatalf("email = %q, %v", email, err)
	}
	for _, bad := range []string{"anna", "anna@example", "an na@example.com", "@example.com"} {
		if _, err := identity.NormalizeEmail(bad); err == nil {
			t.Fatalf("accepted a malformed email: %q", bad)
		}
	}
}

func TestObservationDropsAnIdentifierItCannotNormalize(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.now = at(9)
	observe(t, f, discord("d-1"), "Someone",
		phone("010-0000-0000", "Discord profile"), mail("someone@example.com", "Discord profile"))
	var identifiers int
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM identity_identifier").Scan(&identifiers)
	}); err != nil {
		t.Fatal(err)
	}
	if identifiers != 1 {
		t.Fatalf("identifiers = %d, want only the email", identifiers)
	}
}

func TestLaterIdentifierMakesTheExactLinkAmbiguous(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-a", Identifiers: []identity.SeenIdentifier{{Kind: identity.Email, Value: "a@example.com", Source: "profile"}}},
		{Record: "person-b", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340002", Source: "profile"}}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	id := discord("d-split")
	observe(t, f, id, "Split", mail("a@example.com", "Discord profile"))
	if got := usable(t, f, id); len(got) != 1 || got[0].Record != "person-a" {
		t.Fatalf("first identifier = %+v, want one usable link to person-a", got)
	}
	f.now = at(10)
	observe(t, f, id, "Split", mail("a@example.com", "Discord profile"), phone("+821012340002", "Discord profile"))
	if got := usable(t, f, id); len(got) != 0 {
		t.Fatalf("a later identifier for another record left a usable link: %+v", got)
	}
	question, found, err := f.store.Question(t.Context(), id)
	if err != nil || !found || question.State != "open" {
		t.Fatalf("question = %+v found=%v err=%v", question, found, err)
	}
	if len(question.Candidates) != 2 || question.Candidates[0] != "person-a" || question.Candidates[1] != "person-b" {
		t.Fatalf("candidates = %v, want both records", question.Candidates)
	}
}

func TestFamilyPhoneIsAmbiguousAndAskedOnce(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-a", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821055550001", Source: "iMessage profile"}}},
		{Record: "person-b", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821055550001", Source: "iMessage profile"}}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821055550001"), "Family",
		phone("+821055550001", "iMessage profile"))
	if got := links(t, f, imessage("+821055550001")); len(got) != 0 {
		t.Fatalf("an ambiguous identity got %d links", len(got))
	}
	question, found, err := f.store.Question(t.Context(), imessage("+821055550001"))
	if err != nil || !found {
		t.Fatalf("question = %+v, %v", question, err)
	}
	if question.Reason != identity.ReasonSharedIdentifier || question.State != "open" {
		t.Fatalf("question = %+v", question)
	}
	if len(question.Candidates) != 2 || question.Candidates[0] != "person-a" || question.Candidates[1] != "person-b" {
		t.Fatalf("candidates = %v, want the two records by number", question.Candidates)
	}
	f.now = at(10)
	observe(t, f, imessage("+821055550001"), "Family",
		phone("+821055550001", "iMessage profile"))
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT asked_at FROM identity_question").Scan(new(string))
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM identity_question").Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("questions = %d, want one", count)
	}
}

func TestTeamMailboxAndContactCardAreAmbiguous(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-team-a", Identifiers: []identity.SeenIdentifier{{Kind: identity.Email, Value: "team@example.com", Source: "Google contact"}}},
		{Record: "person-team-b", Identifiers: []identity.SeenIdentifier{{Kind: identity.Email, Value: "team@example.com", Source: "Google contact"}}},
		{Record: "person-phone", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821077770001", Source: "Mac contact card"}}},
		{Record: "person-mail", Identifiers: []identity.SeenIdentifier{{Kind: identity.Email, Value: "card.b@example.com", Source: "Mac contact card"}}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, discord("d-team"), "Team", mail("team@example.com", "Slack profile"))
	observe(t, f, discord("d-card"), "Card",
		phone("+821077770001", "Mac contact card"), mail("card.b@example.com", "Mac contact card"))
	for _, id := range []identity.Identity{discord("d-team"), discord("d-card")} {
		if got := links(t, f, id); len(got) != 0 {
			t.Fatalf("%v got %d links", id, len(got))
		}
		if _, found, err := f.store.Question(t.Context(), id); err != nil || !found {
			t.Fatalf("%v: question found=%t err=%v", id, found, err)
		}
	}
}

func TestRecycledNumberEndsTheEarlierValidity(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{{Record: "person-old", Identifiers: []identity.SeenIdentifier{
		{Kind: identity.Phone, Value: "+821066660001", Source: "Discord profile 2020"},
	}}}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, discord("d-old"), "Old owner", phone("+821066660001", "Discord profile"))
	if len(usable(t, f, discord("d-old"))) != 1 {
		t.Fatal("the first identity did not link")
	}
	f.now = at(11)
	observe(t, f, imessage("+821066660001"), "New owner", phone("+821066660001", "iMessage profile"))

	earlier := links(t, f, discord("d-old"))
	if len(earlier) != 1 || earlier[0].ValidThrough.IsZero() {
		t.Fatalf("the earlier link did not end: %+v", earlier)
	}
	if !earlier[0].ValidThrough.Equal(f.now) {
		t.Fatalf("valid through = %v, want %v", earlier[0].ValidThrough, f.now)
	}
	if len(usable(t, f, discord("d-old"))) != 0 {
		t.Fatal("a number that moved stayed usable")
	}
	if got := links(t, f, imessage("+821066660001")); len(got) != 0 {
		t.Fatalf("the recycled number linked again: %+v", got)
	}
	question, found, err := f.store.Question(t.Context(), imessage("+821066660001"))
	if err != nil || !found || question.Reason != identity.ReasonIdentifierReused {
		t.Fatalf("question = %+v found=%t err=%v", question, found, err)
	}
}

func TestProposalIsNeverUsableAndARejectionStays(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{{Record: "person-ben", Names: []string{"Ben Park"}}}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, discord("d-ben"), "Ben")
	got := links(t, f, discord("d-ben"))
	if len(got) != 1 || got[0].Class != identity.Proposed {
		t.Fatalf("links = %+v", got)
	}
	if got[0].Usable(f.now) {
		t.Fatal("a proposal became usable")
	}
	if err := f.store.Reject(t.Context(), got[0].ID); err != nil {
		t.Fatal(err)
	}
	f.now = at(10)
	observe(t, f, discord("d-ben"), "Ben")
	for _, link := range links(t, f, discord("d-ben")) {
		if link.State == identity.Active {
			t.Fatalf("a rejected proposal returned: %+v", link)
		}
	}
	proposals, err := f.store.Proposals(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 0 {
		t.Fatalf("proposals = %d, want none", len(proposals))
	}
}

func TestSameNameForTwoPeopleProposesNothing(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-sam-a", Names: []string{"Sam Kim"}},
		{Record: "person-sam-b", Names: []string{"Sam Kim"}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, discord("d-sam"), "Sam Kim")
	if got := links(t, f, discord("d-sam")); len(got) != 0 {
		t.Fatalf("a shared name proposed a link: %+v", got)
	}
}

func TestOlderObservationExtendsTheLinkValidity(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-anna", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340001", Source: "iMessage profile"}}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821012340001"), "Anna", phone("+821012340001", "iMessage profile"))
	if len(usable(t, f, imessage("+821012340001"))) != 1 {
		t.Fatal("the identity did not link")
	}
	// History arrives in descending order: an older sighting of the same
	// identifier proves it belonged to this identity then too.
	f.now = at(3)
	observe(t, f, imessage("+821012340001"), "Anna", phone("+821012340001", "iMessage profile"))
	after := links(t, f, imessage("+821012340001"))
	if len(after) != 1 {
		t.Fatalf("links = %+v", after)
	}
	if !after[0].ValidFrom.Equal(at(3)) {
		t.Fatalf("valid from = %v, want the older sighting", after[0].ValidFrom)
	}
	if !after[0].Usable(at(5)) {
		t.Fatal("a message from the older period is not usable")
	}
}

func TestConfirmRecordsTheAnswerAndMakesTheLinkUsable(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-a", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821055550001", Source: "iMessage profile"}}},
		{Record: "person-b", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821055550001", Source: "iMessage profile"}}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821055550001"), "Family", phone("+821055550001", "iMessage profile"))
	link, err := f.store.Confirm(t.Context(), imessage("+821055550001"), "person-a", "that is my sister")
	if err != nil {
		t.Fatal(err)
	}
	if link.Class != identity.Confirmed || link.Record != "person-a" {
		t.Fatalf("link = %+v", link)
	}
	if len(usable(t, f, imessage("+821055550001"))) != 1 {
		t.Fatal("a confirmed link is not usable")
	}
	question, found, err := f.store.Question(t.Context(), imessage("+821055550001"))
	if err != nil || !found || question.State != "answered" || question.Answer != "that is my sister" {
		t.Fatalf("question = %+v found=%t err=%v", question, found, err)
	}
	var changes int
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM identity_link_change WHERE action='create' AND class='confirmed'").Scan(&changes)
	}); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("changes = %d, want the person's confirmed link", changes)
	}
}

func TestConfirmRepairsTheLinkItSupersedes(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-anna", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340001", Source: "iMessage profile"}}},
		{Record: "person-ben"},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821012340001"), "Anna", phone("+821012340001", "iMessage profile"))
	anna := usable(t, f, imessage("+821012340001"))
	if len(anna) != 1 || anna[0].Record != "person-anna" {
		t.Fatalf("exact link = %+v", anna)
	}
	item := f.item(t, "person-anna", "wrongly attributed")
	if err := f.store.RecordArtifact(t.Context(), anna[0].ID, "item", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Confirm(t.Context(), imessage("+821012340001"), "person-ben", "that is Ben"); err != nil {
		t.Fatal(err)
	}
	if visible(t, f, item.ID) {
		t.Fatal("the superseded link's item stayed visible")
	}
	requeues, err := f.store.Requeues(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(requeues) != 1 || requeues[0].Identity != imessage("+821012340001") {
		t.Fatalf("requeues = %+v", requeues)
	}
	remaining := usable(t, f, imessage("+821012340001"))
	if len(remaining) != 1 || remaining[0].Record != "person-ben" {
		t.Fatalf("usable = %+v", remaining)
	}
}

func TestRemoveRetractsItemsAndRequeuesOnlyThatIdentity(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-anna", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340001", Source: "iMessage profile"}}},
		{Record: "person-ben", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340002", Source: "iMessage profile"}}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821012340001"), "Anna", phone("+821012340001", "iMessage profile"))
	observe(t, f, imessage("+821012340002"), "Ben", phone("+821012340002", "iMessage profile"))
	anna := usable(t, f, imessage("+821012340001"))
	ben := usable(t, f, imessage("+821012340002"))
	if len(anna) != 1 || len(ben) != 1 {
		t.Fatal("the two identities did not link")
	}
	item := f.item(t, "person-anna", "prefers short replies")
	other := f.item(t, "person-ben", "works late")
	if err := f.store.RecordArtifact(t.Context(), anna[0].ID, "item", item.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RecordArtifact(t.Context(), ben[0].ID, "item", other.ID); err != nil {
		t.Fatal(err)
	}
	repair, err := f.store.Remove(t.Context(), anna[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if repair.Items != 1 || !repair.Requeued || repair.Description == "" {
		t.Fatalf("repair = %+v", repair)
	}
	if visible(t, f, item.ID) {
		t.Fatal("an item citing the removed link stayed visible")
	}
	if !visible(t, f, other.ID) {
		t.Fatal("another identity's item was retracted")
	}
	requeues, err := f.store.Requeues(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(requeues) != 1 || requeues[0].Identity != imessage("+821012340001") {
		t.Fatalf("requeues = %+v", requeues)
	}
	if len(usable(t, f, imessage("+821012340001"))) != 0 {
		t.Fatal("a removed link stayed usable")
	}
}

func TestMergeThenUndoRestoresLinksAndItems(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-a", Names: []string{"Anna Kim", "Anna"}, Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340001", Source: "iMessage profile"}}},
		{Record: "person-b", Names: []string{"Anna K."}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821012340001"), "Anna", phone("+821012340001", "iMessage profile"))
	link := usable(t, f, imessage("+821012340001"))
	if len(link) != 1 {
		t.Fatal("the identity did not link")
	}
	item := f.item(t, "person-a", "met at the conference")
	before := links(t, f, imessage("+821012340001"))
	if err := f.store.RecordArtifact(t.Context(), before[0].ID, "item", item.ID); err != nil {
		t.Fatal(err)
	}

	merge, err := f.store.Merge(t.Context(), "person-a", "person-b")
	if err != nil {
		t.Fatal(err)
	}
	merged := links(t, f, imessage("+821012340001"))
	if len(merged) != 1 || merged[0].Record != "person-b" {
		t.Fatalf("merged link = %+v", merged)
	}
	aliases, err := f.store.MergeAliases(t.Context(), merge)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases["person-a"]) != 2 || len(aliases["person-b"]) != 1 {
		t.Fatalf("aliases = %+v", aliases)
	}
	if subject(t, f, item.ID) != "person-b" {
		t.Fatal("the item did not follow the merge")
	}
	if err := f.store.UndoMerge(t.Context(), merge); err != nil {
		t.Fatal(err)
	}
	restored := links(t, f, imessage("+821012340001"))
	if len(restored) != 1 || restored[0].Record != "person-a" || restored[0].State != identity.Active || restored[0].Class != identity.Exact {
		t.Fatalf("restored link = %+v", restored)
	}
	if subject(t, f, item.ID) != "person-a" {
		t.Fatal("undo did not put the item back")
	}
	var state string
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT state FROM identity_merge WHERE id=?", merge).Scan(&state)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "undone" {
		t.Fatalf("merge state = %q", state)
	}
}

func subject(t *testing.T, f *fixture, item string) string {
	t.Helper()
	var subject string
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT subject_id FROM memory_item WHERE id=?", item).Scan(&subject)
	}); err != nil {
		t.Fatal(err)
	}
	return subject
}

func TestSplitMovesOneIdentityAndItsItems(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-shared", Identifiers: []identity.SeenIdentifier{
			{Kind: identity.Phone, Value: "+821012340001", Source: "iMessage profile"},
			{Kind: identity.Phone, Value: "+821012340002", Source: "iMessage profile"},
		}},
		{Record: "person-split"},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821012340001"), "One", phone("+821012340001", "iMessage profile"))
	observe(t, f, imessage("+821012340002"), "Two", phone("+821012340002", "iMessage profile"))
	first := usable(t, f, imessage("+821012340001"))
	second := usable(t, f, imessage("+821012340002"))
	if len(first) != 1 || len(second) != 1 {
		t.Fatal("both identities did not link")
	}
	item := f.item(t, "person-shared", "one of the two")
	if err := f.store.RecordArtifact(t.Context(), second[0].ID, "item", item.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Split(t.Context(), "person-shared", "person-split", []identity.Identity{imessage("+821012340002")}); err != nil {
		t.Fatal(err)
	}
	if got := links(t, f, imessage("+821012340002")); len(got) != 1 || got[0].Record != "person-split" {
		t.Fatalf("split link = %+v", got)
	}
	if got := links(t, f, imessage("+821012340001")); len(got) != 1 || got[0].Record != "person-shared" {
		t.Fatalf("other link = %+v", got)
	}
	if subject(t, f, item.ID) != "person-split" {
		t.Fatal("the item did not follow its identity")
	}
}

// stopRemovals can forget only in its own later transaction. A repair must
// refuse it and leave the link unchanged when items have to be retracted.
type stopRemovals struct{}

func (stopRemovals) Forget(context.Context, retract.Request) error {
	return errors.New("removal stopped")
}

func TestRepairDoesNotKeepALinkChangeWhenRetractionStops(t *testing.T) {
	t.Parallel()
	for _, act := range []string{"remove", "correct", "confirm", "no removal path"} {
		t.Run(act, func(t *testing.T) {
			t.Parallel()
			people := identity.StaticPeople{
				{Record: "person-wrong", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340001", Source: "Discord profile"}}},
				{Record: "person-right"},
			}
			f := newFixture(t, people)
			f.now = at(9)
			observe(t, f, discord("d-repair"), "Someone", phone("+821012340001", "Discord profile"))
			before := usable(t, f, discord("d-repair"))
			if len(before) != 1 || before[0].Record != "person-wrong" {
				t.Fatalf("exact link = %+v", before)
			}
			item := f.item(t, "person-wrong", "repair rollback "+act)
			if err := f.store.RecordArtifact(t.Context(), before[0].ID, "item", item.ID); err != nil {
				t.Fatal(err)
			}
			var removals identity.Removals = stopRemovals{}
			if act == "no removal path" {
				removals = nil
			}
			f.store = identity.Open(f.db, "person", people, removals).WithClock(func() time.Time { return f.now })
			var err error
			switch act {
			case "remove", "no removal path":
				_, err = f.store.Remove(t.Context(), before[0].ID)
			case "correct":
				_, err = f.store.Correct(t.Context(), before[0].ID, "person-right")
			case "confirm":
				_, err = f.store.Confirm(t.Context(), discord("d-repair"), "person-right", "that one")
			}
			if !errors.Is(err, identity.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
			got := links(t, f, discord("d-repair"))
			if len(got) != 1 || got[0].Record != "person-wrong" || got[0].Class != identity.Exact || got[0].State != identity.Active {
				t.Fatalf("link changed without a retraction: %+v", got)
			}
			if !visible(t, f, item.ID) {
				t.Fatal("the item was hidden without a committed repair")
			}
		})
	}
}

// TestRepairRetractsACitingItemHiddenByItsAncestor proves a repair still
// retracts an item that is hidden only because an ancestor in its evidence
// was forgotten. Restoring that ancestor must not show the item again.
func TestRepairRetractsACitingItemHiddenByItsAncestor(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-wrong", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340001", Source: "Discord profile"}}},
		{Record: "person-right"},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, discord("d-hidden"), "Someone", phone("+821012340001", "Discord profile"))
	link := usable(t, f, discord("d-hidden"))
	if len(link) != 1 {
		t.Fatal("the identity did not link")
	}
	ancestor := f.itemIn(t, "person-wrong", "ancestor fact", "native:ancestor")
	child := f.itemCiting(t, "person-wrong", "citing fact", ancestor.ID)
	if err := f.store.RecordArtifact(t.Context(), link[0].ID, "item", child.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.removal.Forget(t.Context(), retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: ancestor.ID}}}); err != nil {
		t.Fatal(err)
	}
	if visible(t, f, child.ID) {
		t.Fatal("the ancestor's removal left the citing item visible")
	}
	if _, err := f.store.Correct(t.Context(), link[0].ID, "person-right"); err != nil {
		t.Fatal(err)
	}
	if err := f.removal.Restore(t.Context(), retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: ancestor.ID}}}); err != nil {
		t.Fatal(err)
	}
	if visible(t, f, child.ID) {
		t.Fatal("restoring the ancestor showed the unrepaired citing item")
	}
}

func (f *fixture) itemCiting(t *testing.T, subject, content, ancestor string) memory.Item {
	t.Helper()
	var item memory.Item
	err := f.db.Write(t.Context(), func(tx *canon.Tx) error {
		var err error
		item, err = memory.Insert(t.Context(), tx, memory.Write{Agent: "agent", Person: "person", Item: memory.Item{
			Kind: memory.Fact, Content: content, Person: "person", Basis: memory.Derived,
			Subject:  memory.Subject{Kind: memory.ThirdParty, ID: subject},
			Evidence: []memory.Evidence{{Kind: memory.MemoryItem, ID: ancestor, Precision: memory.Exact}},
		}}, func(context.Context, *canon.Tx, memory.Evidence) (memory.Origin, error) {
			return memory.Origin{Person: "person", Agent: "agent", Speaker: guard.Person, Scope: memory.Private, Quote: content}, nil
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestCorrectMovesTheLinkAndRetractsItsItems(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-wrong", Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340001", Source: "Discord profile"}}},
		{Record: "person-right"},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, discord("d-1"), "Someone", phone("+821012340001", "Discord profile"))
	link := usable(t, f, discord("d-1"))
	if len(link) != 1 {
		t.Fatal("the identity did not link")
	}
	item := f.item(t, "person-wrong", "wrongly attributed")
	if err := f.store.RecordArtifact(t.Context(), link[0].ID, "item", item.ID); err != nil {
		t.Fatal(err)
	}
	repair, err := f.store.Correct(t.Context(), link[0].ID, "person-right")
	if err != nil {
		t.Fatal(err)
	}
	if repair.Items != 1 || visible(t, f, item.ID) {
		t.Fatalf("repair = %+v visible=%t", repair, visible(t, f, item.ID))
	}
	corrected := links(t, f, discord("d-1"))
	if len(corrected) != 1 || corrected[0].Record != "person-right" || corrected[0].Class != identity.Confirmed {
		t.Fatalf("corrected link = %+v", corrected)
	}
	requeues, err := f.store.Requeues(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(requeues) != 1 || requeues[0].Identity != discord("d-1") {
		t.Fatalf("requeues = %+v", requeues)
	}
}

func TestProposalsComeInOneBatchOfAtMostFive(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-a", Names: []string{"Anna"}},
		{Record: "person-b", Names: []string{"Ben"}},
		{Record: "person-c", Names: []string{"Cara"}},
		{Record: "person-d", Names: []string{"Dana"}},
		{Record: "person-e", Names: []string{"Eli"}},
		{Record: "person-f", Names: []string{"Finn"}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	for _, name := range []string{"Anna", "Ben", "Cara", "Dana", "Eli", "Finn"} {
		observe(t, f, discord("d-"+name), name)
	}
	proposals, err := f.store.Proposals(t.Context(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 5 {
		t.Fatalf("proposals = %d, want 5", len(proposals))
	}
	for _, proposal := range proposals {
		if proposal.Class != identity.Proposed || proposal.Usable(f.now) {
			t.Fatalf("proposal = %+v", proposal)
		}
	}
}

func TestStoreRejectsAnIncompleteObservation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	if err := f.store.Observe(t.Context(), identity.Observation{Identity: identity.Identity{Platform: "discord"}}); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

// TestLaterObservationDoesNotMatchASelfAccount proves an account already
// recorded as the person's own stays that way. A later observation that omits
// the self mark must not match the account to a people record.
func TestLaterObservationDoesNotMatchASelfAccount(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{{
		Record: "person-anna",
		Identifiers: []identity.SeenIdentifier{
			{Kind: identity.Phone, Value: "+821012340001", Source: "contacts"},
		},
	}}
	f := newFixture(t, people)
	f.now = at(9)
	account := imessage("+821099990001")
	if err := f.store.Observe(t.Context(), identity.Observation{Identity: account, At: f.now, Self: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Observe(t.Context(), identity.Observation{
		Identity: account, At: f.now.Add(time.Minute),
		Identifiers: []identity.SeenIdentifier{phone("+821012340001", "profile")},
	}); err != nil {
		t.Fatal(err)
	}
	if got := links(t, f, account); len(got) != 0 {
		t.Fatalf("a self account gained a third-party link: %+v", got)
	}
}

// TestUndoMergeAfterAnItemIsForgottenOrDeleted proves ordinary memory removal
// does not make a merge impossible to undo, and does not bring a removed item
// back.
func TestUndoMergeAfterAnItemIsForgottenOrDeleted(t *testing.T) {
	t.Parallel()
	people := identity.StaticPeople{
		{Record: "person-a", Names: []string{"Anna"}, Identifiers: []identity.SeenIdentifier{{Kind: identity.Phone, Value: "+821012340001", Source: "iMessage profile"}}},
		{Record: "person-b", Names: []string{"Ann"}},
	}
	f := newFixture(t, people)
	f.now = at(9)
	observe(t, f, imessage("+821012340001"), "Anna", phone("+821012340001", "iMessage profile"))
	// Each item has its own conversation. Forgetting one must not block the
	// others through the conversation exclusion.
	kept := f.itemIn(t, "person-a", "still here", "native:kept")
	forgotten := f.itemIn(t, "person-a", "please forget", "native:forgotten")
	deleted := f.itemIn(t, "person-a", "please delete", "native:deleted")
	merge, err := f.store.Merge(t.Context(), "person-a", "person-b")
	if err != nil {
		t.Fatal(err)
	}
	if subject(t, f, kept.ID) != "person-b" || subject(t, f, forgotten.ID) != "person-b" {
		t.Fatal("the merge did not move the items")
	}
	ref := func(id string) retract.Request {
		return retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Memory, ID: id}}}
	}
	if err := f.removal.Forget(t.Context(), ref(forgotten.ID)); err != nil {
		t.Fatal(err)
	}
	if err := f.removal.Delete(t.Context(), ref(deleted.ID)); err != nil {
		t.Fatal(err)
	}
	if visible(t, f, forgotten.ID) || visible(t, f, deleted.ID) {
		t.Fatal("a removed item stayed visible")
	}
	if err := f.store.UndoMerge(t.Context(), merge); err != nil {
		t.Fatalf("undo after ordinary removal: %v", err)
	}
	restored := links(t, f, imessage("+821012340001"))
	if len(restored) != 1 || restored[0].Record != "person-a" || restored[0].State != identity.Active {
		t.Fatalf("restored link = %+v", restored)
	}
	if subject(t, f, kept.ID) != "person-a" || !visible(t, f, kept.ID) {
		t.Fatal("the surviving item did not return")
	}
	state, subj, ok := itemRow(t, f, forgotten.ID)
	if !ok || state != "trashed" || subj != "person-a" || visible(t, f, forgotten.ID) {
		t.Fatalf("forgotten item = state %q subject %q present %t", state, subj, ok)
	}
	if _, _, ok := itemRow(t, f, deleted.ID); ok {
		t.Fatal("a permanently deleted item was resurrected")
	}
	var mergeState string
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT state FROM identity_merge WHERE id=?", merge).Scan(&mergeState)
	}); err != nil {
		t.Fatal(err)
	}
	if mergeState != "undone" {
		t.Fatalf("merge state = %q", mergeState)
	}
}

func itemRow(t *testing.T, f *fixture, id string) (state, subject string, ok bool) {
	t.Helper()
	err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		err := tx.QueryRowContext(t.Context(), "SELECT state,subject_id FROM memory_item WHERE id=?", id).Scan(&state, &subject)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		ok = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state, subject, ok
}
