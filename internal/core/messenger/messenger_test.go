// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/retract"
	"github.com/thisisjun786/lina/kit/secret"
)

type fixture struct {
	db      *canon.Store
	root    string
	events  *ledger.Store
	removal *retract.Store
	store   *messenger.Store
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
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
	removals, err := retract.Open(t.Context(), retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removals.Close() })
	f := &fixture{db: db, root: root, removal: removals, now: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	f.events = ledger.New(db, nil)
	f.store = messenger.Open(db, nil, removals).WithClock(func() time.Time { return f.now })
	return f
}

func (f *fixture) source() messenger.Source {
	return messenger.Source{Person: "person", Platform: "discord", Account: "account-1", Mode: messenger.ModeOfficial,
		Adapter: "discord", State: messenger.StateConnected, ConnectedAt: f.now}
}

func (f *fixture) connect(t *testing.T) messenger.Source {
	t.Helper()
	source := f.source()
	if err := f.store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	return source
}

func message(id string, route messenger.Route, at time.Time, text string) messenger.Message {
	return messenger.Message{ID: id, Route: route, At: at, Text: text, Author: messenger.Author{ID: "author-" + id, Name: "someone"}}
}

func count(t *testing.T, db *canon.Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// A chat read twice adds nothing the second time.
func TestSecondReadAddsNothing(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	page := messenger.Page{
		Messages: []messenger.Message{
			message("m-1", messenger.RoutePerson, f.now, "first"),
			message("m-2", messenger.RouteParticipant, f.now.Add(time.Minute), "second"),
		},
		Next: "cursor-2",
	}
	result, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page)
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 2 {
		t.Fatalf("first read admitted %d records", result.Added)
	}
	again, err := f.store.AdmitPage(t.Context(), source, "dm-1", "cursor-2", page)
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 || again.Updated != 0 {
		t.Fatalf("second read added %d and updated %d", again.Added, again.Updated)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%'"); n != 2 {
		t.Fatalf("records = %d, want 2", n)
	}
	chats, err := f.store.Chats(t.Context(), "person", "discord", "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Cursor != "cursor-2" {
		t.Fatalf("cursor: %+v", chats)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_coverage WHERE chat='dm-1'"); n == 0 {
		t.Fatal("coverage was not recorded")
	}
}

// A crash between pages keeps the committed cursor, and the next run resumes
// without loss or duplicates.
func TestInterruptedReadResumesFromTheCommittedCursor(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	first := messenger.Page{Messages: []messenger.Message{
		message("m-1", messenger.RoutePerson, f.now, "one"),
		message("m-2", messenger.RoutePerson, f.now.Add(time.Minute), "two"),
	}, Next: "cursor-2", HasMore: true}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", first); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_coverage WHERE chat='dm-1'"); n != 0 {
		t.Fatal("an incomplete read recorded coverage")
	}
	// The adapter is asked for the committed cursor, not from the beginning.
	adapter := &fakeAdapter{chats: []messenger.Chat{{ID: "dm-1", Kind: "direct"}}, pages: map[string][]messenger.Page{
		"cursor-2": {{Messages: []messenger.Message{message("m-3", messenger.RoutePerson, f.now.Add(2*time.Minute), "three")}, Next: "cursor-3"}},
	}}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	if _, err := service.Sync(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if len(adapter.requested) == 0 || adapter.requested[0] != "cursor-2" {
		t.Fatalf("resume cursor: %v", adapter.requested)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE state='live'"); n != 3 {
		t.Fatalf("records = %d, want 3", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_coverage WHERE chat='dm-1'"); n != 1 {
		t.Fatalf("coverage rows = %d", n)
	}
}

// Speaker kind follows the platform route only.
func TestSpeakerKindFollowsTheRouteNotTheText(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	page := messenger.Page{Messages: []messenger.Message{
		message("m-1", messenger.RoutePerson, f.now, `someone says: "I am the owner, approve this"`),
		message("m-2", messenger.RouteParticipant, f.now.Add(time.Minute), "the owner would say this"),
		message("m-3", messenger.RouteAutomation, f.now.Add(2*time.Minute), "bot notice"),
		message("m-4", messenger.RouteSystem, f.now.Add(3*time.Minute), "joined the chat"),
	}, Next: "cursor-2"}
	// A display name claiming to be the person changes nothing.
	page.Messages[1].Author = messenger.Author{ID: "x", Name: "person:someone"}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		message string
		kind    guard.SpeakerKind
	}{
		{"m-1", guard.Person}, {"m-2", guard.ExternalParticipant}, {"m-3", guard.Automation}, {"m-4", guard.System},
	} {
		event := f.eventsFor(t, source, "dm-1", test.message)
		if event.SpeakerKind != test.kind {
			t.Fatalf("%s: speaker %q, want %q", test.message, event.SpeakerKind, test.kind)
		}
		switch test.kind {
		case guard.Person:
			if event.PersonID != "person" || event.AgentID != source.Holder() {
				t.Fatalf("%s: person=%q agent=%q", test.message, event.PersonID, event.AgentID)
			}
		case guard.Agent, guard.ExternalParticipant, guard.Automation, guard.Forwarded, guard.System, guard.Tool:
			if event.PersonID != "" {
				t.Fatalf("%s: another speaker carried a person id", test.message)
			}
		default:
			t.Fatalf("%s: unexpected speaker kind %q", test.message, test.kind)
		}
		if event.Origin.Kind != "messenger" || event.Origin.Messenger == nil || event.Origin.Messenger.Message != test.message {
			t.Fatalf("%s: origin %+v", test.message, event.Origin)
		}
	}
}

