// SPDX-License-Identifier: Apache-2.0

// Package lifecycle tracks the activity that determines whether Core is idle.
package lifecycle

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/kit/lease"
)

type State uint8

const (
	Unknown State = iota
	Idle
	Busy
)

type IdleReport struct {
	State    State
	Blocking []lease.Activity
	Reported []lease.Activity
	Err      error
}

// Busy fails closed, including for zero reports and unknown future states.
func (r IdleReport) Busy() bool { return r.State != Idle || r.Err != nil }

type Registry interface {
	lease.Leaser
	Idle(context.Context) IdleReport
	Edges() <-chan struct{}
}

// ReadActivities supplies additional activity from a caller-owned store. It
// must honor the context and return a consistent snapshot safe to copy. Errors
// (including missing records) mean Unknown, never an empty successful read.
type ReadActivities func(context.Context) ([]lease.Activity, error)

// ActivityRegistry combines leases in this process with caller-owned activity.
// Use NewRegistry; a nil or zero registry is unreadable and counts as busy.
type ActivityRegistry struct {
	mu        sync.Mutex
	active    map[*lease.Activity]struct{}
	read      ReadActivities
	edges     chan struct{}
	prepared  *attempt
	committed bool
}

// NewRegistry starts an empty lease registry. A nil reader is appropriate only
// when all activity is represented by its leases; no state path is implicit.
func NewRegistry(read ReadActivities) *ActivityRegistry {
	return &ActivityRegistry{active: make(map[*lease.Activity]struct{}), read: read, edges: make(chan struct{}, 1)}
}

func (r *ActivityRegistry) Acquire(ctx context.Context, activity lease.Activity) (lease.Lease, error) {
	if err := ctx.Err(); err != nil {
		return lease.Lease{}, err
	}
	if err := activity.Validate(); err != nil {
		return lease.Lease{}, err
	}
	if r == nil || r.active == nil {
		return lease.Lease{}, errors.New("activity registry is unavailable")
	}
	r.mu.Lock()
	if r.prepared != nil {
		r.mu.Unlock()
		return lease.Lease{}, ErrFenced
	}
	r.active[&activity] = struct{}{}
	r.mu.Unlock()
	r.Notify()
	return lease.New(func() {
		r.mu.Lock()
		delete(r.active, &activity)
		r.mu.Unlock()
		r.Notify()
	}), nil
}

func (r *ActivityRegistry) Idle(ctx context.Context) IdleReport {
	unknown := func(err error) IdleReport { return IdleReport{State: Unknown, Err: err} }
	if err := ctx.Err(); err != nil {
		return unknown(err)
	}
	if r == nil || r.active == nil {
		return unknown(errors.New("activity registry is unavailable"))
	}
	var activities []lease.Activity
	if r.read != nil {
		read, err := r.read(ctx)
		if err != nil {
			return unknown(err)
		}
		activities = slices.Clone(read)
	}
	if err := ctx.Err(); err != nil {
		return unknown(err)
	}
	r.mu.Lock()
	for activity := range r.active {
		activities = append(activities, *activity)
	}
	r.mu.Unlock()
	// Reports are stable even though lease acquisition and removal are concurrent.
	slices.SortFunc(activities, func(a, b lease.Activity) int {
		if order := strings.Compare(string(a.Class), string(b.Class)); order != 0 {
			return order
		}
		return strings.Compare(a.Reason, b.Reason)
	})
	report := IdleReport{State: Idle}
	for _, activity := range activities {
		if err := activity.Validate(); err != nil {
			return unknown(err)
		}
		switch activity.Class {
		case lease.Hard, lease.Live:
			report.State = Busy
			report.Blocking = append(report.Blocking, activity)
		case lease.Reported:
			report.Reported = append(report.Reported, activity)
		}
	}
	return report
}

// Edges is a coalescing wakeup for one lifecycle observer, not an event log.
// On a wakeup the observer must read Idle again. The channel is never closed.
func (r *ActivityRegistry) Edges() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.edges
}

// Notify wakes the observer after caller-owned activity changes. Acquire and
// Release call it automatically for leases, without waiting for an observer.
func (r *ActivityRegistry) Notify() {
	if r == nil {
		return
	}
	select {
	case r.edges <- struct{}{}:
	default:
	}
}
