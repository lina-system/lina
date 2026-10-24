// SPDX-License-Identifier: Apache-2.0
package messenger_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/messenger"
)

// stuckAdapter holds ListChats until released, ignoring cancellation, as a
// platform request or rate-limit wait that does not watch its context would.
type stuckAdapter struct {
	fakeAdapter
	entered chan struct{}
	release chan struct{}
}

func (a *stuckAdapter) ListChats(context.Context, messenger.SourceID) ([]messenger.Chat, error) {
	close(a.entered)
	<-a.release
	return nil, nil
}

func twoSources(t *testing.T, f *fixture) (messenger.Source, messenger.Source) {
	t.Helper()
	first, second := f.source(), f.source()
	first.Account, first.Adapter = "account-a", "discord-a"
	second.Account, second.Adapter = "account-b", "discord-b"
	for _, source := range []messenger.Source{first, second} {
		if err := f.store.Connect(t.Context(), source); err != nil {
			t.Fatal(err)
		}
	}
	return first, second
}

// waitDone fails the test when call has not returned once the other source's
// read is known to be stuck. The correct path does not wait on that read at all.
func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("disconnect waited on another source's read")
		return nil
	}
}

func TestDisconnectDoesNotWaitForAnotherSource(t *testing.T) {
	f := newFixture(t)
	first, second := twoSources(t, f)
	stuck := &stuckAdapter{entered: make(chan struct{}), release: make(chan struct{})}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord-a": &fakeAdapter{}, "discord-b": stuck}, time.Hour)
	synced := make(chan error, 1)
	go func() { _, err := service.Sync(t.Context(), second); synced <- err }()
	<-stuck.entered
	defer func() { close(stuck.release); <-synced }()

	done := make(chan error, 1)
	go func() { done <- service.Disconnect(t.Context(), first) }()
	if err := waitDone(t, done); err != nil {
		t.Fatal(err)
	}
	// Live admission for the other source is not held behind the disconnect.
	applied := make(chan error, 1)
	go func() {
		_, err := service.Apply(t.Context(), second, messenger.Event{Chat: "dm-1", Deletion: &messenger.Deletion{Message: "x"}})
		applied <- err
	}()
	if err := waitDone(t, applied); err != nil {
		t.Fatal(err)
	}
	sources, err := f.store.Sources(t.Context(), "person")
	if err != nil || len(sources) != 2 || sources[0].State != messenger.StateDisconnected || sources[1].State != messenger.StateConnected {
		t.Fatalf("sources after disconnect: %+v %v", sources, err)
	}
}

func TestDisconnectHonoursItsContext(t *testing.T) {
	f := newFixture(t)
	first, _ := twoSources(t, f)
	stuck := &stuckAdapter{entered: make(chan struct{}), release: make(chan struct{})}
	service := messenger.NewService(f.store, map[string]messenger.Adapter{"discord-a": stuck}, time.Hour)
	synced := make(chan error, 1)
	go func() { _, err := service.Sync(t.Context(), first); synced <- err }()
	<-stuck.entered
	defer func() { close(stuck.release); <-synced }()

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.Disconnect(ctx, first) }()
	cancel()
	if err := waitDone(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled disconnect: %v", err)
	}
}