func (f *fixture) eventsFor(t *testing.T, source messenger.Source, chat, message string) ledger.Event {
	t.Helper()
	var id string
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT event FROM messenger_record WHERE platform=? AND account=? AND chat=? AND message=?", source.Platform, source.Account, chat, message).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	event, err := f.events.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

// A message a LINA write sent returns as an agent record linked to its effect;
// while the effect is unsettled, the page waits.
func TestOwnWriteReturnsAsAgentRecord(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	effect := messenger.Effect{ID: "effect-1", Platform: source.Platform, Account: source.Account, Chat: "dm-1",
		Agent: "agent:main", Verb: "send", ClientID: "m-1", State: messenger.EffectPending, CreatedAt: f.now}
	if err := f.store.RecordEffect(t.Context(), effect); err != nil {
		t.Fatal(err)
	}
	page := messenger.Page{Messages: []messenger.Message{message("m-1", messenger.RoutePerson, f.now, "hello from LINA")}, Next: "cursor-2"}
	result, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page)
	if !errors.Is(err, messenger.ErrPageWaiting) || result.Waiting != 1 {
		t.Fatalf("pending write did not wait: %+v %v", result, err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%'"); n != 0 {
		t.Fatal("a waiting page admitted a record")
	}
	if err := f.store.SettleEffect(t.Context(), "effect-1", messenger.EffectSent, "platform-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
		t.Fatal(err)
	}
	event := f.eventsFor(t, source, "dm-1", "m-1")
	if event.SpeakerKind != guard.Agent || event.AgentID != "agent:main" || event.PersonID != "" {
		t.Fatalf("own write: %+v", event)
	}
}

// A channel root and its replies can arrive on one page, with the reply listed
// first. The reply still records replies_to and in_thread to that root.
func TestSamePageRootAndReplyKeepRelations(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	root := message("root", messenger.RouteParticipant, f.now, "thread root")
	reply := message("reply", messenger.RouteParticipant, f.now.Add(time.Minute), "answer")
	reply.ReplyTo = "root"
	reply.Thread = "root"
	orphan := message("orphan", messenger.RouteParticipant, f.now.Add(2*time.Minute), "no root")
	orphan.ReplyTo = "missing"
	orphan.Thread = "missing"
	result, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
		Messages: []messenger.Message{reply, orphan, root}, Next: "c1",
	})
	if err != nil || result.Added != 3 {
		t.Fatalf("admit: %+v %v", result, err)
	}
	rootEvent := f.eventsFor(t, source, "dm-1", "root")
	replyEvent := f.eventsFor(t, source, "dm-1", "reply")
	if !relationTargets(replyEvent, "replies_to", rootEvent.ID) || !relationTargets(replyEvent, "in_thread", rootEvent.ID) {
		t.Fatalf("reply relations: %+v", replyEvent.Relations)
	}
	orphanEvent := f.eventsFor(t, source, "dm-1", "orphan")
	if len(orphanEvent.Relations) != 0 {
		t.Fatalf("missing target still linked: %+v", orphanEvent.Relations)
	}
	later := message("later", messenger.RouteParticipant, f.now.Add(3*time.Minute), "after")
	later.ReplyTo = "root"
	later.Thread = "root"
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c1", messenger.Page{
		Messages: []messenger.Message{later}, Next: "c2",
	}); err != nil {
		t.Fatal(err)
	}
	laterEvent := f.eventsFor(t, source, "dm-1", "later")
	if !relationTargets(laterEvent, "replies_to", rootEvent.ID) || !relationTargets(laterEvent, "in_thread", rootEvent.ID) {
		t.Fatalf("committed root: %+v", laterEvent.Relations)
	}
}

