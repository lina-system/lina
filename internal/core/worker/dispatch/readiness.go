// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/provider"
)

// ReadinessFailure records a definitive refusal before a worker session exists.
// It contains Core-owned codes, never the remote error's message or data.
type ReadinessFailure struct {
	Worker  string `json:"worker"`
	Version string `json:"version"`
	Code    string `json:"code"`
}

func (s *Service) needsSignIn(ctx context.Context, r *Record) error {
	r.Code = "needs_sign_in"
	r.ReadinessFailures = append(r.ReadinessFailures, ReadinessFailure{Worker: r.Worker, Version: r.Version, Code: r.Code})
	// Tried already excludes this worker for this task. A refusal before a
	// prompt says nothing about quota or readiness for a later task: the person
	// may sign in externally, and ACP has no quota observation to clear a stop.
	return s.save(ctx, *r)
}

// recoverReadinessQuota removes the old unmeasured, worker-wide stop only when
// every recorded attempt at that worker was a definitive readiness refusal.
// A launched/uncertain attempt or measured quota makes attribution ambiguous;
// neither is permission to clear a quota block. Run before dispatch starts.
func (s *Service) recoverReadinessQuota(ctx context.Context, records []Record) error {
	refused, uncertain := map[string]bool{}, map[string]bool{}
	for _, r := range records {
		failures := map[string]bool{}
		for _, failure := range r.ReadinessFailures {
			if failure.Code == "needs_sign_in" {
				failures[failure.Worker] = true
				refused[failure.Worker] = true
				// Recovery and correction retain failures while resetting Tried.
				// That history cannot prove the worker never executed a later turn.
				if r.Recoveries > 0 || r.ReturnReceipt != "" {
					uncertain[failure.Worker] = true
				}
			}
		}
		for _, name := range r.Tried {
			if !failures[name] {
				uncertain[name] = true
			}
		}
		if r.Worker != "" && !failures[r.Worker] || r.Thread != nil || r.Snapshot != nil {
			uncertain[r.WorkerName()] = true
		}
	}
	return s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		for name := range refused {
			if uncertain[name] {
				continue
			}
			var raw []byte
			err := tx.QueryRowContext(ctx, "SELECT rows FROM worker_quota WHERE worker=?", name).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			var rows []provider.QuotaRow
			if err = json.Unmarshal(raw, &rows); err != nil {
				return err
			}
			if len(rows) != 1 {
				continue
			}
			q := rows[0]
			if q.Provider != "" || q.Account != "" || q.LimitID != "" || q.Bucket != "" || q.Window != provider.UnknownWindow || q.UsedPct != nil || q.ResetsAt != nil || q.State != provider.QuotaBlocked || q.Source != provider.ErrorCode {
				continue
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM worker_quota WHERE worker=?", name); err != nil {
				return err
			}
		}
		return nil
	})
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
