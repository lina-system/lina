// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/lane"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/lease"
)

func TestConversationCleanupAfterWorkFailure(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	failure := errors.New("work engine failed")
	cleanupFailure := errors.New("growth shutdown failed")
	growthDone := make(chan error, 1)
	growthDone <- cleanupFailure
	r.growth = &growthRuntime{stop: func() {}, done: growthDone}
	done := make(chan struct{})
	close(done)
	r.workEngine = &workRuntime{stop: func() {}, done: done, err: failure}
	watcherDone := make(chan struct{})
	r.ownerViews = &ownerViews{stop: func() { close(watcherDone) }, done: watcherDone}
	workerDone := make(chan struct{})
	released := false
	r.workers = &workerRuntime{sessions: map[string]*workerSession{"worker": {
		close: func() error { close(workerDone); return nil }, done: workerDone,
		process: lease.New(func() { released = true }),
	}}}
	var err error
	r.records, err = records.Open(r.root, r.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.records.Close() })
	if err := r.Close(t.Context()); !errors.Is(err, failure) || !errors.Is(err, cleanupFailure) {
		t.Errorf("lost work or cleanup failure: %v", err)
	}
	select {
	case <-watcherDone:
	default:
		t.Error("owner watcher was not stopped")
	}
	if _, err := r.lanes.Submit(t.Context(), "after-close", func(context.Context) error { return nil }); !errors.Is(err, lane.ErrClosed) {
		t.Errorf("conversation lanes remain open: %v", err)
	}
	if err := r.db.Read(t.Context(), func(*canon.Tx) error { return nil }); !errors.Is(err, canon.ErrClosed) {
		t.Errorf("canon remains open after work failure: %v", err)
	}
	if !r.workers.closed || !released {
		t.Error("worker session remains open")
	}
	if _, err := r.records.Head(t.Context()); err == nil {
		t.Error("records remain open")
	}
	// The regression must also clean up the pre-fix failure path.
	_ = r.lanes.Close(context.Background())
	if !r.workers.closed {
		r.workers.close()
	}
}

func TestConversationCleanupWaitsForWorkToJoin(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	done := make(chan struct{})
	stopped := false
	r.workEngine = &workRuntime{stop: func() { stopped = true }, done: done}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Close(ctx); !errors.Is(err, context.Canceled) || !stopped {
		t.Fatalf("shutdown did not cancel and wait: stopped=%v err=%v", stopped, err)
	}
	if err := r.db.Read(t.Context(), func(*canon.Tx) error { return nil }); err != nil {
		t.Fatalf("closed canon while work can still access it: %v", err)
	}
	close(done)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Read(t.Context(), func(*canon.Tx) error { return nil }); !errors.Is(err, canon.ErrClosed) {
		t.Fatalf("canon remains open after joining work: %v", err)
	}
}
