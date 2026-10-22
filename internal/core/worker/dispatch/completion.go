// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker/antigravity"
)

func (s *Service) finish(ctx context.Context, r Record, runID, status string) error {
	head := ""
	if r.PR != nil {
		head = r.PR.Head
	}
	return s.cfg.DB.Write(ctx, func(tx *canon.Tx) error {
		if err := work.FinishIn(ctx, tx, r.ID, runID, status, head); err != nil {
			return err
		}
		return saveRecord(ctx, tx, r)
	})
}
func saveRecord(ctx context.Context, tx *canon.Tx, r Record) error {
	_, err := tx.ExecContext(ctx, "UPDATE worker_dispatch SET state=?,record=? WHERE id=?", r.State, mustJSON(r), r.ID)
	return err
}

// React uses a committed outbox event, then reads current PR evidence before
// handing the receipt to the manager. Remote content never supplies authority.
func (s *Service) React(ctx context.Context, event work.Completion, card work.Card) (bool, error) {
	var r Record
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE id=?", event.TaskID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &r)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if r.Announced {
		return true, nil
	}
	if card.Run == nil || card.Run.ID != event.Attempt || card.Head != event.Head {
		return true, work.ErrState
	}
	if r.PR != nil {
		repository, _, ok := strings.Cut(strings.TrimPrefix(r.PR.URL, "https://github.com/"), "/pull/")
		if !ok {
			return true, work.ErrInvalid
		}
		current, err := antigravity.ReadPullRequest(ctx, s.cfg.PR, repository, r.TaskDefinition().Branch, r.Definition.Base, event.Head)
		if current.Head != "" && current.Head != event.Head {
			return true, s.cfg.Work.ObserveHead(ctx, event.TaskID, event.Attempt, current.Head)
		}
		if err != nil {
			return true, err
		}
		for i := range current.Reviews {
			current.Reviews[i].Body = ""
		}
		current.Comments = nil
		r.PR = &current
	}
	if s.cfg.Publish == nil {
		return true, work.ErrInvalid
	}
	if err = s.cfg.Publish(ctx, r); err != nil {
		return true, err
	}
	r.Announced = true
	return true, s.save(ctx, r)
}
