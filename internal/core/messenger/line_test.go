// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
)

// newestNAdapter stands in for LINE: it serves no durable cursor and answers
// every read with the same newest messages, in the platform's newest-first
// order.
type newestNAdapter struct {
	chats    []messenger.Chat
	messages []messenger.Message
	revoked  bool
}

func (a *newestNAdapter) SignIn(context.Context, messenger.SourceID) (messenger.SignInState, error) {
	if a.revoked {
		return messenger.NeedsSignIn, nil
	}
	return messenger.SignedIn, nil
}

func (a *newestNAdapter) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	return a.chats, nil
}

func (a *newestNAdapter) Page(context.Context, messenger.SourceID, string, messenger.Cursor) (messenger.Page, error) {
	page := messenger.Page{Messages: a.messages, Next: ""}
	if len(a.messages) > 0 {
		page.Through = a.messages[0].At
	}
	return page, nil
}

func (a *newestNAdapter) Members(context.Context, messenger.SourceID, string) ([]messenger.Author, error) {
	return nil, nil
}
func (a *newestNAdapter) Live(context.Context, messenger.SourceID, func(messenger.Event) error) error {
	return nil
}
func (a *newestNAdapter) FetchAttachment(context.Context, messenger.SourceID, string, string) (io.ReadCloser, error) {
	return nil, errors.New("no attachments")
}
func (a *newestNAdapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{HistoryDepth: "newest-N", CursorKind: "none", LiveEvents: true, Interval: time.Minute}
}

func (f *fixture) text(t *testing.T, query string, args ...any) string {
	t.Helper()
	var value string
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), query, args...).Scan(&value)
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

// Two overlapping newest-N reads store each message once: the platform's
// repeated page adds nothing, and the coverage record keeps saying that only
// the newest messages were read and no cursor survives a restart.
func TestOverlappingNewestNReadsStoreEachMessageOnce(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	newest := []messenger.Message{
		message("m-2", messenger.RouteParticipant, f.now.Add(time.Minute), "second"),
		message("m-1", messenger.RoutePerson, f.now, "first"),
	}
	adapter := &newestNAdapter{chats: []messenger.Chat{{ID: "dm-1", Kind: "direct"}}, messages: newest}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Minute)

	for i := range 2 {
		if _, err := service.Sync(t.Context(), source); err != nil {
			t.Fatalf("read %d: %v", i+1, err)
		}
	}
	if got := count(t, f.db, "SELECT count(*) FROM messenger_record"); got != 2 {
		t.Fatalf("records = %d, want 2", got)
	}
	for _, id := range []string{"m-1", "m-2"} {
		if got := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE message=?", id); got != 1 {
			t.Fatalf("record %s stored %d times", id, got)
		}
	}
	if got := f.text(t, "SELECT history_depth FROM messenger_coverage WHERE chat='dm-1'"); got != "newest-N" {
		t.Fatalf("history depth = %q", got)
	}
	if got := count(t, f.db, "SELECT count(*) FROM messenger_coverage WHERE chat='dm-1' AND cursor_survives_restart=0"); got != 1 {
		t.Fatalf("coverage does not say the cursor dies with the process: %d rows", got)
	}
}

// A message the platform served but this account could not decrypt stays a
// record with the flag and no text.
func TestDecryptionFailureKeepsARecordWithTheFlag(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	failed := message("m-2", messenger.RouteParticipant, f.now.Add(time.Minute), "")
	failed.DecryptionFailed = true
	page := messenger.Page{Messages: []messenger.Message{
		message("m-1", messenger.RoutePerson, f.now, "readable"),
		failed,
	}, Next: "cursor-2"}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); err != nil {
		t.Fatal(err)
	}
	if got := count(t, f.db, "SELECT decryption_failed FROM messenger_record WHERE message='m-2'"); got != 1 {
		t.Fatalf("decryption flag = %d, want 1", got)
	}
	if got := count(t, f.db, "SELECT decryption_failed FROM messenger_record WHERE message='m-1'"); got != 0 {
		t.Fatalf("readable message flagged")
	}
	if event := f.eventsFor(t, source, "dm-1", "m-2"); event.Body.Text() != "" {
		t.Fatalf("flagged record carries text %q", event.Body.Text())
	}
	// The flag and text together would read as an empty message with a claim
	// about it; the adapter must never send both.
	page.Messages = []messenger.Message{{ID: "m-3", Route: messenger.RouteParticipant, At: f.now, Text: "invented", DecryptionFailed: true}}
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", page); !errors.Is(err, messenger.ErrInvalid) {
		t.Fatalf("flagged message with text = %v, want invalid", err)
	}
}

// A session the platform revoked needs sign-in and reads nothing new.
func TestRevokedNewestNSessionNeedsSignIn(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	adapter := &newestNAdapter{chats: []messenger.Chat{{ID: "dm-1", Kind: "direct"}}, revoked: true}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Minute)
	if _, err := service.Sync(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if got := f.text(t, "SELECT state FROM messenger_source WHERE platform='discord'"); got != "needs_sign_in" {
		t.Fatalf("source state = %q", got)
	}
	if got := count(t, f.db, "SELECT count(*) FROM messenger_record"); got != 0 {
		t.Fatalf("revoked session stored %d records", got)
	}
	if got := f.text(t, "SELECT cursor FROM messenger_chat"); got != "" {
		t.Fatalf("revoked session moved the cursor to %q", got)
	}
}
