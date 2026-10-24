// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotCacheReturnsUnknownWhileReadingAndFencesTurns(t *testing.T) {
	var cache SnapshotCache
	defer cache.Close()
	entered, release, changed := make(chan Thread, 2), make(chan struct{}), make(chan struct{}, 2)
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	read := func(ctx context.Context, thread Thread) (ThreadSnapshot, error) {
		calls.Add(1)
		entered <- thread
		select {
		case <-release:
			return ThreadSnapshot{Thread: thread, State: Active}, nil
		case <-ctx.Done():
			return ThreadSnapshot{}, ctx.Err()
		}
	}
	notify := func() { changed <- struct{}{} }
	first := Thread{ID: "thread", TurnID: "first"}
	if got, err := cache.Read(t.Context(), first, read, notify); err != nil || got.State != Unknown {
		t.Fatalf("cold observation: %+v %v", got, err)
	}
	<-entered
	if got, _ := cache.Read(t.Context(), first, read, notify); got.State != Unknown || calls.Load() != 1 {
		t.Fatal("pending read was duplicated or guessed")
	}
	next := first
	next.TurnID = "next"
	if got, _ := cache.Read(t.Context(), next, read, notify); got.State != Unknown {
		t.Fatal("old turn entered the new turn's projection")
	}
	<-changed // Changing turns cancels the old read, without waiting in Read.
	if got, _ := cache.Read(t.Context(), next, read, notify); got.State != Unknown {
		t.Fatal("unread turn claimed a state")
	}
	if got := <-entered; got != next {
		t.Fatalf("read %+v, want %+v", got, next)
	}
	cache.Close() // Cancellation must join the blocked read.
	if _, err := cache.Read(t.Context(), next, read, notify); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("closed cache started another read")
	}
}

func TestSnapshotCacheExpiresWithoutPublishingStaleState(t *testing.T) {
	var cache SnapshotCache
	defer cache.Close()
	changed := make(chan struct{}, 2)
	thread := Thread{ID: "thread", TurnID: "turn"}
	read := func(context.Context, Thread) (ThreadSnapshot, error) {
		return ThreadSnapshot{Thread: thread, State: Waiting}, nil
	}
	notify := func() { changed <- struct{}{} }
	cache.Read(t.Context(), thread, read, notify)
	<-changed
	if got, _ := cache.Read(t.Context(), thread, read, notify); got.State != Waiting {
		t.Fatal("fresh observation was lost")
	}
	cache.mu.Lock()
	cache.at = time.Now().Add(-2 * snapshotFreshness)
	cache.mu.Unlock()
	blocked := func(ctx context.Context, _ Thread) (ThreadSnapshot, error) {
		<-ctx.Done()
		return ThreadSnapshot{}, ctx.Err()
	}
	if got, _ := cache.Read(t.Context(), thread, blocked, notify); got.State != Unknown {
		t.Fatal("expired observation stayed visible")
	}
}

func TestLiveCachedObservationDoesNotSurviveReadoption(t *testing.T) {
	l := NewLive()
	defer l.Close(context.Background())
	changed := make(chan struct{}, 2)
	notify := func() { changed <- struct{}{} }
	s := session("handle", "task")
	adopt := func() {
		a := newIdleAdapter()
		if err := l.Adopt(s, a, func(context.Context) error { close(a.events); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	adopt()
	_, _, live, err := l.ObserveCached(t.Context(), s.ID, notify)
	if !live || err != nil {
		t.Fatal("missing live session", err)
	}
	<-changed
	if err := l.End(t.Context(), s.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, live, _ := l.ObserveCached(t.Context(), s.ID, notify); live {
		t.Fatal("ended session remained visible")
	}
	adopt() // Even the identical handle/thread belongs to a new cache.
	_, got, live, err := l.ObserveCached(t.Context(), s.ID, notify)
	if !live || err != nil || got.State != Unknown {
		t.Fatalf("replacement reused the old cache: %+v %v", got, err)
	}
}