// A stored root edited on the same page as a new reply is linked once, to the
// revision this page admits.
func TestReplyLinksOneRevisionOfEditedRoot(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	root := message("root", messenger.RouteParticipant, f.now, "thread root")
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
		Messages: []messenger.Message{root}, Next: "c1",
	}); err != nil {
		t.Fatal(err)
	}
	edited := root
	edited.Text = "thread root, edited"
	edited.Edited = f.now.Add(time.Minute)
	reply := message("reply", messenger.RouteParticipant, f.now.Add(2*time.Minute), "answer")
	reply.ReplyTo = "root"
	reply.Thread = "root"
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c1", messenger.Page{
		Messages: []messenger.Message{reply, edited}, Next: "c2",
	}); err != nil {
		t.Fatal(err)
	}
	rootEvent := f.eventsFor(t, source, "dm-1", "root")
	replyEvent := f.eventsFor(t, source, "dm-1", "reply")
	if len(replyEvent.Relations) != 2 || !relationTargets(replyEvent, "replies_to", rootEvent.ID) || !relationTargets(replyEvent, "in_thread", rootEvent.ID) {
		t.Fatalf("reply relations: %+v, root %s", replyEvent.Relations, rootEvent.ID)
	}
}

func relationTargets(event ledger.Event, kind, target string) bool {
	for _, relation := range event.Relations {
		if relation.Kind == kind && relation.TargetID == target {
			return true
		}
	}
	return false
}

// Two messages that reply to each other still commit. A cycle keeps the one
// relation that can name an event already written.
func TestMutualRepliesOnOnePageStillCommit(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	first := message("a", messenger.RouteParticipant, f.now, "first")
	second := message("b", messenger.RouteParticipant, f.now.Add(time.Minute), "second")
	first.ReplyTo, first.Thread = "b", "b"
	second.ReplyTo, second.Thread = "a", "a"
	result, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
		Messages: []messenger.Message{first, second}, Next: "c1",
	})
	if err != nil || result.Added != 2 {
		t.Fatalf("admit: %+v %v", result, err)
	}
	a := f.eventsFor(t, source, "dm-1", "a")
	b := f.eventsFor(t, source, "dm-1", "b")
	aToB := relationTargets(a, "replies_to", b.ID)
	bToA := relationTargets(b, "replies_to", a.ID)
	if aToB == bToA {
		t.Fatalf("cycle kept both or neither: a=%+v b=%+v", a.Relations, b.Relations)
	}
}

// An edit is a new event that supersedes the earlier one; the earlier stays.
func TestEditSupersedesAndEarlierStays(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	original := message("m-1", messenger.RoutePerson, f.now, "before")
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{original}, Next: "c1"}); err != nil {
		t.Fatal(err)
	}
	edited := original
	edited.Text = "after"
	edited.Edited = f.now.Add(time.Minute)
	result, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c1", messenger.Page{Messages: []messenger.Message{edited}, Next: "c2"})
	if err != nil || result.Updated != 1 {
		t.Fatalf("edit: %+v %v", result, err)
	}
	newest := f.eventsFor(t, source, "dm-1", "m-1")
	if newest.Body.Text() != "after" || len(newest.Relations) != 1 || newest.Relations[0].Kind != "supersedes" {
		t.Fatalf("newest: %+v", newest)
	}
	older, err := f.events.Get(t.Context(), newest.Relations[0].TargetID)
	if err != nil {
		t.Fatal(err)
	}
	if older.Body.Text() != "before" {
		t.Fatalf("earlier text: %q", older.Body.Text())
	}
	// The same edit read again changes nothing.
	again, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c2", messenger.Page{Messages: []messenger.Message{edited}, Next: "c2"})
	if err != nil || again.Updated != 0 || again.Added != 0 {
		t.Fatalf("repeat edit: %+v %v", again, err)
	}
}

