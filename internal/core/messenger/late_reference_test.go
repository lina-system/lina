// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// A backward first fill admits a reply on a newer page before its parent. The
// relation is added when the parent's page commits.
func TestReplyOnNewerPageLinksWhenParentArrives(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	reply := message("20", messenger.RouteParticipant, f.now.Add(time.Hour), "answer")
	reply.ReplyTo, reply.Thread = "10", "10"
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{reply}, Next: "c1", HasMore: true}); err != nil {
		t.Fatal(err)
	}
	if event := f.eventsFor(t, source, "dm-1", "20"); len(event.Relations) != 0 {
		t.Fatalf("relation to a missing parent: %+v", event.Relations)
	}
	parent := message("10", messenger.RouteParticipant, f.now, "question")
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c1", messenger.Page{Messages: []messenger.Message{parent}, Next: "c2"}); err != nil {
		t.Fatal(err)
	}
	parentEvent := f.eventsFor(t, source, "dm-1", "10")
	replyEvent := f.eventsFor(t, source, "dm-1", "20")
	if !relationTargets(replyEvent, "replies_to", parentEvent.ID) || !relationTargets(replyEvent, "in_thread", parentEvent.ID) {
		t.Fatalf("reply not linked after its parent arrived: %+v", replyEvent.Relations)
	}
	// Reading both pages again adds no second relation.
	for _, page := range []messenger.Page{{Messages: []messenger.Message{reply}, Next: "c1", HasMore: true}, {Messages: []messenger.Message{parent}, Next: "c2"}} {
		if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, f.db, "SELECT count(*) FROM ev_rel WHERE event_id=?", replyEvent.ID); n != 2 {
		t.Fatalf("reply relations = %d, want 2", n)
	}
}

// A reply deleted before its parent arrives gains no relation.
func TestDeletedReplyIsNotLinkedLater(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	reply := message("20", messenger.RouteParticipant, f.now.Add(time.Hour), "answer")
	reply.ReplyTo = "10"
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{reply}, Next: "c1", HasMore: true}); err != nil {
		t.Fatal(err)
	}
	replyEvent := f.eventsFor(t, source, "dm-1", "20")
	if _, err := f.store.DeleteRecords(t.Context(), source, "dm-1", []string{"20"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c1", messenger.Page{Messages: []messenger.Message{message("10", messenger.RouteParticipant, f.now, "question")}, Next: "c2"}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM ev_rel WHERE event_id=?", replyEvent.ID); n != 0 {
		t.Fatalf("deleted reply gained %d relations", n)
	}
}

// A deletion that names no chat can arrive before the page holding that message
// is admitted. The page must not bring the deleted content in.
func TestChatlessDeletionBeforeAdmissionKeepsContentOut(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if err := f.store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "server-1", Name: "Server", Kind: "channel"}}); err != nil {
		t.Fatal(err)
	}
	deleted, err := f.store.AdmitEvent(t.Context(), source, messenger.Event{Deletion: &messenger.Deletion{Message: "m-9", At: f.now}, ChatPrefixes: []string{"dm-"}})
	if err != nil || deleted.Deleted != 0 {
		t.Fatalf("unmatched deletion: %+v %v", deleted, err)
	}
	result, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{
		message("m-9", messenger.RouteParticipant, f.now, "deleted before admission"),
		message("m-10", messenger.RouteParticipant, f.now, "kept"),
	}, Next: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 {
		t.Fatalf("admitted %d records, want only the kept one", result.Added)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event WHERE body='deleted before admission'"); n != 0 {
		t.Fatal("deleted content entered the ledger")
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE chat='dm-1' AND message='m-9' AND state='deleted'"); n != 1 {
		t.Fatal("no tombstone for the deleted message")
	}
	// The deletion was scoped to direct chats: a channel's own id sequence keeps
	// a message with the same id.
	if _, err := f.store.AdmitPage(t.Context(), source, "server-1", "", messenger.Page{Messages: []messenger.Message{
		message("m-9", messenger.RouteParticipant, f.now, "another sequence"),
	}, Next: "c1"}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event WHERE body='another sequence'"); n != 1 {
		t.Fatal("deletion crossed into a chat outside its scope")
	}
}
