// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/work/goal"
)

// goalTaskList is the running-list row for each task of a goal. A landing
// changes the row's state. It is not a notice.
type goalTaskList struct{}

func goalTaskKey(goalID, taskID string) string { return goalID + "\x1f" + taskID }

func (s goalTaskList) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	items, err := s.Items(ctx, tx, person)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids, nil
}

// Items decodes each goal once for all of its task rows.
func (goalTaskList) Items(ctx context.Context, tx *canon.Tx, person string) ([]runs.ListCandidate, error) {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM work_records WHERE kind='goal' ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runs.ListCandidate
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var g goal.Goal
		if err = json.Unmarshal(raw, &g); err != nil {
			return nil, err
		}
		if g.Person != person {
			continue
		}
		for _, task := range g.Tasks {
			if task.ID == "" {
				continue
			}
			summary, state := goal.RunningRow(task)
			out = append(out, runs.ListCandidate{ID: goalTaskKey(g.ID, task.ID), Item: runs.ListItem{Person: g.Person, Agent: g.Agent, Summary: summary, State: state}})
		}
	}
	return out, rows.Err()
}

func (goalTaskList) Read(ctx context.Context, tx *canon.Tx, id string) (runs.ListItem, error) {
	goalID, taskID, ok := strings.Cut(id, "\x1f")
	if !ok || goalID == "" || taskID == "" {
		return runs.ListItem{}, runs.ErrListItem
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM work_records WHERE kind='goal' AND id=?", goalID).Scan(&raw); err != nil {
		return runs.ListItem{}, err
	}
	var g goal.Goal
	if err := json.Unmarshal(raw, &g); err != nil {
		return runs.ListItem{}, err
	}
	for _, task := range g.Tasks {
		if task.ID != taskID {
			continue
		}
		summary, state := goal.RunningRow(task)
		return runs.ListItem{Person: g.Person, Agent: g.Agent, Summary: summary, State: state}, nil
	}
	return runs.ListItem{}, sql.ErrNoRows
}

// goalQuietWork hides a dispatch card that already has a goal-task row.
type goalQuietWork struct{ inner runs.WorkListSource }

func (s goalQuietWork) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	ids, err := s.inner.Candidates(ctx, tx, person)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		goalCard, err := dispatchGoal(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !goalCard {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s goalQuietWork) Read(ctx context.Context, tx *canon.Tx, id string) (runs.ListItem, error) {
	return s.inner.Read(ctx, tx, id)
}

func dispatchGoal(ctx context.Context, tx *canon.Tx, listID string) (bool, error) {
	i := strings.LastIndexByte(listID, ':')
	if i <= 0 {
		return false, nil
	}
	task := listID[:i]
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT record FROM worker_dispatch WHERE id=?", task).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var rec struct {
		Goal bool `json:"goal"`
	}
	if err = json.Unmarshal(raw, &rec); err != nil {
		return false, err
	}
	return rec.Goal, nil
}

// goalQuietRuns drops a background run that is a goal task. The goal row is
// the one the person sees, and its landing sends no notice.
type goalQuietRuns struct{ inner runs.RunListSource }

func (s goalQuietRuns) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	ids, err := s.inner.Candidates(ctx, tx, person)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		owned, err := goalOwnsRunTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !owned {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s goalQuietRuns) Read(ctx context.Context, tx *canon.Tx, id string) (runs.ListItem, error) {
	return s.inner.Read(ctx, tx, id)
}

func (r *conversationRuntime) goalOwnsRun(ctx context.Context, id string) (bool, error) {
	var owned bool
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		owned, err = goalOwnsRunTx(ctx, tx, id)
		return err
	})
	return owned, err
}

func goalOwnsRunTx(ctx context.Context, tx *canon.Tx, id string) (bool, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT record FROM background_runs WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var rec runs.Record
	if err = json.Unmarshal(raw, &rec); err != nil {
		return false, err
	}
	if rec.StartKey == "" {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM work_records WHERE kind='action'")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var call string
		if err = rows.Scan(&call); err != nil {
			return false, err
		}
		if rec.StartKey == runs.CallKey(rec.Origin, call) {
			return true, nil
		}
	}
	return false, rows.Err()
}
