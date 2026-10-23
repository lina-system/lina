// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"fmt"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/worker"
	"time"
)

// Only a complete terminal snapshot can settle a cut attempt. An adapter with
// no persisted snapshot leaves unknown across restart; a new process is not a
// read of the old one. The work ledger retains each prior receipt and artifact.
func (s *Service) recoverRecord(ctx context.Context, r Record) (Record, error) {
	if r.Restored {
		return r, nil
	}
	snap := r.Snapshot
	if snap == nil || snap.Finish == nil {
		return r, nil
	}
	attempt := max(1, r.Attempt)
	current := reactor.Record{State: "unknown", Thread: snap.Thread, Retries: attempt - 1}
	next, send := reactor.Observe(current, *snap)
	if next.State == "unknown" || next.State == "parked" {
		return r, nil
	}
	status := "failed"
	if snap.Finish.Status == worker.Done {
		status = "completed"
	}
	if err := s.cfg.Work.Finish(ctx, r.ID, fmt.Sprintf("%s:%d", r.ID, attempt), status); err != nil {
		return r, err
	}
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
	} else {
		r.State = "finished"
	}
	return r, s.save(ctx, r)
}
