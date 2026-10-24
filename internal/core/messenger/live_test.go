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

// A live event is admitted once and leaves the chat's cursor and coverage
// where the last page put them.
func TestLiveEventsAreAdmittedWithoutMovingTheCursor(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
		Messages: []messenger.Message{message("m-1", messenger.RoutePerson, f.now, "first")},
		Next:     "cursor-1",
	}); err != nil {
		t.Fatal(err)
	}
	coverage := count(t, f.db, "SELECT count(*) FROM messenger_coverage WHERE chat='dm-1'")

	event := messenger.Event{Chat: "dm-1", Message: &messenger.Message{
		ID: "m-2", Route: messenger.RouteParticipant, At: f.now.Add(time.Minute), Text: "live",
		Author: messenger.Author{ID: "author-2", Name: "someone"},
	}}
	if _, err := f.store.AdmitEvent(t.Context(), source, event); err != nil {
		t.Fatal(err)
	}
	again, err := f.store.AdmitEvent(t.Context(), source, event)
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 {
		t.Fatalf("a repeated live event added %d records", again.Added)
	}
	chats, err := f.store.Chats(t.Context(), "person", "discord", "account-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Cursor != "cursor-1" {
		t.Fatalf("a live event moved the cursor: %+v", chats)
	}
	if got := count(t, f.db, "SELECT count(*) FROM messenger_coverage WHERE chat='dm-1'"); got != coverage {
		t.Fatalf("a live event wrote coverage: %d, want %d", got, coverage)
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE chat='dm-1' AND state='live'"); n != 2 {
		t.Fatalf("records = %d, want 2", n)
	}
}

// The sync loop's live reader admits pushed messages and deletions while it
// runs, and stops with the service.
func TestServiceFollowsLiveEvents(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
		Messages: []messenger.Message{message("m-1", messenger.RoutePerson, f.now, "first")},
		Next:     "cursor-1",
	}); err != nil {
		t.Fatal(err)
	}
	events := make(chan messenger.Event, 4)
	adapter := &liveAdapter{chats: []messenger.Chat{{ID: "dm-1", Kind: "direct"}}, events: events}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	events <- messenger.Event{Chat: "dm-1", Message: &messenger.Message{
		ID: "m-2", Route: messenger.RouteParticipant, At: f.now.Add(time.Minute), Text: "pushed",
		Author: messenger.Author{ID: "author-2", Name: "someone"},
	}}
	events <- messenger.Event{Chat: "dm-1", Deletion: &messenger.Deletion{Message: "m-1", At: f.now.Add(2 * time.Minute)}}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if count(t, f.db, "SELECT count(*) FROM messenger_record WHERE message='m-2' AND state='live'") == 1 &&
			count(t, f.db, "SELECT count(*) FROM messenger_record WHERE message='m-1' AND state='deleted'") == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the live reader did not admit its events")
}

// Disconnecting a source stops its live reader at once.
func TestDisconnectStopsTheLiveReader(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	adapter := &liveAdapter{chats: []messenger.Chat{{ID: "dm-1", Kind: "direct"}}, events: make(chan messenger.Event), started: make(chan struct{}, 4), stopped: make(chan struct{}, 4)}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	select {
	case <-adapter.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the live reader never started")
	}
	if err := f.store.Disconnect(t.Context(), "person", source.Platform, source.Account); err != nil {
		t.Fatal(err)
	}
	// The next schedule notices the source is gone and stops its reader.
	service.SyncAll(t.Context(), nil)
	select {
	case <-adapter.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("a disconnected source kept its live reader")
	}
}

// Without message text the source states the cause and stores nothing.
func TestTextUnavailableIsReportedOnTheSource(t *testing.T) {
	f := newFixture(t)
	f.connect(t)
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": &liveAdapter{fail: messenger.ErrTextUnavailable}}, time.Hour)
	service.SyncAll(t.Context(), &f.now)
	sources, err := f.store.Sources(t.Context(), "person")
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources: %+v %v", sources, err)
	}
	if sources[0].State != messenger.StateFailing || !strings.Contains(sources[0].Failure, "message text is unavailable") {
		t.Fatalf("failure: %+v", sources[0])
	}
	if n := count(t, f.db, "SELECT count(*) FROM messenger_record"); n != 0 {
		t.Fatalf("records = %d, want none", n)
	}
}

// liveAdapter lists one chat and delivers scripted live events until ctx ends.
type liveAdapter struct {
	chats   []messenger.Chat
	events  chan messenger.Event
	started chan struct{}
	stopped chan struct{}
	fail    error
}

func (a *liveAdapter) SignIn(context.Context) (messenger.SignInState, error) {
	if a.fail != nil {
		return messenger.Failing, a.fail
	}
	return messenger.SignedIn, nil
}

func (a *liveAdapter) ListChats(context.Context) ([]messenger.Chat, error) { return a.chats, nil }

func (a *liveAdapter) Page(_ context.Context, _ string, cursor messenger.Cursor) (messenger.Page, error) {
	if a.fail != nil {
		return messenger.Page{}, a.fail
	}
	return messenger.Page{Next: cursor}, nil
}

func (a *liveAdapter) Members(context.Context, string) ([]messenger.Author, error) { return nil, nil }

func (a *liveAdapter) Live(ctx context.Context, deliver func(messenger.Event) error) error {
	if a.started != nil {
		a.started <- struct{}{}
	}
	if a.stopped != nil {
		defer func() { a.stopped <- struct{}{} }()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-a.events:
			if err := deliver(event); err != nil && !errors.Is(err, messenger.ErrPageWaiting) {
				return err
			}
		}
	}
}

func (a *liveAdapter) FetchAttachment(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("no attachments")
}

func (a *liveAdapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{Edits: true, Deletions: true, LiveEvents: true}
}
