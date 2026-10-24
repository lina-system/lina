// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/retract"
)

// Attachment metadata commits with the record and its cursor, survives a second
// read of the same page, and is deleted with the record's text.
func TestAttachmentMetadataIsStoredWithTheRecord(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	page := messenger.Page{Messages: []messenger.Message{{
		ID: "m-1", Route: messenger.RouteParticipant, At: f.now, Text: "",
		Attachments: []messenger.Attachment{
			{ID: "a-1", Name: "report.pdf", MIMEType: "application/pdf", Size: 1200},
			{ID: "a-2", Name: "photo.png", MIMEType: "image/png", Size: 48},
		},
	}}, Next: "c1"}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
		t.Fatal(err)
	}
	chats, err := f.store.Chats(t.Context(), "person", "discord", "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Cursor != "c1" {
		t.Fatalf("cursor: %+v", chats)
	}
	type stored struct {
		attachment, name, mime string
		size                   int64
	}
	var got []stored
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(t.Context(), `SELECT attachment,name,mime,size FROM messenger_attachment
WHERE platform=? AND account=? AND chat=? AND message=? ORDER BY ordinal`, source.Platform, source.Account, "dm-1", "m-1")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row stored
			if err := rows.Scan(&row.attachment, &row.name, &row.mime, &row.size); err != nil {
				return err
			}
			got = append(got, row)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != (stored{"a-1", "report.pdf", "application/pdf", 1200}) || got[1] != (stored{"a-2", "photo.png", "image/png", 48}) {
		t.Fatalf("stored attachments: %+v", got)
	}
	// Reading the page again adds nothing and keeps the metadata.
	again, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c1", page)
	if err != nil || again.Added != 0 {
		t.Fatalf("second read: %+v %v", again, err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_attachment"); n != 2 {
		t.Fatalf("attachments after a second read = %d", n)
	}
	// The record's permanent deletion deletes its attachment metadata.
	event := f.eventsFor(t, source, "dm-1", "m-1")
	request := retract.Request{Person: "person", Targets: []retract.Ref{{Kind: retract.Event, ID: event.ID}}}
	if err := f.removal.Forget(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := f.removal.Delete(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_attachment"); n != 0 {
		t.Fatalf("attachments after erasure = %d", n)
	}
}

// An edit carries its own attachments: the earlier revision keeps the metadata
// it arrived with, and the newest event has the new set.
func TestAttachmentFollowsEachRevision(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	original := message("m-1", messenger.RouteParticipant, f.now, "first")
	original.Attachments = []messenger.Attachment{{ID: "a-1", Name: "before.pdf", Size: 10}}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
		Messages: []messenger.Message{original}, Next: "c1",
	}); err != nil {
		t.Fatal(err)
	}
	edited := original
	edited.Text = "second"
	edited.Edited = f.now.Add(time.Minute)
	edited.Attachments = []messenger.Attachment{{ID: "a-2", Name: "after.pdf", Size: 20}}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "c1", messenger.Page{
		Messages: []messenger.Message{edited}, Next: "c2",
	}); err != nil {
		t.Fatal(err)
	}
	newest := f.eventsFor(t, source, "dm-1", "m-1")
	var current, earlier int
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM messenger_attachment WHERE event=? AND attachment='a-2'", newest.ID).Scan(&current); err != nil {
			return err
		}
		target := ""
		for _, relation := range newest.Relations {
			if relation.Kind == "supersedes" {
				target = relation.TargetID
			}
		}
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM messenger_attachment WHERE event=? AND attachment='a-1'", target).Scan(&earlier)
	}); err != nil {
		t.Fatal(err)
	}
	if current != 1 || earlier != 1 {
		t.Fatalf("attachments per revision: newest %d, earlier %d", current, earlier)
	}
}

// An attachment without a platform id cannot be fetched later, so the page is
// refused rather than stored incomplete.
func TestAttachmentWithoutAnIdIsRefused(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	_, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{{
		ID: "m-1", Route: messenger.RoutePerson, At: f.now, Text: "see attached",
		Attachments: []messenger.Attachment{{Name: "unnamed"}},
	}}, Next: "c1"})
	if err == nil {
		t.Fatal("a page with an unidentified attachment was admitted")
	}
}
