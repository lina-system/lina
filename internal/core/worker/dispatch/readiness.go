// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
)

// ReadinessFailure records a definitive refusal before a worker session exists.
// It contains Core-owned codes, never the remote error's message or data.
type ReadinessFailure struct {
	Worker  string `json:"worker"`
	Version string `json:"version"`
	Code    string `json:"code"`
}

func (s *Service) needsSignIn(ctx context.Context, r *Record, adapter worker.Adapter) error {
	r.Code = "needs_sign_in"
	r.ReadinessFailures = append(r.ReadinessFailures, ReadinessFailure{Worker: r.Worker, Version: r.Version, Code: r.Code})
	if err := s.save(ctx, *r); err != nil {
		return err
	}
	// Use the same eligibility block and two-worker limit as a login failure
	// during a turn. Readiness does not grant an extra attempt or a retry.
	return s.park(ctx, adapter)
}

func (s *Service) nextRun(ctx context.Context, r Record) (work.Run, error) {
	card, err := s.cfg.Work.Get(ctx, r.ID)
	if err != nil {
		return work.Run{}, err
	}
	generation := 1
	if card.Run != nil {
		generation = card.Run.Generation + 1
	}
	// A refused session consumed a chooser attempt but did not start a run.
	return work.Run{ID: fmt.Sprintf("%s:%d", r.ID, generation), TaskID: r.ID, Generation: generation, Worker: r.Worker, NodeID: s.cfg.Main}, nil
}

func (s *Service) reportReadiness(ctx context.Context, r Record, run work.Run, sequence *uint64) error {
	for _, failure := range r.ReadinessFailures {
		*sequence++
		if err := s.cfg.Work.ReportProgress(ctx, r.ID, run.ID, *sequence, failure.Worker+" needs sign-in."); err != nil {
			return err
		}
	}
	return nil
}
