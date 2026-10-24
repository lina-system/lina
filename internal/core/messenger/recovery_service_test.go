// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/messenger"
	"testing"
	"time"
)

type recoveryAdapter struct {
	fakeAdapter
	failRecovery error
	event        messenger.Event
	recovered    bool
}

func (a *recoveryAdapter) Recover(_ context.Context, _ messenger.SourceID, deliver func(messenger.Event) error) error {
	if a.failRecovery != nil {
		return a.failRecovery
	}
	a.recovered = true
	return deliver(a.event)
}
func (a *recoveryAdapter) Page(ctx context.Context, id messenger.SourceID, chat string, cursor messenger.Cursor) (messenger.Page, error) {
	if !a.recovered {
		return messenger.Page{}, errors.New("history started before update recovery")
	}
	return a.fakeAdapter.Page(ctx, id, chat, cursor)
}
func TestSourceRecoveryPrecedesHistoryAndFailureRetainsCursor(t *testing.T) {
	f := newFixture(t)
	source := f.connect(t)
	original := message("1", messenger.RouteParticipant, f.now, "original")
	if _, err := f.store.AdmitPage(t.Context(), source, "dm-1", "", messenger.Page{Messages: []messenger.Message{original}, Next: "old"}); err != nil {
		t.Fatal(err)
	}
	edit := original
	edit.Text = "recovered edit"
	edit.Edited = f.now.Add(time.Minute)
	failure := errors.New("update recovery failed")
	adapter := &recoveryAdapter{failRecovery: failure, event: messenger.Event{Chat: "dm-1", Message: &edit}, fakeAdapter: fakeAdapter{pages: map[string][]messenger.Page{"old": {{Messages: []messenger.Message{original}, Next: "current"}}}}}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord": adapter}, time.Hour)
	if _, err := service.Sync(t.Context(), source); !errors.Is(err, failure) {
		t.Fatalf("recovery failure was bypassed: %v", err)
	}
	chats, err := f.store.Chats(t.Context(), source.Person, source.Platform, source.Account)
	if err != nil || chats[0].Cursor != "old" {
		t.Fatalf("failed recovery moved cursor: %+v %v", chats, err)
	}
	adapter.failRecovery = nil
	if _, err := service.Sync(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.db, "SELECT count(*) FROM conv_event WHERE body='recovered edit'"); n != 1 {
		t.Fatal("missed edit did not reach canon")
	}
	chats, err = f.store.Chats(t.Context(), source.Person, source.Platform, source.Account)
	if err != nil || chats[0].Cursor != "current" {
		t.Fatalf("recovered source did not advance: %+v %v", chats, err)
	}
}
