// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
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
	// Disconnect must stop the reader without another scheduled read.
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

func (a *liveAdapter) SignIn(context.Context, messenger.SourceID) (messenger.SignInState, error) {
	if a.fail != nil {
		return messenger.Failing, a.fail
	}
	return messenger.SignedIn, nil
}

func (a *liveAdapter) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	return a.chats, nil
}

func (a *liveAdapter) Page(_ context.Context, _ messenger.SourceID, _ string, cursor messenger.Cursor) (messenger.Page, error) {
	if a.fail != nil {
		return messenger.Page{}, a.fail
	}
	return messenger.Page{Next: cursor}, nil
}

func (a *liveAdapter) Members(context.Context, messenger.SourceID, string) ([]messenger.Author, error) {
	return nil, nil
}

func (a *liveAdapter) Live(ctx context.Context, _ messenger.SourceID, deliver func(messenger.Event) error) error {
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

func (a *liveAdapter) FetchAttachment(context.Context, messenger.SourceID, string, string) (io.ReadCloser, error) {
	return nil, errors.New("no attachments")
}

func (a *liveAdapter) Capabilities() messenger.Capabilities {
	return messenger.Capabilities{Edits: true, Deletions: true, LiveEvents: true}
}

func TestLiveEventsHonorCurrentSwitchAndSourceState(t *testing.T) {
	for _, stop := range []string{"sync", "disconnect"} {
		t.Run(stop, func(t *testing.T) {
			f := newFixture(t)
			source := f.connect(t)
			old := message("old", messenger.RouteParticipant, f.now, "retained")
			if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{old}, Next: "page-1"}); err != nil {
				t.Fatal(err)
			}
			if stop == "sync" {
				if err := f.store.SetSwitches(t.Context(), "person", source.Platform, source.Account, "dm-1", messenger.Switches{}); err != nil {
					t.Fatal(err)
				}
			} else if err := f.store.Disconnect(t.Context(), "person", source.Platform, source.Account); err != nil {
				t.Fatal(err)
			}
			fresh := message("new", messenger.RouteParticipant, f.now, "excluded")
			for _, event := range []messenger.Event{{Chat: "dm-1", Message: &fresh}, {Chat: "dm-1", Deletion: &messenger.Deletion{Message: "old", At: f.now}}} {
				_, err := f.store.AdmitEvent(t.Context(), source, event)
				if err != nil && !errors.Is(err, messenger.ErrNotConnected) {
					t.Fatal(err)
				}
			}
			if n := count(t, f.db, "SELECT count(*) FROM messenger_record WHERE message='new' OR state='deleted'"); n != 0 {
				t.Fatalf("disabled live admission changed %d records", n)
			}
		})
	}
}

func TestLiveAdmissionDoesNotOverwriteNewerProgress(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	// The trigger models a scheduled progress commit after live admission reads
	// its inputs, deterministically, without racing goroutines or sleeping.
	if err := f.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER scheduled_progress BEFORE INSERT ON messenger_record BEGIN
 UPDATE messenger_chat SET cursor='newer-page' WHERE chat='dm-1'; END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m := message("new", messenger.RouteParticipant, f.now, "live")
	if _, err := f.store.AdmitEvent(t.Context(), source, messenger.Event{Chat: "dm-1", Message: &m}); err != nil {
		t.Fatal(err)
	}
	chats, err := f.store.Chats(t.Context(), "person", source.Platform, source.Account)
	if err != nil {
		t.Fatal(err)
	}
	if chats[0].Cursor != "newer-page" {
		t.Fatalf("live admission overwrote progress: %q", chats[0].Cursor)
	}
}

// Pause a scheduled page after planning but before committing its event. The
// live deletion must either see that event or prevent its later admission.
func TestScheduledPageCannotOutrunLiveDeletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture(t)
		source := f.connect(t)
		planned, release := make(chan struct{}), make(chan struct{})
		var paused atomic.Bool
		f.store.WithClock(func() time.Time {
			if paused.CompareAndSwap(false, true) {
				close(planned)
				<-release
			}
			return f.now
		})
		pageDone, deletionDone := make(chan error, 1), make(chan error, 1)
		go func() {
			_, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
				Messages: []messenger.Message{message("overlap", messenger.RouteParticipant, f.now, "must disappear")}, Next: "page-1",
			})
			pageDone <- err
		}()
		<-planned
		go func() {
			_, err := f.store.AdmitEvent(t.Context(), source, messenger.Event{Chat: "dm-1", Deletion: &messenger.Deletion{Message: "overlap", At: f.now}})
			deletionDone <- err
		}()
		// Wait for the deletion to finish or block behind page admission. This
		// chooses the interleaving deterministically, with no wall-clock delay.
		synctest.Wait()
		close(release)
		if err := <-pageDone; err != nil {
			t.Fatal(err)
		}
		if err := <-deletionDone; err != nil {
			t.Fatal(err)
		}
		if n := count(t, f.db, "SELECT count(*) FROM retract_visible_event WHERE origin LIKE '%messenger%'"); n != 0 {
			t.Fatalf("page resurrected %d searchable events after a live deletion", n)
		}
	})
}