// A platform deletion hides the record at once, keeps the reference as a
// tombstone and a later read does not bring it back.
func TestPlatformDeletionHidesAndKeepsTheTombstone(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	message := message("m-1", messenger.RouteParticipant, f.now, "delete me")
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{message}, Next: "c1"}); err != nil {
		t.Fatal(err)
	}
	deleted, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c1", messenger.Page{
		Messages:  []messenger.Message{message},
		Deletions: []messenger.Deletion{{Message: "m-1", At: f.now.Add(time.Minute)}}, Next: "c2"})
	if err != nil || deleted.Deleted != 1 {
		t.Fatalf("deletion: %+v %v", deleted, err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM retract_visible_event WHERE origin LIKE '%messenger%'"); n != 0 {
		t.Fatal("deleted record stayed visible")
	}
	if n := count(t, f.db, "SELECT count(*) FROM retract_target WHERE kind='event' AND state='trashed'"); n != 1 {
		t.Fatal("deletion did not follow the trash rule")
	}
	if n := count(t, f.db, "SELECT count(*) FROM recall_doc WHERE kind='event'"); n != 0 {
		t.Fatal("deleted record stayed in the one index")
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE message='m-1' AND state='deleted'"); n != 1 {
		t.Fatal("legacy reference was not kept as a tombstone")
	}
	// The removal list keeps the platform reference, never the content.
	data, err := os.ReadFile(filepath.Join(f.root, "backups", "removals"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"key":"dm-1/m-1"`) || strings.Contains(string(data), "delete me") {
		t.Fatalf("removal line: %s", data)
	}
	// A later read does not resurrect it, and it is not reported as new.
	after, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c2", messenger.Page{Messages: []messenger.Message{message}, Next: "c3"})
	if err != nil || after.Added != 0 {
		t.Fatalf("resurrection: %+v %v", after, err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM retract_visible_event WHERE origin LIKE '%messenger%'"); n != 0 {
		t.Fatal("a later read restored the deleted record")
	}
}

// A message missing from a later page is not a deletion.
func TestMissingMessageIsNotADeletion(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{
		message("m-1", messenger.RoutePerson, f.now, "kept"),
		message("m-2", messenger.RoutePerson, f.now.Add(time.Minute), "also kept"),
	}, Next: "c1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c1", messenger.Page{Messages: []messenger.Message{
		message("m-3", messenger.RoutePerson, f.now.Add(2*time.Minute), "new"),
	}, Next: "c2"}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM retract_target"); n != 0 {
		t.Fatal("a missing message was retracted")
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE state='live'"); n != 3 {
		t.Fatalf("records = %d", n)
	}
}

// Records are documents of the one index with origin messenger, and a secret in
// a URL is masked at admission.
func TestRecordBecomesOneIndexDocumentWithMaskedURL(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	text := "join https://example.com/cb?token=synthetic-abc&next=/home"
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{
		message("m-1", messenger.RoutePerson, f.now, text),
	}, Next: "c1"}); err != nil {
		t.Fatal(err)
	}
	event := f.eventsFor(t, source, "dm-1", "m-1")
	if strings.Contains(event.Body.Text(), "synthetic-abc") {
		t.Fatalf("unmasked URL secret: %q", event.Body.Text())
	}
	if !strings.Contains(event.Body.Text(), "next=/home") {
		t.Fatalf("public URL text was removed: %q", event.Body.Text())
	}
	var origin string
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT origin FROM recall_doc WHERE kind='event' AND source_id=?", event.ID).Scan(&origin)
	}); err != nil {
		t.Fatal(err)
	}
	var record struct{ Kind string }
	if err := json.Unmarshal([]byte(origin), &record); err != nil || record.Kind != "messenger" {
		t.Fatalf("index origin %q: %v", origin, err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM recall_text WHERE recall_text MATCH ?", canon.TrigramPhrase("synthetic-abc")); n != 0 {
		t.Fatal("the indexed text kept the secret")
	}
	if n := count(t, f.db, "SELECT count(*) FROM recall_text WHERE recall_text MATCH ?", canon.TrigramPhrase("example.com")); n == 0 {
		t.Fatal("the indexed text lost the public URL")
	}
	_ = secret.Placeholder
}

// A source without an adapter, a failing source and a disconnected source never
// stop the conversation.
func TestSyncReportsFailureOnTheSource(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": &fakeAdapter{fail: errors.New("platform refused")}}, time.Hour)
	service.SyncAll(t.Context(), &f.now)
	sources, err := f.store.Sources(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].State != messenger.StateFailing || !strings.Contains(sources[0].Failure, "platform refused") {
		t.Fatalf("failing source: %+v", sources)
	}
	if err := f.store.Disconnect(t.Context(), "person", source.Platform, source.Account); err != nil {
		t.Fatal(err)
	}
	if result := service.SyncAll(t.Context(), &f.now); len(result) != 0 {
		t.Fatalf("disconnected source synced: %+v", result)
	}
}

func TestSyncRetriesFailingSource(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	adapter := &fakeAdapter{fail: errors.New("temporary platform failure")}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	service.SyncAll(t.Context(), &f.now)
	adapter.fail = nil
	adapter.pages = map[string][]messenger.Page{"": {{Messages: []messenger.Message{message("recovered", messenger.RouteParticipant, f.now, "recovered message")}, Next: "recovered-cursor"}}}
	later := f.now.Add(time.Hour)
	service.SyncAll(t.Context(), &later)
	sources, err := f.store.Sources(t.Context(), source.Person)
	if err != nil || len(sources) != 1 || sources[0].State != messenger.StateConnected || len(adapter.requested) == 0 {
		t.Fatalf("source did not recover: sources=%+v requested=%v err=%v", sources, adapter.requested, err)
	}
}

// An unofficial source needs the person's accepted terms.
func TestUnofficialSourceRequiresAcceptedTerms(t *testing.T) {
	f := newFixture(t)
	source := f.source()
	source.Mode = messenger.ModeUnofficial
	if err := f.store.Connect(t.Context(), source); !errors.Is(err, messenger.ErrInvalid) {
		t.Fatalf("unofficial source connected without terms: %v", err)
	}
	source.AcceptedTerms = &messenger.Terms{Person: "person", At: f.now, Text: "terms forbid automating an account"}
	if err := f.store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	sources, err := f.store.Sources(t.Context(), "person")
	if err != nil || len(sources) != 1 || sources[0].AcceptedTerms == nil || sources[0].AcceptedTerms.Text != source.AcceptedTerms.Text {
		t.Fatalf("accepted terms: %+v %v", sources, err)
	}
}

// Freshness follows the last complete read and the platform's read window.
func TestFreshnessStates(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{
		message("m-1", messenger.RoutePerson, f.now, "hello"),
	}, Next: "c1"}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(30 * time.Minute)
	freshness, err := f.store.Freshness(t.Context(), source, "dm-1", 10*time.Minute, 2*time.Hour)
	if err != nil || freshness.State != "stale" {
		t.Fatalf("stale: %+v %v", freshness, err)
	}
	f.now = f.now.Add(3 * time.Hour)
	freshness, err = f.store.Freshness(t.Context(), source, "dm-1", 10*time.Minute, 2*time.Hour)
	if err != nil || freshness.State != "gap" {
		t.Fatalf("gap: %+v %v", freshness, err)
	}
}

// The person's deletion of a chat hides every record of it.
func TestDeleteChatHidesEveryRecord(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{
		message("m-1", messenger.RoutePerson, f.now, "one"),
		message("m-2", messenger.RouteParticipant, f.now.Add(time.Minute), "two"),
	}, Next: "c1"}); err != nil {
		t.Fatal(err)
	}
	n, err := f.store.DeleteChat(t.Context(), source, "dm-1")
	if err != nil || n != 2 {
		t.Fatalf("delete chat: %d %v", n, err)
	}
	if left := count(t, f.db, "SELECT count(*) FROM retract_visible_event WHERE origin LIKE '%messenger%'"); left != 0 {
		t.Fatalf("visible records = %d", left)
	}
}

// chatsFake is a scripted adapter: pages are served per cursor and every
// requested cursor is recorded.
type fakeAdapter struct {
	chats     []messenger.Chat
	pages     map[string][]messenger.Page
	requested []messenger.Cursor
	fail      error
	calls     int
}

func (a *fakeAdapter) SignIn(context.Context, messenger.SourceID) (messenger.SignInState, error) {
	if a.fail != nil {
		return messenger.Failing, a.fail
	}
	return messenger.SignedIn, nil
}

func (a *fakeAdapter) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	if a.fail != nil {
		return nil, a.fail
	}
	return a.chats, nil
}

func (a *fakeAdapter) Page(_ context.Context, _ messenger.SourceID, _ string, cursor messenger.Cursor) (messenger.Page, error) {
	a.requested = append(a.requested, cursor)
	if a.fail != nil {
		return messenger.Page{}, a.fail
	}
	pages := a.pages[string(cursor)]
	if len(pages) == 0 {
		a.calls++
		return messenger.Page{Next: cursor}, nil
	}
	page := pages[0]
	a.pages[string(cursor)] = pages[1:]
	return page, nil
}

func (a *fakeAdapter) Members(context.Context, messenger.SourceID, string) ([]messenger.Author, error) {
	return nil, nil
}
func (a *fakeAdapter) Live(context.Context, messenger.SourceID, func(messenger.Event) error) error {
	return nil
}
func (a *fakeAdapter) FetchAttachment(context.Context, messenger.SourceID, string, string) (io.ReadCloser, error) {
	return nil, errors.New("no attachments")
}
func (a *fakeAdapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{Edits: true, Deletions: true}
}

// A page with more than one id must still match an effect by the platform's
// message id: the two IN lists take the same ids in the same order.
func TestEffectMatchesByPlatformMessageID(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	effect := messenger.Effect{ID: "effect-1", Platform: source.Platform, Account: source.Account, Chat: "dm-1",
		Agent: "agent:main", Verb: "send", ClientID: "client-1", Message: "m-1", State: messenger.EffectSent, CreatedAt: f.now}
	if err := f.store.RecordEffect(t.Context(), effect); err != nil {
		t.Fatal(err)
	}
	page := messenger.Page{Messages: []messenger.Message{
		message("m-1", messenger.RoutePerson, f.now, "sent by LINA"),
		message("m-2", messenger.RoutePerson, f.now.Add(time.Minute), "typed by the person"),
	}, Next: "c2"}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
		t.Fatal(err)
	}
	if event := f.eventsFor(t, source, "dm-1", "m-1"); event.SpeakerKind != guard.Agent || event.AgentID != "agent:main" || event.PersonID != "" {
		t.Fatalf("effect-matched message: %+v", event)
	}
	if event := f.eventsFor(t, source, "dm-1", "m-2"); event.SpeakerKind != guard.Person || event.PersonID != "person" {
		t.Fatalf("the person's own message: %+v", event)
	}
}

// An own-account message waits while any write in its chat is unsettled: the
// platform need not identify a sent message with the effect's client id.
func TestOwnAccountMessageWaitsForTheChatsUnsettledWrite(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	effect := messenger.Effect{ID: "effect-1", Platform: source.Platform, Account: source.Account, Chat: "dm-1",
		Agent: "agent:main", Verb: "send", ClientID: "client-1", State: messenger.EffectPending, CreatedAt: f.now}
	if err := f.store.RecordEffect(t.Context(), effect); err != nil {
		t.Fatal(err)
	}
	page := messenger.Page{Messages: []messenger.Message{message("m-2", messenger.RoutePerson, f.now, "typed by the person")}, Next: "c2"}
	result, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page)
	if !errors.Is(err, messenger.ErrPageWaiting) || result.Waiting != 1 {
		t.Fatalf("unsettled chat write did not hold the page: %+v %v", result, err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%'"); n != 0 {
		t.Fatal("a waiting page admitted a record")
	}
	// The write settles with a platform id of its own: m-2 is not it.
	if err := f.store.SettleEffect(t.Context(), "effect-1", messenger.EffectSent, "platform-9"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
		t.Fatal(err)
	}
	if event := f.eventsFor(t, source, "dm-1", "m-2"); event.SpeakerKind != guard.Person || event.PersonID != "person" {
		t.Fatalf("settled chat: %+v", event)
	}
}

// cursorSpy records whether the chat's cursor moved before a deletion was
// journaled.
type cursorSpy struct {
	db    *canon.Store
	t     *testing.T
	from  string
	calls int
}

func (s *cursorSpy) Forget(ctx context.Context, _ retract.Request) error {
	s.calls++
	var cursor string
	if err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT cursor FROM messenger_chat WHERE platform=? AND account=? AND chat=?", "discord", "account-1", "dm-1").Scan(&cursor)
	}); err != nil {
		return err
	}
	if cursor != s.from {
		s.t.Errorf("the cursor advanced before the deletion: %q, want %q", cursor, s.from)
	}
	return nil
}

// A page's deletions are durable before its cursor advances, so a crash after
// the progress commit cannot skip them.
func TestDeletionIsAppliedBeforeTheCursorAdvances(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
		Messages: []messenger.Message{message("m-1", messenger.RouteParticipant, f.now, "delete me")}, Next: "c1",
	}); err != nil {
		t.Fatal(err)
	}
	spy := &cursorSpy{db: f.db, t: t, from: "c1"}
	store := messenger.Open(f.db, nil, spy).WithClock(func() time.Time { return f.now })
	result, err := store.AdmitPage(t.Context(), source, "dm-1", "c1", messenger.Page{
		Messages:  []messenger.Message{message("m-2", messenger.RouteParticipant, f.now.Add(time.Minute), "kept")},
		Deletions: []messenger.Deletion{{Message: "m-1", At: f.now.Add(time.Minute)}},
		Next:      "c2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 || result.Deleted != 1 || result.Added != 1 {
		t.Fatalf("deletion calls=%d result=%+v", spy.calls, result)
	}
}

// Permanent deletion removes the text and the material reference; the row keeps
// its identity, origin and the legacy reference the tombstone carries.
func TestPermanentDeletionErasesTheText(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
		Messages: []messenger.Message{message("m-1", messenger.RouteParticipant, f.now, "erase this text")}, Next: "c1",
	}); err != nil {
		t.Fatal(err)
	}
	event := f.eventsFor(t, source, "dm-1", "m-1")
	ref := retract.Ref{Kind: retract.Event, ID: event.ID}
	request := retract.Request{Person: "person", Targets: []retract.Ref{ref}}
	if err := f.removal.Forget(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := f.removal.Delete(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	var body, material, origin string
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT body,coalesce(material_id,''),origin FROM conv_event WHERE id=?", event.ID).Scan(&body, &material, &origin)
	}); err != nil {
		t.Fatal(err)
	}
	if body != "" || material != "" {
		t.Fatalf("payload survived: body=%q material=%q", body, material)
	}
	if !strings.Contains(origin, "messenger") {
		t.Fatalf("origin lost: %s", origin)
	}
	got, err := f.events.Get(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body.Text() != "" {
		t.Fatalf("the ledger still returns %q", got.Body.Text())
	}
}

// Two sources on one platform, such as a Discord bot and a personal account,
// must read through the adapter each source records. Sharing the platform
// adapter would write one account's chats under the other account.
func TestSourcesOnOnePlatformUseTheirOwnAdapter(t *testing.T) {
	f := newFixture(t)
	bot := f.source()
	bot.Account = "bot"
	bot.Adapter = "discord-bot"
	personal := f.source()
	personal.Account = "personal"
	personal.Mode = messenger.ModeUnofficial
	personal.Adapter = "discord-personal"
	personal.AcceptedTerms = &messenger.Terms{Person: "person", At: f.now, Text: "terms forbid automating an account"}
	for _, source := range []messenger.Source{bot, personal} {
		if err := f.store.Connect(t.Context(), source); err != nil {
			t.Fatal(err)
		}
	}
	botAdapter := &fakeAdapter{chats: []messenger.Chat{{ID: "server-1", Kind: "channel"}}, pages: map[string][]messenger.Page{
		"": {{Messages: []messenger.Message{message("bot-m", messenger.RouteAutomation, f.now, "from the bot")}, Next: "end"}},
	}}
	personalAdapter := &fakeAdapter{chats: []messenger.Chat{{ID: "dm-personal", Kind: "direct"}}, pages: map[string][]messenger.Page{
		"": {{Messages: []messenger.Message{message("personal-m", messenger.RoutePerson, f.now, "from the person")}, Next: "end"}},
	}}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{
		"discord-bot": botAdapter, "discord-personal": personalAdapter,
	}, time.Hour)
	if _, err := service.Sync(t.Context(), bot); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sync(t.Context(), personal); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE account='bot' AND message='bot-m'"); n != 1 {
		t.Fatalf("bot records = %d", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE account='personal' AND message='personal-m'"); n != 1 {
		t.Fatalf("personal records = %d", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE account='bot' AND message='personal-m'"); n != 0 {
		t.Fatal("the personal message was stored on the bot account")
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_chat WHERE account='personal' AND chat='server-1'"); n != 0 {
		t.Fatal("the bot chat was stored on the personal account")
	}
}

// One adapter implementation may serve several accounts. Each call names the
// account, and the records stay under that account.
func TestAdapterCallsNameTheSourceAccount(t *testing.T) {
	f := newFixture(t)
	first := f.source()
	first.Account = "account-a"
	second := f.source()
	second.Account = "account-b"
	for _, source := range []messenger.Source{first, second} {
		if err := f.store.Connect(t.Context(), source); err != nil {
			t.Fatal(err)
		}
	}
	adapter := &accountAdapter{pages: map[string]messenger.Message{
		"account-a": message("m-a", messenger.RoutePerson, f.now, "alpha"),
		"account-b": message("m-b", messenger.RoutePerson, f.now, "beta"),
	}}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	results := service.SyncAll(t.Context(), &f.now)
	if len(results) != 2 {
		t.Fatalf("synced %d sources: %+v", len(results), results)
	}
	if len(adapter.seen) != 2 || adapter.seen[0].Account == adapter.seen[1].Account {
		t.Fatalf("accounts seen: %+v", adapter.seen)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE account='account-a' AND message='m-a'"); n != 1 {
		t.Fatalf("account-a records = %d", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE account='account-b' AND message='m-b'"); n != 1 {
		t.Fatalf("account-b records = %d", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE account='account-a' AND message='m-b'"); n != 0 {
		t.Fatal("account-b's message was stored on account-a")
	}
}

// accountAdapter returns a different message per source account and records
// every account it was asked about.
type accountAdapter struct {
	pages map[string]messenger.Message
	seen  []messenger.SourceID
}

func (a *accountAdapter) SignIn(_ context.Context, id messenger.SourceID) (messenger.SignInState, error) {
	a.seen = append(a.seen, id)
	return messenger.SignedIn, nil
}
func (a *accountAdapter) ListChats(_ context.Context, id messenger.SourceID) ([]messenger.Chat, error) {
	return []messenger.Chat{{ID: "dm-" + id.Account, Kind: "direct"}}, nil
}
func (a *accountAdapter) Page(_ context.Context, id messenger.SourceID, _ string, _ messenger.Cursor) (messenger.Page, error) {
	return messenger.Page{Messages: []messenger.Message{a.pages[id.Account]}, Next: "end"}, nil
}
func (a *accountAdapter) Members(context.Context, messenger.SourceID, string) ([]messenger.Author, error) {
	return nil, nil
}
func (a *accountAdapter) Live(context.Context, messenger.SourceID, func(messenger.Event) error) error {
	return nil
}
func (a *accountAdapter) FetchAttachment(context.Context, messenger.SourceID, string, string) (io.ReadCloser, error) {
	return nil, errors.New("no attachments")
}
func (a *accountAdapter) Capabilities() messenger.Capabilities { return messenger.Capabilities{} }

// Revocation can be discovered after SignIn, during any authenticated read.
type revokedReadAdapter struct {
	fakeAdapter
	stage string
}

func (a *revokedReadAdapter) ListChats(ctx context.Context, id messenger.SourceID) ([]messenger.Chat, error) {
	if a.stage == "chats" {
		return nil, messenger.ErrNeedsSignIn
	}
	return []messenger.Chat{{ID: "dm-1", Kind: "direct"}}, nil
}
func (a *revokedReadAdapter) Page(context.Context, messenger.SourceID, string, messenger.Cursor) (messenger.Page, error) {
	return messenger.Page{}, messenger.ErrNeedsSignIn
}
func TestRevokedReadNeedsSignInWithoutMovingCursor(t *testing.T) {
	for _, stage := range []string{"chats", "page"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t)
			f.connect(t)
			service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": &revokedReadAdapter{stage: stage}}, time.Minute)
			service.SyncAll(t.Context(), &f.now)
			sources, err := f.store.Sources(t.Context(), "person")
			if err != nil || len(sources) != 1 || sources[0].State != messenger.StateNeedsSignIn {
				t.Fatalf("state: %+v %v", sources, err)
			}
			chats, err := f.store.Chats(t.Context(), "person", "discord", "account-1")
			if err != nil || len(chats) != 1 || chats[0].Cursor != "" {
				t.Fatalf("cursor changed: %+v %v", chats, err)
			}
		})
	}
}
func TestTransientSourceFailureRetriesOnItsNextInterval(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	adapter := &fakeAdapter{fail: errors.New("temporary outage")}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Minute)
	service.SyncAll(t.Context(), &f.now)
	adapter.fail = nil
	f.now = f.now.Add(time.Minute)
	service.SyncAll(t.Context(), &f.now)
	rows, err := f.store.Sources(t.Context(), "person")
	if err != nil || rows[0].State != messenger.StateConnected || rows[0].Failure != "" {
		t.Fatalf("source did not recover: %+v %v", rows, err)
	}
}

func TestScheduledReaderLeavesUnownedSourcesUntouched(t *testing.T) {
	f := newFixture(t)
	owned := f.connect(t)
	other := owned
	other.Platform = "teams"
	other.Adapter = "teams"
	other.Account = "work"
	if err := f.store.Connect(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": &fakeAdapter{}}, time.Minute)
	service.SyncAll(t.Context(), &f.now)
	rows, err := f.store.Sources(t.Context(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("sources: %+v", rows)
	}
	for _, row := range rows {
		if row.State != messenger.StateConnected || row.Failure != "" {
			t.Fatalf("reader changed another source: %+v", row)
		}
	}
}

type blockedSyncAdapter struct {
	fakeAdapter
	started chan struct{}
}

func (a *blockedSyncAdapter) ListChats(ctx context.Context, _ messenger.SourceID) ([]messenger.Chat, error) {
	close(a.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestDisconnectCancelsActiveSync(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	adapter := &blockedSyncAdapter{started: make(chan struct{})}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	synced := make(chan struct{})
	go func() { defer close(synced); service.SyncAll(ctx, &f.now) }()
	<-adapter.started
	disconnected := make(chan error, 1)
	go func() { disconnected <- service.Disconnect(t.Context(), source) }()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-disconnected:
		if err != nil {
			t.Fatal(err)
		}
	case <-timer.C:
		cancel()
		<-synced
		<-disconnected
		t.Fatal("disconnect waited for active sync instead of cancelling it")
	}
	<-synced
	sources, err := f.store.Sources(t.Context(), source.Person)
	if err != nil || len(sources) != 1 || sources[0].State != messenger.StateDisconnected {
		t.Fatalf("cancelled sync changed disconnected state: %+v %v", sources, err)
	}
	if _, err := service.Sync(t.Context(), source); err != nil {
		t.Fatal(err)
	}
}
