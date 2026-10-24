// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/kit/secret"
)

// TestM1MessengerStoreInstalledCore proves the messenger store inside a running
// Core: a fake adapter's pages become ledger records, one index documents,
// cursor and coverage; the second start resumes from the committed cursor after
// an interrupted read and adds nothing twice; an edit supersedes, a
// platform-reported deletion hides the record, and a URL secret is masked.
func TestM1MessengerStoreInstalledCore(t *testing.T) {
	t.Parallel()

	h := newHarness(t, "")
	installMessengerBundle(t, h)

	start := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	text := "join https://example.com/cb?token=synthetic-abc&next=/home"
	source := messenger.Source{Person: "person:messenger-host", Platform: "fixture", Account: "account-1",
		Mode: messenger.ModeOfficial, Adapter: "fixture", State: messenger.StateConnected, ConnectedAt: start}
	// Connecting a source is the person's act; Core never connects one itself,
	// so the registry row exists before Core starts.
	db, closeStore := installedAcceptanceStore(t, h)
	if err := messenger.Open(db, nil, nil).Connect(h.ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := messenger.Open(db, nil, nil).UpsertChats(h.ctx, source, []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	closeStore()

	first := &scriptedAdapter{pages: map[string][]scriptedPage{
		"": {{page: messenger.Page{
			Messages: []messenger.Message{
				message("m-1", messenger.RoutePerson, start, text),
				message("m-2", messenger.RouteParticipant, start.Add(time.Minute), "delete me"),
			},
			Next: "c1", HasMore: true}}},
		"c1": {{block: true}},
	}}
	stop := runMessengerCore(t, h, first)
	first.waitFor(t, "c1")
	stop()

	second := &scriptedAdapter{pages: map[string][]scriptedPage{
		"c1": {{page: messenger.Page{
			Messages: []messenger.Message{
				message("m-3", messenger.RouteParticipant, start.Add(2*time.Minute), "third"),
				edited("m-1", messenger.RoutePerson, start, text+" (edited)", start.Add(3*time.Minute)),
			},
			Deletions: []messenger.Deletion{{Message: "m-2", At: start.Add(3 * time.Minute)}},
			Next:      "c2",
		}}},
		"c2": {{page: messenger.Page{Next: "c2"}}},
	}}
	stop = runMessengerCore(t, h, second)
	second.waitFor(t, "c2")
	stop()

	// A later run re-serving the newest page adds nothing and starts at the
	// committed cursor.
	third := &scriptedAdapter{pages: map[string][]scriptedPage{
		"c2": {{page: messenger.Page{
			Messages: []messenger.Message{message("m-3", messenger.RouteParticipant, start.Add(2*time.Minute), "third")},
			Next:     "c2"}}},
	}}
	stop = runMessengerCore(t, h, third)
	third.waitFor(t, "c2")
	stop()

	if first.requested[0] != "" {
		t.Fatalf("first read cursor = %q", first.requested[0])
	}
	if len(second.requested) == 0 || second.requested[0] != "c1" {
		t.Fatalf("resume cursor = %v, want c1", second.requested)
	}
	if len(third.requested) == 0 || third.requested[0] != "c2" {
		t.Fatalf("later read cursor = %v, want c2", third.requested)
	}

	db, closeStore = installedAcceptanceStore(t, h)
	defer closeStore()
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
	// m-1 was read twice as an edit, so four events exist and the earlier text
	// stays as history; the re-read of m-3 added nothing.
	if n := count("SELECT count(*) FROM conv_event WHERE origin LIKE '%messenger%'"); n != 4 {
		t.Fatalf("records = %d, want 4", n)
	}
	if n := count(`SELECT count(*) FROM conv_event WHERE body LIKE '%(edited)%' AND speaker_kind='person'`); n != 1 {
		t.Fatal("the edit was not admitted as the person's record")
	}
	if n := count(`SELECT count(*) FROM ev_rel WHERE kind='supersedes'`); n != 1 {
		t.Fatal("the edit did not supersede the earlier record")
	}
	// The platform deletion hid m-2 at once and kept its reference.
	if n := count("SELECT count(*) FROM retract_visible_event WHERE body='delete me'"); n != 0 {
		t.Fatal("a deleted record stayed visible")
	}
	if n := count("SELECT count(*) FROM messenger_record WHERE message='m-2' AND state='deleted'"); n != 1 {
		t.Fatal("the deleted record lost its legacy reference")
	}
	if n := count("SELECT count(*) FROM messenger_coverage WHERE chat='dm-1'"); n < 2 {
		t.Fatalf("coverage rows = %d, want a record per complete read", n)
	}
	if n := count("SELECT count(*) FROM messenger_coverage WHERE chat='dm-1' AND messages>0"); n != 1 {
		t.Fatalf("content coverage rows = %d, want 1", n)
	}
	if n := count("SELECT count(*) FROM messenger_chat WHERE chat='dm-1' AND cursor='c2'"); n != 1 {
		t.Fatal("the cursor was not committed")
	}
	// Records are documents of the one index, and the URL secret is masked.
	if n := count(`SELECT count(*) FROM recall_doc WHERE kind='event' AND json_extract(origin,'$.kind')='messenger'`); n != 3 {
		t.Fatalf("indexed records = %d, want 3", n)
	}
	if n := count("SELECT count(*) FROM conv_event WHERE body LIKE '%synthetic-abc%'"); n != 0 {
		t.Fatal("an unmasked URL secret reached the ledger")
	}
	if n := count("SELECT count(*) FROM conv_event WHERE body LIKE '%next=/home%' AND body LIKE '%" + secret.Placeholder + "%'"); n == 0 {
		t.Fatal("the URL's public text or the placeholder is missing")
	}
	if n := count("SELECT count(*) FROM removal_applied"); n != 1 {
		t.Fatal("the deletion did not travel through the removal list")
	}
}

// installMessengerBundle installs this test executable as Core and the client
// of a fresh state root, with the main identity the messenger harness uses.
func installMessengerBundle(t *testing.T, h *harness) {
	t.Helper()
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
}

// TestM1MessengerStoreRetriesAFailingSource proves the running Core reads a
// source again after a transient page error: the source comes back to
// connected, the recovered page's record and its attachment metadata are in
// canon, and the chat's cursor committed past the page.
func TestM1MessengerStoreRetriesAFailingSource(t *testing.T) {
	t.Parallel()

	h := newHarness(t, "")
	installMessengerBundle(t, h)
	start := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	source := messenger.Source{Person: "person:messenger-host", Platform: "fixture", Account: "account-1",
		Mode: messenger.ModeOfficial, Adapter: "fixture", State: messenger.StateConnected, ConnectedAt: start}
	db, closeStore := installedAcceptanceStore(t, h)
	if err := messenger.Open(db, nil, nil).Connect(h.ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := messenger.Open(db, nil, nil).UpsertChats(h.ctx, source, []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}); err != nil {
		t.Fatal(err)
	}
	closeStore()

	recovered := message("m-1", messenger.RouteParticipant, start, "after the retry")
	recovered.Attachments = []messenger.Attachment{{ID: "a-1", Name: "report.pdf", MIMEType: "application/pdf", Size: 1200}}
	adapter := &retryAdapter{failures: 2, page: messenger.Page{Messages: []messenger.Message{recovered}, Next: "c1"}}
	stop := runMessengerCore(t, h, adapter)
	adapter.waitFor(t, "c1")
	stop()

	if adapter.failuresLeft() != 0 || len(adapter.seen()) < 3 {
		t.Fatalf("reads = %v, failures left %d; want two refusals and a recovery", adapter.seen(), adapter.failuresLeft())
	}
	db, closeStore = installedAcceptanceStore(t, h)
	defer closeStore()
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
	if n := count("SELECT count(*) FROM messenger_source WHERE state='connected' AND failure=''"); n != 1 {
		t.Fatalf("recovered sources = %d, want the failing source read again", n)
	}
	if n := count("SELECT count(*) FROM messenger_record WHERE message='m-1' AND state='live'"); n != 1 {
		t.Fatal("the recovered page's record was not admitted")
	}
	if n := count("SELECT count(*) FROM messenger_attachment WHERE message='m-1' AND attachment='a-1' AND name='report.pdf' AND size=1200"); n != 1 {
		t.Fatal("the attachment metadata was not stored with the record")
	}
	if n := count("SELECT count(*) FROM messenger_chat WHERE chat='dm-1' AND cursor='c1'"); n != 1 {
		t.Fatal("the cursor did not commit past the recovered page")
	}
}

// retryAdapter refuses the first reads and then serves one page, recording every
// cursor it was asked for.
type retryAdapter struct {
	mu       sync.Mutex
	failures int
	page     messenger.Page
	asked    []messenger.Cursor
}

func (a *retryAdapter) SignIn(context.Context, messenger.SourceID) (messenger.SignInState, error) {
	return messenger.SignedIn, nil
}

func (a *retryAdapter) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	return []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}, nil
}

