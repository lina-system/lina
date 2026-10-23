// SPDX-License-Identifier: Apache-2.0
// Scheduler releases delegated work when authoritative observations change.
package reactor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/work"
)

// ErrNotStarted marks a failure observed before any worker start was sent.
// All other start errors retain reservations because their outcome is unknown.
var ErrNotStarted = errors.New("worker start was not sent")

// Hooks are bound by boot to worker and forge adapters. Unless a start error
// wraps ErrNotStarted, its durable reservation remains held for reconciliation.
type Hooks struct {
	Availability func(context.Context) (work.Availability, error)
	Start        func(context.Context, work.Card) (work.Run, error)
	ReadPR       func(context.Context, work.Card) (work.PullRequest, error)
	Return       func(context.Context, work.Card) (work.Run, error)
	Changed      func(context.Context)
}
type Scheduler struct {
	mu    sync.Mutex
	store *work.Service
	hooks Hooks
}

func NewScheduler(s *work.Service, h Hooks) *Scheduler { return &Scheduler{store: s, hooks: h} }
func (r *Scheduler) Schedule(ctx context.Context, tasks []work.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.store.Schedule(ctx, tasks); err != nil {
		return err
	}
	return r.release(ctx)
}
func (r *Scheduler) Wake(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.release(ctx)
}

// Stop serializes durable cancellation with starts and completions. Callers
// interrupt workers only after every selected card has been closed.
func (r *Scheduler) Stop(ctx context.Context, person, conversation, reference string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count, err := r.store.StopScheduled(ctx, person, conversation, reference)
	if err == nil && r.hooks.Changed != nil {
		r.hooks.Changed(ctx)
	}
	return count, err
}
func (r *Scheduler) release(ctx context.Context) error {
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
	for {
		cards, err := r.store.Release(ctx, av)
		if err != nil {
			return errors.Join(result, err)
		}
		freed := false
		for _, c := range cards {
			run, e := r.hooks.Start(ctx, c)
			if errors.Is(e, ErrNotStarted) {
				// Release already committed the reservation. A canceled caller
				// must not keep the slot and area; the next wake uses what this
				// cleanup frees. It does not start another worker on this call.
				settle, cancelSettle := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				settled := r.store.StartFailed(settle, c.Task.ID)
				cancelSettle()
				freed = freed || settled == nil
				e = errors.Join(e, settled)
			} else if e == nil {
				e = r.store.Start(ctx, run)
			}
			result = errors.Join(result, e)
		}
		if !freed {
			break
		}
		// Known failures settle durably and never retry here. Reconsider other
		// tasks immediately using the slots and areas they just freed.
	}
	return result
}
func (r *Scheduler) Finished(ctx context.Context, id, run, status string) error {
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
func (r *Scheduler) Completed(ctx context.Context, id, thread, turn, status string) error {
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
func (r *Scheduler) finished(ctx context.Context, c work.Card, status string) error {
	if err := r.store.Finish(ctx, c.Task.ID, c.Run.ID, status); err != nil {
		return err
	}
	observation := r.observe(ctx, c.Task.ID)
	return errors.Join(observation, r.release(ctx))
}
func (r *Scheduler) Observe(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return errors.Join(r.observe(ctx, id), r.release(ctx))
}
func (r *Scheduler) observe(ctx context.Context, id string) error {
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

func (r *Scheduler) Unknown(ctx context.Context, id, thread, turn string) error {
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

func (r *Scheduler) InputNeeded(ctx context.Context, id, thread, turn string) error {
	return r.inputState(ctx, id, thread, turn, false)
}
func (r *Scheduler) Answered(ctx context.Context, id, thread, turn string) error {
	return r.inputState(ctx, id, thread, turn, true)
}
func (r *Scheduler) inputState(ctx context.Context, id, thread, turn string, answered bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, err := r.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if c.Run == nil || c.Run.ThreadID != thread || c.Run.TurnID != turn {
		return work.ErrState
	}
	if answered {
		err = r.store.WorkerInputAnswered(ctx, id, c.Run.ID)
	} else {
		err = r.store.WorkerInputNeeded(ctx, id, c.Run.ID)
	}
	if r.hooks.Changed != nil {
		r.hooks.Changed(ctx)
	}
	return err
}