// Production Discord has Close() with no error result. Both registry eviction
// and explicit service cleanup must release that adapter's transport.
func TestServiceClosesAdaptersWithoutErrorResult(t *testing.T) {
	for _, action := range []string{"shutdown", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t)
			source := f.connect(t)
			stop := errors.New("stop before starting the reader")
			adapter := &voidCloseAdapter{liveAdapter: &liveAdapter{fail: stop}}
			service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
			t.Cleanup(service.Close)
			// Resolve and cache the adapter, but stop before starting any live reader.
			if _, err := service.Sync(t.Context(), source); !errors.Is(err, stop) {
				t.Fatalf("sync=%v", err)
			}
			if action == "shutdown" {
				service.Close()
			} else {
				if err := f.store.Disconnect(t.Context(), "person", source.Platform, source.Account); err != nil {
					t.Fatal(err)
				}
				service.SyncAll(t.Context(), &f.now)
			}
			if calls := adapter.closed.Load(); calls != 1 {
				t.Fatalf("adapter closed %d times, want once", calls)
			}
		})
	}
}

type voidCloseAdapter struct {
	*liveAdapter
	closed atomic.Int32
}

func (a *voidCloseAdapter) Close() { a.closed.Add(1) }

// resultAdapter reports what the service's live delivery returned, so a test
// can tell an acknowledged event from a replayable one.
type resultAdapter struct {
	*liveAdapter
	results chan error
}

func (a *resultAdapter) Live(ctx context.Context, _ messenger.SourceID, deliver func(messenger.Event) error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-a.events:
			a.results <- deliver(event)
		}
	}
}

// A live edit that arrives while the chat has an unsettled write is not
// acknowledged: the service asks the session to replay it, and once the write
// settles the same edit is admitted instead of being lost.
func TestLiveEditDuringPendingWriteIsRetried(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{
		Messages: []messenger.Message{message("m-1", messenger.RoutePerson, f.now, "first")},
		Next:     "cursor-1",
	}); err != nil {
		t.Fatal(err)
	}
	effect := messenger.Effect{ID: "effect-1", Platform: source.Platform, Account: source.Account, Chat: "dm-1",
		Agent: "agent:main", Verb: "send", ClientID: "client-1", State: messenger.EffectPending, CreatedAt: f.now}
	if err := f.store.RecordEffect(t.Context(), effect); err != nil {
		t.Fatal(err)
	}
	events := make(chan messenger.Event, 4)
	results := make(chan error, 4)
	adapter := &resultAdapter{liveAdapter: &liveAdapter{chats: []messenger.Chat{{ID: "dm-1", Kind: "direct"}}, events: events}, results: results}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); service.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	edit := messenger.Event{Chat: "dm-1", Message: &messenger.Message{
		ID: "m-1", Route: messenger.RoutePerson, At: f.now.Add(time.Minute), Edited: f.now.Add(time.Minute), Text: "edited",
		Author: messenger.Author{ID: "person", Name: "Jun"},
	}}
	events <- edit
	select {
	case err := <-results:
		if err == nil || !channels.Retryable(err) {
			t.Fatalf("a live edit during an unsettled write was acknowledged: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the live reader never reported the admission")
	}
	// The write settles; the replayed dispatch is admitted instead of lost.
	if err := f.store.SettleEffect(t.Context(), "effect-1", messenger.EffectSent, "platform-9"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AdmitEvent(t.Context(), source, edit); err != nil {
		t.Fatal(err)
	}
	if got := f.eventsFor(t, source, "dm-1", "m-1"); got.Body.Text() != "edited" {
		t.Fatalf("the replayed edit was not applied: %q", got.Body.Text())
	}
}

// A source disconnected and reconnected with a replacement token before the
// next schedule must not keep reading with the previous credentials.
func TestReconnectedSourceResolvesAFreshAdapter(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	started := make(chan struct{}, 4)
	stopped := make(chan struct{}, 4)
	var resolved atomic.Int32
	service := messenger.NewServiceWithResolver(f.store, func(messenger.Source) (messenger.Adapter, bool) {
		resolved.Add(1)
		return &liveAdapter{chats: []messenger.Chat{{ID: "dm-1", Name: "Direct", Kind: "direct"}},
			events: make(chan messenger.Event), started: started, stopped: stopped}, true
	}, time.Millisecond)
	t.Cleanup(service.Close)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service.SyncAll(ctx, nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the live reader never started")
	}
	if err := f.store.Disconnect(t.Context(), "person", source.Platform, source.Account); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the live reader did not stop on disconnect")
	}
	if err := f.store.Connect(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	service.SyncAll(ctx, nil)
	if got := resolved.Load(); got != 2 {
		t.Fatalf("adapter resolutions = %d, want a fresh adapter after the reconnect", got)
	}
}
