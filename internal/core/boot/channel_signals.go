// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/lease"
	"sync/atomic"
	"time"
)

type channelPresenter interface {
	PresentApproval(context.Context, lease.Lease, channels.Address, string) (gateway.SendResult, error)
}

// Presentation never holds channel control's mutex or a turn lane. The local
// presence counter is safe to read inside authority's canon transaction.
func (c *channelRuntime) channelSignals(ctx context.Context, bot channels.Bot, adapter channelAdapter, healthy *atomic.Bool) {
	presenter, ok := adapter.(channelPresenter)
	if !ok {
		return
	}
	available := false
	defer func() {
		if available {
			c.r.channelSurfaces.Add(-1)
		}
	}()
	attempted := map[string]bool{}
	for ctx.Err() == nil {
		var bindings []channels.Binding
		if healthy.Load() {
			rows, err := c.store.Connections(ctx, c.r.person)
			if err == nil {
				for _, row := range rows {
					if row.Bot == bot && row.Sender != "" {
						for _, b := range row.Bindings {
							if b.Chat == row.PrivateChat && b.Topic == "" && b.Scope.Tools && !b.Unreachable {
								bindings = append(bindings, b)
							}
						}
					}
				}
			}
		}
		next := len(bindings) > 0
		if next != available {
			if next {
				c.r.channelSurfaces.Add(1)
			} else {
				c.r.channelSurfaces.Add(-1)
			}
			available = next
		}
		if available {
			pending, err := c.r.personaAuthority.Pending(ctx)
			if err == nil {
				for _, p := range pending {
					for _, b := range bindings {
						key := b.Key() + ":" + p.ID
						if p.Person != b.Person || p.Agent != b.Agent || attempted[key] {
							continue
						}
						activity, err := c.r.activities.Acquire(ctx, lease.Activity{Class: lease.Live, Reason: "channel approval"})
						if err != nil {
							return
						}
						attempted[key] = true
						_, _ = presenter.PresentApproval(ctx, activity, b.Address, p.ID)
						activity.Release()
					}
				}
			}
		}
		if channelPause(ctx, 100*time.Millisecond) != nil {
			return
		}
	}
}
