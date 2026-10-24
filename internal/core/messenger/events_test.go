// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// A message the platform pushed is a record, not a read: the chat's cursor and
// coverage stay where the last page put them.
func TestPushedMessageKeepsTheCursor(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	page := messenger.Page{Messages: []messenger.Message{message("m-1", messenger.RouteParticipant, f.now, "one")}, Next: "cursor-2"}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
		t.Fatal(err)
	}
	coverage := count(t, f.db, "SELECT count(*) FROM messenger_coverage WHERE chat='dm-1'")

	edit := message("m-1", messenger.RouteParticipant, f.now, "one, edited")
	edit.Edited = f.now.Add(time.Minute)
	result, err := f.store.AdmitMessages(t.Context(), source, "dm-1", []messenger.Message{edit})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 0 || result.Updated != 1 {
		t.Fatalf("pushed edit: %+v", result)
	}
	chats, err := f.store.Chats(t.Context(), "person", "discord", "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Cursor != "cursor-2" {
		t.Fatalf("cursor after a push: %+v", chats)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_coverage WHERE chat='dm-1'"); n != coverage {
		t.Fatalf("a push recorded coverage: %d -> %d", coverage, n)
	}
	// The edit supersedes, and re-admitting the same revision adds nothing.
	if n := count(t, f.db, "SELECT count(*) FROM ev_rel WHERE kind='supersedes'"); n != 1 {
		t.Fatalf("supersession relations = %d", n)
	}
	if _, err := f.store.AdmitMessages(t.Context(), source, "dm-1", []messenger.Message{edit}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record_revision WHERE message='m-1'"); n != 2 {
		t.Fatalf("revisions = %d, want the original and one edit", n)
	}
}

// A deletion the platform pushed without a chat clears the id in every chat
// that holds it, and leaves the other ids alone.
func TestPushedDeletionWithoutAChatClearsEveryChat(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if err := f.store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "group-1", Name: "Group", Kind: "group"}}); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []string{"dm-1", "group-1"} {
		page := messenger.Page{Messages: []messenger.Message{
			message("7", messenger.RouteParticipant, f.now, "seven in "+chat),
			message("8", messenger.RouteParticipant, f.now.Add(time.Minute), "eight in "+chat),
		}, Next: messenger.Cursor("cursor-" + chat)}
		if _, err := f.store.AdmitPage(t.Context(), source, chat, "", page); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := f.store.DeleteSourceMessages(t.Context(), source, []string{"7"})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted %d records, want one in each chat", deleted)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE message='7' AND state='deleted'"); n != 2 {
		t.Fatalf("tombstones = %d", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE message='8' AND state='live'"); n != 2 {
		t.Fatalf("untouched records = %d, want 2", n)
	}
	if n := count(t, f.db, "SELECT count(*) FROM removal_applied"); n != 2 {
		t.Fatalf("removal rows = %d", n)
	}
}

// Service.Apply routes a pushed event by its kind.
func TestApplyAdmitsAnEditAndADeletion(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	page := messenger.Page{Messages: []messenger.Message{
		message("1", messenger.RouteParticipant, f.now, "first"),
	}, Next: "cursor-2"}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
		t.Fatal(err)
	}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": &fakeAdapter{}}, time.Hour)

	edit := message("1", messenger.RouteParticipant, f.now, "first, edited")
	edit.Edited = f.now.Add(time.Minute)
	result, err := service.Apply(t.Context(), source, messenger.Event{Chat: "dm-1", Message: &edit})
	if err != nil || result.Updated != 1 {
		t.Fatalf("apply edit: %+v %v", result, err)
	}
	result, err = service.Apply(t.Context(), source, messenger.Event{Deletion: &messenger.Deletion{Message: "1", At: f.now}})
	if err != nil || result.Deleted != 1 {
		t.Fatalf("apply deletion: %+v %v", result, err)
	}
	if _, err := service.Apply(t.Context(), source, messenger.Event{Message: &edit}); err == nil {
		t.Fatal("an event without a chat was accepted")
	}
}
