// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// GoalRecord observes an existing dispatch without starting or resuming it.
func (s *Service) GoalRecord(ctx context.Context, origin Origin, action string) (Record, error) {
	var r Record
	err := s.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE id=?", admissionID(origin, action)).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &r)
	})
	if err == nil && r.Origin != origin {
		err = work.ErrInvalid
	}
	return r, err
}

// RunGoal is the trusted goal runner's entry after its durable action claim.
// It shares admission, preset checks, manager assignment and the worker adapter
// with dispatch_work. The action key never restarts running or uncertain work.
func (s *Service) RunGoal(ctx context.Context, origin Origin, action string, d Definition) (Record, error) {
	if action == "" {
		return Record{}, work.ErrInvalid
	}
	r, err := s.GoalRecord(ctx, origin, action)
	if err == nil {
		canonical, e := filepath.EvalSymlinks(d.Repository)
		if e != nil {
			return Record{}, e
		}
		d.Repository = canonical
		if !reflect.DeepEqual(r.Definition, d) {
			return Record{}, work.ErrState
		}
		if r.State != "queued" && r.State != "admitting" {
			return r, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Record{}, err
	}
	if errors.Is(err, sql.ErrNoRows) || r.State == "admitting" {
		result := s.Tool(origin).Execute(ctx, provider.ToolCall{ID: action, Name: "dispatch_work", Arguments: string(mustJSON(d))})
		if result.Code != loop.OK {
			return Record{}, work.ErrInvalid
		}
		r, err = s.GoalRecord(ctx, origin, action)
		if err != nil {
			return Record{}, err
		}
	}
	s.mu.Lock()
	if s.running[r.ID] {
		s.mu.Unlock()
		return s.GoalRecord(ctx, origin, action)
	}
	s.running[r.ID] = true
	s.mu.Unlock()
	ownsReservation := true
	defer func() {
		if ownsReservation {
			s.mu.Lock()
			delete(s.running, r.ID)
			s.mu.Unlock()
		}
	}()
	r.Goal, r.State = true, "running"
	r.GoalAction = action
	if err := s.save(ctx, r); err != nil {
		return Record{}, err
	}
	// execute releases this reservation and can wake the next attempt. Do not
	// remove that newer reservation after reading the first attempt's result.
	ownsReservation = false
	s.execute(ctx, r)
	return s.GoalRecord(ctx, origin, action)
}
