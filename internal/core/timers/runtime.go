// SPDX-License-Identifier: Apache-2.0
package timers

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Run owns one sleeper. Bounded delivery workers keep a slow conversation from
// holding other timers. Cancelling Core joins all deliveries before stores close.
func (s *Store) Run(ctx context.Context, epoch string, on func(context.Context, Fire) error) {
	queue := make(chan Fire, 64)
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for f := range queue {
				if ctx.Err() != nil {
					continue
				}
				// A replacement never spins on a broken source. Its durable
				// attempt also counts toward the restart replacement budget.
				if f.Attempt > 1 && !waitRetry(ctx, time.Second) {
					continue
				}
				settle := s.Finish
				if on(ctx, f) != nil {
					settle = s.Fail
				}
				// Retain the outcome while storage is unavailable. Retrying
				// acknowledgement must never invoke the action again.
				for delay := time.Second; ctx.Err() == nil; delay = min(2*delay, time.Minute) {
					err := settle(ctx, f)
					if err == nil || errors.Is(err, ErrClaim) || !waitRetry(ctx, delay) {
						break
					}
				}
			}
		})
	}
	defer func() { close(queue); workers.Wait() }()
	sleeper := time.NewTimer(time.Hour)
	defer sleeper.Stop()
	for ctx.Err() == nil {
		changed := s.db.Changed()
		fires, err := s.ClaimDue(ctx, time.Now(), epoch)
		if err == nil {
			for _, f := range fires {
				select {
				case queue <- f:
				case <-ctx.Done():
					return
				}
			}
		}
		delay := time.Minute // bounded backoff on storage errors and wall-clock changes
		if err == nil {
			if next, e := s.NextDue(ctx); e == nil && !next.IsZero() {
				delay = min(delay, max(time.Until(next), time.Millisecond))
			}
		}
		sleeper.Reset(delay)
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-sleeper.C:
		}
	}
}

func waitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
