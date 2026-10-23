// SPDX-License-Identifier: Apache-2.0
// Package reactor delivers committed work signals through Core's reaction path.
package reactor

import (
	"context"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
)

const SweepInterval = 5 * time.Minute

type Reactor struct {
	DB    *canon.Store
	Work  *work.Service
	Epoch string
	On    func(context.Context, work.Completion, work.Card) error
}

// Sweep only repairs durable signals. It has no model or worker dependency.
func (r *Reactor) Sweep(ctx context.Context) error { return r.Work.ReconcileCompletions(ctx) }

func (r *Reactor) Drain(ctx context.Context) error {
	pending, err := r.Work.PendingCompletions(ctx, r.Epoch)
	if err != nil {
		return err
	}
	var failures []error
	for _, candidate := range pending {
		event, card, claimed, err := r.Work.ClaimCompletion(ctx, candidate.ID, r.Epoch)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if err = r.On(ctx, event, card); err == nil {
			err = r.Work.AcknowledgeCompletion(ctx, event.ID, r.Epoch)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Run subscribes before reading so committed completions wake it immediately.
// Unsuccessful deliveries retry at the slow reconciliation boundary; their
// downstream inbox/turn identities survive crashes and prevent duplicate work.
func (r *Reactor) Run(ctx context.Context) {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()
	for {
		changed := r.DB.Changed()
		_ = r.Drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-ticker.C:
			if r.Sweep(ctx) == nil {
				_ = r.Work.RetryCompletions(ctx, r.Epoch)
			}
		}
	}
}
