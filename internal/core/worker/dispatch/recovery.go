// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
)

// recoveryRecord derives a new attempt, never a replay of an earlier command.
// The caller commits it atomically with the old attempt's terminal observation,
// before either the completion reactor or the scheduler can act on that event.
func recoveryRecord(r Record) Record {
	snap := r.Snapshot
	if r.Restored || snap == nil || snap.Finish == nil {
		return r
	}
	attempt := max(1, r.Attempt)
	next, send := reactor.Observe(reactor.Record{State: "unknown", Thread: snap.Thread, Retries: attempt - 1}, *snap)
	r.State = "unknown"
	if send {
		r.Attempt = attempt + 1
		r.State = "queued"
		r.Code = "recovering"
		r.Snapshot = nil
		r.Announced = false
	} else if next.State == "decision" {
		r.State = "decision"
		r.Code = snap.Finish.Code
		r.Announced = false
		if r.DecisionAt == 0 {
			r.DecisionAt = time.Now().UnixNano()
		}
	} else if next.State == "finished" {
		r.State = "finished"
	}
	return r
}

// Only a committed complete snapshot can settle a cut headless attempt. An
// absent snapshot stays unknown; starting a process is not a read of the old one.
func (s *Service) recoverRecord(ctx context.Context, r Record) (Record, error) {
	next := recoveryRecord(r)
	if r.Restored || r.Snapshot == nil || r.Snapshot.Finish == nil || next.State == "unknown" {
		return r, nil
	}
	status := "failed"
	if r.Snapshot.Finish.Status == worker.Done {
		status = "completed"
	}
	head := ""
	if r.PR != nil {
		head = r.PR.Head
	}
	err := s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		if err := work.FinishIn(ctx, tx, r.ID, fmt.Sprintf("%s:%d", r.ID, max(1, r.Attempt)), status, head); err != nil {
			return err
		}
		return saveRecord(ctx, tx, next)
	})
	return next, err
}