func (a *retryAdapter) Page(_ context.Context, _ messenger.SourceID, _ string, cursor messenger.Cursor) (messenger.Page, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, cursor)
	if a.failures > 0 {
		a.failures--
		return messenger.Page{}, errors.New("platform refused")
	}
	return a.page, nil
}

func (a *retryAdapter) Members(context.Context, messenger.SourceID, string) ([]messenger.Author, error) {
	return nil, nil
}
func (a *retryAdapter) Live(context.Context, messenger.SourceID, func(messenger.Event) error) error {
	return nil
}
func (a *retryAdapter) FetchAttachment(context.Context, messenger.SourceID, string, string) (io.ReadCloser, error) {
	return nil, errors.New("no attachments")
}
func (a *retryAdapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{Edits: true, Deletions: true}
}

func (a *retryAdapter) failuresLeft() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.failures
}

func (a *retryAdapter) seen() []messenger.Cursor {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]messenger.Cursor(nil), a.asked...)
}

// waitFor waits until the adapter has been asked for the named cursor.
func (a *retryAdapter) waitFor(t *testing.T, cursor messenger.Cursor) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for time.Now().Before(deadline) {
		for _, asked := range a.seen() {
			if asked == cursor {
				time.Sleep(50 * time.Millisecond)
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("adapter was not asked for cursor %q", cursor)
}

func message(id string, route messenger.Route, at time.Time, text string) messenger.Message {
	return messenger.Message{ID: id, Route: route, At: at, Text: text, Author: messenger.Author{ID: "author-" + id, Name: "someone"}}
}

func edited(id string, route messenger.Route, at time.Time, text string, edited time.Time) messenger.Message {
	m := message(id, route, at, text)
	m.Edited = edited
	return m
}

// runMessengerCore starts Core in this process with the scripted adapter and
// returns a stop function that waits for an orderly shutdown.
func runMessengerCore(t *testing.T, h *harness, adapter messenger.Adapter) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan error, 1)
	go func() {
		done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: h.socket, Messenger: &boot.MessengerConfig{
			Adapters: map[string]messenger.Adapter{"fixture": adapter}, Interval: 25 * time.Millisecond,
		}})
	}()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("messenger Core stopped with %v", err)
			}
		case <-time.After(operationTimeout):
			t.Fatal("messenger Core shutdown timed out")
		}
	}
}

