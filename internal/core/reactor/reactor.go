// SPDX-License-Identifier: Apache-2.0
// Package reactor releases delegated work when authoritative observations change.
package reactor

import (
	"context"
	"errors"
	"sync"

	"github.com/thisisjun786/lina/internal/core/work"
)

// Hooks are bound by boot to worker and forge adapters. A start error has an
// unknown outcome: its durable reservation remains held until reconciliation.
type Hooks struct {
	Availability func(context.Context) (work.Availability, error)
	Start        func(context.Context, work.Card) (work.Run, error)
	ReadPR       func(context.Context, work.Card) (work.PullRequest, error)
	Return       func(context.Context, work.Card) (work.Run, error)
	Changed      func(context.Context)
}
type Reactor struct {
	mu    sync.Mutex
	store *work.Service
	hooks Hooks
}

func New(s *work.Service, h Hooks) *Reactor { return &Reactor{store: s, hooks: h} }
func (r *Reactor) Schedule(ctx context.Context, tasks []work.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.store.Schedule(ctx, tasks); err != nil {
		return err
	}
	return r.release(ctx)
}
func (r *Reactor) Wake(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.release(ctx)
}
func (r *Reactor) release(ctx context.Context) error {
	if r.hooks.Changed != nil {
		defer r.hooks.Changed(ctx)
	}
	if r.hooks.Availability == nil || r.hooks.Start == nil {
		return errors.New("work execution unavailable")
	}
	av, err := r.hooks.Availability(ctx)
	if err != nil {
		return err
	}
	pending, err := r.store.Cards(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, c := range pending {
		if !c.Scheduling.ReturnRequested || c.Scheduling.ReturnSent || c.Scheduling.Landed || c.Scheduling.Closed || r.hooks.Return == nil {
			continue
		}
		claimed, e := r.store.ClaimReturn(ctx, c.Task.ID, av)
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		if !claimed {
			continue
		}
		run, e := r.hooks.Return(ctx, c)
		if e == nil {
			e = r.store.Start(ctx, run)
		}
		result = errors.Join(result, e)
	}
	cards, err := r.store.Release(ctx, av)
	if err != nil {
		return err
	}
	for _, c := range cards {
		run, e := r.hooks.Start(ctx, c)
		if e == nil {
			e = r.store.Start(ctx, run)
		}
		result = errors.Join(result, e)
	}
	return result
}
func (r *Reactor) Finished(ctx context.Context, id, run, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Run == nil || c.Run.ID != run {
		return work.ErrState
	}
	return r.finished(ctx, c, status)
}

// Completed serializes even a completion that precedes the Start reply behind
// admission, then checks the actual worker turn rather than a caller guess.
func (r *Reactor) Completed(ctx context.Context, id, thread, turn, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Run == nil || c.Run.ThreadID != thread || c.Run.TurnID != turn {
		return work.ErrState
	}
	return r.finished(ctx, c, status)
}
func (r *Reactor) finished(ctx context.Context, c work.Card, status string) error {
	if err := r.store.Finish(ctx, c.Task.ID, c.Run.ID, status); err != nil {
		return err
	}
	observation := r.observe(ctx, c.Task.ID)
	return errors.Join(observation, r.release(ctx))
}
func (r *Reactor) Observe(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(r.observe(ctx, id), r.release(ctx))
}
func (r *Reactor) observe(ctx context.Context, id string) error {
	c, err := r.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Active || c.Scheduling.Landed || c.Scheduling.Closed || c.Run == nil {
		return nil
	}
	if r.hooks.ReadPR == nil {
		return nil
	}
	pr, err := r.hooks.ReadPR(ctx, c)
	if err != nil {
		return err
	}
	if pr.Number == 0 {
		return nil
	} // no pull request is not a merge
	_, err = r.store.ObservePullRequest(ctx, id, c.Run.ID, pr)
	return err
}

func (r *Reactor) Unknown(ctx context.Context, id, thread, turn string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Run == nil || c.Run.ThreadID != thread || c.Run.TurnID != turn {
		return work.ErrState
	}
	err = r.store.UnknownExecution(ctx, id, c.Run.ID)
	if r.hooks.Changed != nil {
		r.hooks.Changed(ctx)
	}
	return err
}
