// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"time"
)

const SafetySweep = 5 * time.Minute

type Reconciler struct {
	done   chan struct{}
	errors chan error
}

func (r *Reconciler) Done() <-chan struct{} { return r.done }
func (r *Reconciler) Errors() <-chan error  { return r.errors }

// StartReconciler is the common event/startup/safety-sweep reaction path. The
// optional sweep channel lets Core share its clock and tests advance it without
// timing luck. Reconcile must derive from committed state, not wake counts.
func StartReconciler(ctx context.Context, j *Journal, reconcile func(context.Context) error, sweep <-chan time.Time) *Reconciler {
	return startReconciler(ctx, j, reconcile, sweep, nil)
}
func startReconciler(ctx context.Context, j *Journal, reconcile func(context.Context) error, sweep <-chan time.Time, refresh func(context.Context) error) *Reconciler {
	r := &Reconciler{done: make(chan struct{}), errors: make(chan error, 1)}
	go func() {
		defer close(r.done)
		defer close(r.errors)
		if sweep == nil {
			timer := time.NewTicker(SafetySweep)
			defer timer.Stop()
			sweep = timer.C
		}
		refreshDue := true
		for {
			if refreshDue && refresh != nil {
				if err := refresh(ctx); err != nil && ctx.Err() == nil {
					select {
					case r.errors <- err:
					default:
					}
				}
			}
			refreshDue = false
			if ctx.Err() != nil {
				return
			}
			if err := reconcile(ctx); err != nil && ctx.Err() == nil {
				select {
				case r.errors <- err:
				default:
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-j.Wake():
			case _, ok := <-sweep:
				refreshDue = ok
				if !ok {
					sweep = nil
				}
			}
		}
	}()
	return r
}
