// SPDX-License-Identifier: Apache-2.0
package worker

import (
	"context"
	"sync"
	"time"
)

const snapshotFreshness = 5 * time.Second

// SnapshotCache belongs to one live session. Read never waits for adapter I/O;
// an absent, expired or different-turn observation is unknown. At most one
// bounded read is in flight, and its completion wakes the progress publisher.
type SnapshotCache struct {
	mu       sync.Mutex
	thread   Thread
	snapshot ThreadSnapshot
	err      error
	at       time.Time
	pending  bool
	reads    sync.WaitGroup
	cancel   context.CancelFunc
	closed   bool
}

func (c *SnapshotCache) Read(ctx context.Context, thread Thread, read func(context.Context, Thread) (ThreadSnapshot, error), changed func()) (ThreadSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	unknown := ThreadSnapshot{Thread: thread, State: Unknown}
	if c.closed || ctx.Err() != nil {
		return unknown, ctx.Err()
	}
	if c.thread == thread && !c.at.IsZero() && time.Since(c.at) < snapshotFreshness {
		return c.snapshot, c.err
	}
	if c.pending {
		if c.thread != thread {
			c.cancel()
		}
		return unknown, nil
	}
	watch, cancel := context.WithTimeout(ctx, snapshotFreshness)
	c.thread, c.at, c.cancel = thread, time.Time{}, cancel
	c.pending = true
	c.reads.Add(1)
	go func() {
		defer c.reads.Done()
		snapshot, err := read(watch, thread)
		cancel()
		c.mu.Lock()
		if !c.closed {
			c.snapshot, c.err, c.at = snapshot, err, time.Now()
		}
		c.pending = false
		closed := c.closed
		c.mu.Unlock()
		if !closed && ctx.Err() == nil && changed != nil {
			changed()
		}
	}()
	return unknown, nil
}

// Close cancels and joins an observation before its session is released.
func (c *SnapshotCache) Close() {
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	c.reads.Wait()
}
