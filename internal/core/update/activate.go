// SPDX-License-Identifier: Apache-2.0
package update

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

// ActivationOps are composition boundaries, supplied by the command. Drain
// must use Core's admission fence, Snapshot must verify a compatible generation,
// and Smoke must join its candidate before returning, even on failure. Resume
// starts only the service owned by this install, never an installed user tool.
type ActivationOps struct {
	Drain    func(context.Context) error
	Snapshot func(context.Context) (string, error)
	Swap     func(context.Context) error
	Smoke    func(context.Context) error
	Rollback func(context.Context) error
	Resume   func(context.Context) error
}

// Activate owns the durable step ordering. Core independently claims startup
// and commits READY; the observer cannot erase that fact after a crash.
func Activate(ctx context.Context, s *ctlfiles.Store, r ctlfiles.UpdateRecord, ops ActivationOps) (ctlfiles.UpdateRecord, error) {
	if ops.Drain == nil || ops.Snapshot == nil || ops.Swap == nil || ops.Smoke == nil || ops.Rollback == nil || ops.Resume == nil {
		return r, errors.New("update_missing_operation")
	}
	save := func(phase string) error { r.Phase = phase; return s.WriteUpdate(r) }
	hold := func(cause string, err error) (ctlfiles.UpdateRecord, error) {
		if errors.Is(err, syscall.ENOSPC) {
			cause = "update_disk_full"
		}
		r.Cause = cause
		r.Phase = "held"
		return r, errors.Join(fmt.Errorf("%s: %w", cause, err), s.WriteUpdate(r))
	}
	fail := func(cause string, err error, rollback bool) (ctlfiles.UpdateRecord, error) {
		observed, readErr := s.ReadUpdate()
		if readErr != nil {
			return hold(cause, errors.Join(err, readErr))
		}
		if observed != nil {
			r = *observed
		}
		if errors.Is(err, syscall.ENOSPC) {
			cause = "update_disk_full"
		}
		r.Cause = cause
		if r.ReachedReady {
			return hold(cause, err)
		}
		if r.ForwardMigration && (r.Phase == "starting" || r.Phase == "booting") {
			return hold("update_irreversible_migration", err)
		}
		if rollback {
			if e := save("rollback"); e != nil {
				return hold("update_rollback_failed", errors.Join(err, e))
			}
			if e := ops.Rollback(ctx); e != nil {
				return hold("update_rollback_failed", errors.Join(err, e))
			}
		}
		if e := save("rolled_back"); e != nil {
			return hold(cause, errors.Join(err, e))
		}
		if e := ops.Resume(ctx); e != nil {
			return hold("update_resume_failed", errors.Join(err, e))
		}
		return r, fmt.Errorf("%s: %w", cause, err)
	}
	if err := save("draining"); err != nil {
		return r, err
	}
	if err := ops.Drain(ctx); err != nil {
		return hold("update_drain_failed", err)
	}
	if err := save("drained"); err != nil {
		return hold("update_disk_full", err)
	}
	generation, err := ops.Snapshot(ctx)
	if err != nil {
		return fail("update_snapshot_failed", err, false)
	}
	r.Generation = generation
	if err := save("snapshot"); err != nil {
		return hold("update_disk_full", err)
	}
	if err := save("swapping"); err != nil {
		return hold("update_disk_full", err)
	}
	if err := ops.Swap(ctx); err != nil {
		return fail("update_swap_failed", err, true)
	}
	if err := save("starting"); err != nil {
		return hold("update_disk_full", err)
	}
	if err := ops.Smoke(ctx); err != nil {
		return fail("update_smoke_failed", err, true)
	}
	observed, err := s.ReadUpdate()
	if err != nil {
		return hold("update_start_failed", err)
	}
	if observed == nil || !observed.ReachedReady {
		return fail("update_start_failed", errors.New("candidate supplied no READY receipt"), true)
	}
	r = *observed
	if err := save("complete"); err != nil {
		return hold("update_disk_full", err)
	}
	if err := ops.Resume(ctx); err != nil {
		return hold("update_resume_failed", err)
	}
	return r, nil
}
