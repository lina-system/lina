// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"context"
	"testing"
)

type liveReadProbe struct {
	*idleAdapter
	entered, release chan struct{}
}

func (p *liveReadProbe) Snapshot(ctx context.Context, thread Thread) (ThreadSnapshot, error) {
	if p.entered != nil {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return ThreadSnapshot{}, ctx.Err()
		}
	}
	return ThreadSnapshot{Thread: thread, State: Active}, nil
}

func TestLiveObserveRejectsEndedAndReplacedSession(t *testing.T) {
	l := NewLive()
	old := &liveReadProbe{idleAdapter: newIdleAdapter(), entered: make(chan struct{}), release: make(chan struct{})}
	first := session("handle", "task")
	if err := l.Adopt(first, old, func(context.Context) error { close(old.events); return nil }); err != nil {
		t.Fatal(err)
	}
	read := make(chan bool, 1)
	go func() { _, _, live, _ := l.Observe(t.Context(), first.ID); read <- live }()
	<-old.entered
	// Ending does not wait for the read; replacing this handle cannot make the
	// old observation live again even if it returns after the new adoption.
	if err := l.End(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	next := &liveReadProbe{idleAdapter: newIdleAdapter()}
	replacement := first
	replacement.Thread.ID = "replacement"
	if err := l.Adopt(replacement, next, func(context.Context) error { close(next.events); return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(context.Background()) })
	close(old.release)
	if <-read {
		t.Fatal("ended session's observation survived a replacement")
	}
	s, snapshot, live, err := l.Observe(t.Context(), first.ID)
	if err != nil || !live || s.Thread != replacement.Thread || snapshot.State != Active {
		t.Fatalf("replacement observation: %+v %+v %v %v", s, snapshot, live, err)
	}
}
