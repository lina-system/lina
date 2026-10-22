// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

// SubmitDeterministic is Core's precomputed-candidate ingress. Its queue row, revision and receipt
// commit together through the worker's Apply path, without Aux.
func (s *Service) SubmitDeterministic(ctx context.Context, id string, input json.RawMessage) error {
	d, ok := s.cfg.Definitions[id]
	if !ok {
		return jobapi.ErrUnavailable
	}
	if !d.validPayload(input) {
		return jobapi.ErrInvalid
	}
	err := s.apply(ctx, s.cfg.Epoch, func(tx *canon.Tx) error {
		j, err := readJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if !j.Deterministic {
			return jobapi.ErrInvalid
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO moirai_tasks(job,sequence,input) VALUES(?,?,?)", id, j.Watermark+1, []byte(input)); err != nil {
			return err
		}
		return s.applyProposal(ctx, tx, d, jobapi.Proposal{Epoch: s.cfg.Epoch, JobID: id, From: j.Watermark, Through: j.Watermark + 1, Output: input})
	})
	if err == nil && s.cfg.AfterApply != nil {
		// The caller is asking whether adoption committed. Delivery retries
		// use the durable outbox and cannot change that answer.
		_ = s.cfg.AfterApply(ctx)
	}
	return err
}

// DrainDeterministic recovers queued text writes after a crash. Receipts fence
// competing workers. A failed job returns to its owner independently of
// the basic conversation lifecycle.
func (s *Service) DrainDeterministic(ctx context.Context, id string) error {
	for {
		var j jobapi.Job
		var input []byte
		err := s.cfg.Store.Read(ctx, func(tx *canon.Tx) error {
			var err error
			j, err = readJob(ctx, tx, id)
			if err != nil {
				return err
			}
			if !j.Deterministic {
				return jobapi.ErrInvalid
			}
			if !j.Enabled {
				return sql.ErrNoRows
			}
			return tx.QueryRowContext(ctx, "SELECT input FROM moirai_tasks WHERE job=? AND sequence=?", id, j.Watermark+1).Scan(&input)
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = s.Apply(ctx, jobapi.Proposal{Epoch: s.cfg.Epoch, JobID: id, From: j.Watermark, Through: j.Watermark + 1, Output: input})
		if errors.Is(err, jobapi.ErrConflict) {
			// Another caller may have consumed this exact task. Verify progress;
			// never spin on a stale persona revision that has not been consumed.
			var current jobapi.Job
			readErr := s.cfg.Store.Read(ctx, func(tx *canon.Tx) error { var err error; current, err = readJob(ctx, tx, id); return err })
			if readErr != nil {
				return readErr
			}
			if current.Watermark > j.Watermark {
				continue
			}
		}
		if err != nil {
			return err
		}
	}
}
