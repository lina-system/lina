// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// goneAdapter can no longer read one of its chats, as after the account left a
// channel.
type goneAdapter struct{ fakeAdapter }

func (a *goneAdapter) Page(ctx context.Context, id messenger.SourceID, chat string, cursor messenger.Cursor) (messenger.Page, error) {
	if chat == "gone" {
		return messenger.Page{}, messenger.ErrChatUnavailable
	}
	return a.fakeAdapter.Page(ctx, id, chat, cursor)
}

func TestUnreadableChatDoesNotStopTheSource(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if err := f.store.UpsertChats(t.Context(), source, []messenger.Chat{{ID: "gone", Name: "Left", Kind: "channel"}}); err != nil {
		t.Fatal(err)
	}
	adapter := &goneAdapter{fakeAdapter{pages: map[string][]messenger.Page{"": {{Messages: []messenger.Message{message("m-1", messenger.RouteParticipant, f.now, "still read")}, Next: "c1"}}}}}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	if _, err := service.Sync(t.Context(), source); err != nil {
		t.Fatalf("an unreadable chat stopped the source: %v", err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event WHERE body='still read'"); n != 1 {
		t.Fatal("the readable chat was not read")
	}
	sources, err := f.store.Sources(t.Context(), "person")
	if err != nil || len(sources) != 1 || sources[0].State != messenger.StateConnected {
		t.Fatalf("source after sync: %+v %v", sources, err)
	}
}

// attachmentAdapter records which record an attachment fetch names.
type attachmentAdapter struct {
	fakeAdapter
	asked []string
}

func (a *attachmentAdapter) FetchAttachment(_ context.Context, _ messenger.SourceID, chat, message, attachment string) (io.ReadCloser, error) {
	a.asked = append(a.asked, chat+"/"+message+"/"+attachment)
	return io.NopCloser(strings.NewReader("bytes")), nil
}

// A fetch after a restart names the record the attachment came from, so the
// adapter can find it again without its in-memory location.
func TestFetchAttachmentNamesItsRecord(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	withFile := message("m-1", messenger.RouteParticipant, f.now, "with a file")
	withFile.Attachments = []messenger.Attachment{{ID: "att-1", Name: "notes.txt", MIMEType: "text/plain", Size: 5}}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{withFile}, Next: "c1"}); err != nil {
		t.Fatal(err)
	}
	adapter := &attachmentAdapter{}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	reader, err := service.FetchAttachment(t.Context(), source, "dm-1", "att-1")
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if len(adapter.asked) != 1 || adapter.asked[0] != "dm-1/m-1/att-1" {
		t.Fatalf("adapter asked for %v", adapter.asked)
	}
	if _, err := service.FetchAttachment(t.Context(), source, "dm-1", "att-404"); !errors.Is(err, messenger.ErrNotFound) {
		t.Fatalf("unknown attachment: %v", err)
	}
	if _, err := f.store.DeleteRecords(t.Context(), source, "dm-1", []string{"m-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FetchAttachment(t.Context(), source, "dm-1", "att-1"); !errors.Is(err, messenger.ErrNotFound) {
		t.Fatalf("deleted record's attachment: %v", err)
	}
	if len(adapter.asked) != 1 {
		t.Fatalf("adapter asked for %v", adapter.asked)
	}
}
