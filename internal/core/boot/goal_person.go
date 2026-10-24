// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

// goalTaskList is the running-list row for each task of a goal. A landing
// changes the row's state. It is not a notice.
type goalTaskList struct{}

func goalTaskKey(goalID, taskID string) string { return goalID + "\x1f" + taskID }

func (goalTaskList) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM work_records WHERE kind='goal' ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
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
			ids = append(ids, goalTaskKey(g.ID, task.ID))
		}
	}
	return ids, rows.Err()
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

// goalResultPage reads the result of the worker card or background run that a
// goal-task row replaced. The published id is the goal and task, not that source.
func (r *conversationRuntime) goalResultPage(ctx context.Context, person, id string, offset int64) (string, int64, error) {
	goalID, taskID, ok := strings.Cut(id, "\x1f")
	if !ok || goalID == "" || taskID == "" || person == "" {
		return "", 0, runs.ErrInvalid
	}
	var g goal.Goal
	var actions []goal.Action
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		var raw []byte
		if err := tx.QueryRowContext(ctx, "SELECT body FROM work_records WHERE kind='goal' AND id=?", goalID).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &g); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT body FROM work_records WHERE kind='action' AND json_extract(body,'$.goal_id')=? AND json_extract(body,'$.task_id')=?", goalID, taskID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body []byte
			var action goal.Action
			if err = rows.Scan(&body); err != nil {
				return err
			}
			if err = json.Unmarshal(body, &action); err != nil {
				return err
			}
			if action.GoalID == goalID && action.TaskID == taskID {
				actions = append(actions, action)
			}
		}
		return rows.Err()
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, runs.ErrListItem
	}
	if err != nil {
		return "", 0, err
	}
	if g.Person != person {
		return "", 0, runs.ErrScope
	}
	var task goal.Task
	found := false
	for _, item := range g.Tasks {
		if item.ID == taskID {
			task = item
			found = true
			break
		}
	}
	if !found {
		return "", 0, runs.ErrListItem
	}
	switch task.Kind {
	case goal.BackgroundStep:
		runID, err := r.goalBackgroundRun(ctx, person, task, actions)
		if err != nil {
			return "", 0, err
		}
		if r.background == nil || r.background.service == nil {
			return "", 0, runs.ErrListItem
		}
		return r.background.service.ResultPage(ctx, person, runID, offset)
	case goal.WaitStep:
		return "", 0, runs.ErrState
	default:
		if task.Kind != "" && task.Kind != goal.TaskStep {
			return "", 0, runs.ErrListItem
		}
		listID, err := r.goalWorkListID(ctx, person, task, actions)
		if err != nil {
			return "", 0, err
		}
		if r.work == nil {
			return "", 0, runs.ErrListItem
		}
		return r.work.SummaryPage(ctx, listID, offset)
	}
}

// The dispatch record keeps the action that admitted the card. Opening uses
// that card, including when a later observation names the original action.
func (r *conversationRuntime) goalWorkListID(ctx context.Context, person string, task goal.Task, actions []goal.Action) (string, error) {
	calls := goalTaskCalls(actions, task.Attempt, true)
	if len(calls) == 0 {
		return "", runs.ErrListItem
	}
	var listID string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		for _, call := range calls {
			rows, err := tx.QueryContext(ctx, "SELECT record FROM worker_dispatch WHERE json_extract(record,'$.goal_action')=?", call)
			if err != nil {
				return err
			}
			var records []dispatch.Record
			for rows.Next() {
				var raw []byte
				var record dispatch.Record
				if err = rows.Scan(&raw); err != nil {
					rows.Close()
					return err
				}
				if err = json.Unmarshal(raw, &record); err != nil {
					rows.Close()
					return err
				}
				if record.Goal && record.GoalAction == call && record.Origin.Person == person && record.ID != "" {
					records = append(records, record)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			var id string
			for _, record := range records {
				var cardRaw []byte
				err = tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", record.ID).Scan(&cardRaw)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				var card work.Card
				if err = json.Unmarshal(cardRaw, &card); err != nil {
					return err
				}
				if card.Task.PersonID != person {
					return runs.ErrScope
				}
				generation := 0
				if card.Run != nil {
					generation = card.Run.Generation
				}
				next := record.ID + ":" + strconv.Itoa(generation)
				if id != "" && id != next {
					return runs.ErrListItem
				}
				id = next
			}
			if id != "" {
				listID = id
				return nil
			}
		}
		return nil
	})
	if err != nil || listID == "" {
		if err == nil {
			err = runs.ErrListItem
		}
		return "", err
	}
	return listID, nil
}

func (r *conversationRuntime) goalBackgroundRun(ctx context.Context, person string, task goal.Task, actions []goal.Action) (string, error) {
	calls := goalTaskCalls(actions, task.Attempt, false)
	if len(calls) == 0 {
		return "", runs.ErrListItem
	}
	var id string
	err := r.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id, record FROM background_runs WHERE json_extract(record,'$.Origin.Person')=?", person)
		if err != nil {
			return err
		}
		defer rows.Close()
		type stored struct {
			id     string
			record runs.Record
		}
		var runsForPerson []stored
		for rows.Next() {
			var raw []byte
			var item stored
			if err = rows.Scan(&item.id, &raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &item.record); err != nil {
				return err
			}
			if item.record.ID == item.id && item.record.Origin.Person == person && item.record.StartKey != "" {
				runsForPerson = append(runsForPerson, item)
			}
		}
		if err = rows.Err(); err != nil {
			return err
		}
		for _, call := range calls {
			var matched string
			for _, item := range runsForPerson {
				if item.record.StartKey != runs.CallKey(item.record.Origin, call) {
					continue
				}
				if matched != "" && matched != item.id {
					return runs.ErrListItem
				}
				matched = item.id
			}
			if matched != "" {
				id = matched
				return nil
			}
		}
		return nil
	})
	if err != nil || id == "" {
		if err == nil {
			err = runs.ErrListItem
		}
		return "", err
	}
	return id, nil
}

// goalTaskCalls orders the actions that may own the task's result. The current
// attempt comes first, then older remaining actions. An action without a card
// or run is skipped by the caller. An observation names the action that started
// the work. A superseded action is not a result.
func goalTaskCalls(actions []goal.Action, attempt int, worker bool) []string {
	type candidate struct {
		action goal.Action
		match  bool
	}
	var items []candidate
	for _, action := range actions {
		if action.Superseded || action.ID == "" {
			continue
		}
		if worker {
			if !goal.WorkerAction(action.Kind) {
				continue
			}
		} else if action.Kind != goal.Assign {
			continue
		}
		items = append(items, candidate{action, action.Attempt == attempt})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].match != items[j].match {
			return items[i].match
		}
		return items[i].action.Sequence > items[j].action.Sequence
	})
	var calls []string
	seen := map[string]bool{}
	for _, item := range items {
		call := item.action.ID
		if item.action.ObserveActionID != "" {
			call = item.action.ObserveActionID
		}
		if call == "" || seen[call] {
			continue
		}
		seen[call] = true
		calls = append(calls, call)
	}
	return calls
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