type scriptedPage struct {
	page  messenger.Page
	block bool
}

// scriptedAdapter serves scripted pages per cursor and records every request.
type scriptedAdapter struct {
	mu        sync.Mutex
	pages     map[string][]scriptedPage
	requested []messenger.Cursor
}

func (a *scriptedAdapter) SignIn(context.Context, messenger.SourceID) (messenger.SignInState, error) {
	return messenger.SignedIn, nil
}

func (a *scriptedAdapter) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	return []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}}, nil
}

func (a *scriptedAdapter) Page(ctx context.Context, _ messenger.SourceID, _ string, cursor messenger.Cursor) (messenger.Page, error) {
	a.mu.Lock()
	a.requested = append(a.requested, cursor)
	queue := a.pages[string(cursor)]
	if len(queue) == 0 {
		a.mu.Unlock()
		return messenger.Page{Next: cursor}, nil
	}
	next := queue[0]
	a.pages[string(cursor)] = queue[1:]
	a.mu.Unlock()
	if next.block {
		<-ctx.Done()
		return messenger.Page{}, ctx.Err()
	}
	return next.page, nil
}

func (a *scriptedAdapter) Members(context.Context, messenger.SourceID, string) ([]messenger.Author, error) {
	return nil, nil
}
func (a *scriptedAdapter) Live(context.Context, messenger.SourceID, func(messenger.Event) error) error {
	return nil
}
func (a *scriptedAdapter) FetchAttachment(context.Context, messenger.SourceID, string, string) (io.ReadCloser, error) {
	return nil, errors.New("no attachments")
}
func (a *scriptedAdapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{Edits: true, Deletions: true}
}

// waitFor waits until the adapter has been asked for the named cursor.
func (a *scriptedAdapter) waitFor(t *testing.T, cursor messenger.Cursor) {
	t.Helper()
	deadline := time.Now().Add(operationTimeout)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		for _, requested := range a.requested {
			if requested == cursor {
				a.mu.Unlock()
				// Give the page one commit interval to land before Core stops.
				time.Sleep(50 * time.Millisecond)
				return
			}
		}
		a.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("adapter was not asked for cursor %q", cursor)
}
