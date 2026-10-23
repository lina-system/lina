// SPDX-License-Identifier: Apache-2.0
package timers

import (
	"context"
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
				if ctx.Err() == nil && on(ctx, f) == nil {
					_ = s.Finish(ctx, f)
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
