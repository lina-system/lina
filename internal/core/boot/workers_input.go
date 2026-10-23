// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/rand"
	"errors"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/recovery"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/tools"
)

// The caller holds control. Keep recovery and notification updates outside the
// interval from uncertain admission through the returned binding checkpoint.
func (e *workerSession) inputWorker(ctx context.Context, snap worker.ThreadSnapshot, text string, activity lease.Lease) (tools.State, error) {
	e.recoveryMu.Lock()
	defer e.recoveryMu.Unlock()
	if state := e.recovery.State; state == "unknown" || state == "retrying" || state == "decision" || state == "parked" {
		activity.Release()
		return tools.Failed, errors.New("worker effects require reconciliation")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.scheduled {
		// Scheduled input may only steer, even if completion races the snapshot.
		steerer, ok := e.adapter.(interface {
			Steer(context.Context, worker.Thread, worker.Input) error
		})
		if !ok {
			return tools.Failed, errors.New("worker does not support active-turn steering")
		}
		err := steerer.Steer(ctx, snap.Thread, worker.Input{Text: text})
		if errors.Is(err, worker.ErrInputRefused) {
			return tools.Failed, err
		}
		if err != nil {
			return tools.Unknown, err
		}
		return tools.Applied, nil
	}
	work := e.origin.work
	idle := snap.State == worker.Idle
	// Only an undispatched idle input can release this new reservation.
	release := func() error {
		if !idle {
			return nil
		}
		activity.Release()
		if work != nil && e.reservation != "" {
			if err := work.ReleaseWorker(context.WithoutCancel(ctx), e.reservation); err != nil {
				return err
			}
			e.reservation, e.reservationTurn = "", ""
		}
		return nil
	}
	if work != nil {
		var err error
		if idle {
			if e.reservation != "" && e.reservationTurn == snap.Thread.TurnID {
				if err = work.ReleaseWorker(ctx, e.reservation); err != nil {
					activity.Release()
					return tools.Failed, err
				}
				e.reservation, e.reservationTurn = "", ""
			}
			reservation := rand.Text()
			if err = work.ReserveWorker(ctx, reservation, e.workdir); err == nil {
				e.reservation = reservation
				e.reservationTurn = "unknown:" + reservation
			}
		} else {
			err = work.CheckWorker(ctx, e.reservation)
		}
		if err != nil {
			activity.Release()
			return tools.Failed, err
		}
	}
	prior := e.recovery
	store := recovery.Recovery{DB: e.origin.db}
	if prior.ID != "" {
		intent := prior
		intent.State, intent.Thread.TurnID, intent.Finish = "unknown", "", nil
		intent.Reservation, intent.ObservedAt = e.reservation, 0
		if err := store.Put(ctx, intent); err != nil {
			return tools.Failed, errors.Join(err, release())
		}
		e.recovery = intent
	}
	err := e.adapter.Input(ctx, snap.Thread, worker.Input{Text: text})
	if errors.Is(err, worker.ErrInputRefused) {
		if releaseErr := release(); releaseErr != nil {
			return tools.Unknown, errors.Join(err, releaseErr)
		}
		if prior.ID != "" {
			prior.Reservation = e.reservation
			if restoreErr := store.Put(context.WithoutCancel(ctx), prior); restoreErr != nil {
				return tools.Unknown, errors.Join(err, restoreErr)
			}
			e.recovery = prior
		}
		return tools.Failed, err
	}
	// Even an active snapshot can become idle inside Input and start a new
	// turn. Persist its exact binding without waiting for a started event.
	key := "unknown:" + rand.Text()
	if err == nil {
		current, readErr := e.adapter.Snapshot(ctx, snap.Thread)
		if readErr != nil {
			err = readErr
		} else if current.Thread.TurnID == "" || current.State == worker.Unknown || current.State == worker.Dead {
			err = errors.New("worker input binding is unknown")
		} else {
			if prior.ID != "" {
				next := e.recovery
				next.Thread, next.State = current.Thread, "running"
				if err = store.Put(context.WithoutCancel(ctx), next); err == nil {
					e.recovery = next
				}
			}
			if err == nil {
				key = current.Thread.TurnID
			}
		}
	}
	e.reservationTurn = key
	if idle {
		e.active[key] = activity
	} else if key != snap.Thread.TurnID {
		e.active[key] = e.active[snap.Thread.TurnID]
		delete(e.active, snap.Thread.TurnID)
	}
	if err != nil {
		return tools.Unknown, err
	}
	return tools.Applied, nil
}
