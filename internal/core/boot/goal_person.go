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

// Items reads each goal, the goals' actions and the person's runs once for
// all task rows.
func (goalTaskList) Items(ctx context.Context, tx *canon.Tx, person string) ([]runs.ListCandidate, error) {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM work_records WHERE kind='goal' ORDER BY id")
	if err != nil {
		return nil, err
	}
	var goals []goal.Goal
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		var g goal.Goal
		if err = json.Unmarshal(raw, &g); err != nil {
			rows.Close()
			return nil, err
		}
		if g.Person == person {
			goals = append(goals, g)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var actions map[string][]goal.Action
	var personRunList []personRun
	var runsLoaded bool
	load := goalTaskLoad{
		actions: func(goalID, taskID string) ([]goal.Action, error) {
			if actions == nil {
				all, err := allGoalTaskActions(ctx, tx)
				if err != nil {
					return nil, err
				}
				actions = all
			}
			return actions[goalTaskKey(goalID, taskID)], nil
		},
		runs: func(string) ([]personRun, error) {
			if !runsLoaded {
				list, err := personRuns(ctx, tx, person)
				if err != nil {
					return nil, err
				}
				personRunList, runsLoaded = list, true
			}
			return personRunList, nil
		},
	}
	var out []runs.ListCandidate
	for _, g := range goals {
		for _, task := range g.Tasks {
			if task.ID == "" {
				continue
			}
			item, err := goalTaskItem(ctx, tx, g, task, load)
			if err != nil {
				return nil, err
			}
			out = append(out, runs.ListCandidate{ID: goalTaskKey(g.ID, task.ID), Item: item})
		}
	}
	return out, nil
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
		return goalTaskItem(ctx, tx, g, task, goalTaskLoad{
			actions: func(goalID, taskID string) ([]goal.Action, error) { return goalTaskActions(ctx, tx, goalID, taskID) },
			runs:    func(person string) ([]personRun, error) { return personRuns(ctx, tx, person) },
		})
	}
	return runs.ListItem{}, sql.ErrNoRows
}

// goalTaskLoad supplies what a background step's row reads besides its goal.
type goalTaskLoad struct {
	actions func(goalID, taskID string) ([]goal.Action, error)
	runs    func(person string) ([]personRun, error)
}

// goalTaskItem is one task's row. A background step whose run waits shows
// that run's reason.
func goalTaskItem(ctx context.Context, tx *canon.Tx, g goal.Goal, task goal.Task, load goalTaskLoad) (runs.ListItem, error) {
	summary, state := goal.RunningRow(task)
	item := runs.ListItem{Person: g.Person, Agent: g.Agent, Summary: summary, State: state}
	if state == "working" {
		if reason, ok := goal.RunningHold(g, task.ID); ok {
			item.State = "waiting"
			item.Reason = reason
		}
	}
	if task.Kind != goal.BackgroundStep {
		return item, nil
	}
	actions, err := load.actions(g.ID, task.ID)
	if err != nil {
		return runs.ListItem{}, err
	}
	calls := goalTaskCalls(actions, task.Attempt, false)
	if g.Person == "" || len(calls) == 0 {
		return item, nil
	}
	list, err := load.runs(g.Person)
	if err != nil {
		return runs.ListItem{}, err
	}
	runID, err := matchRunID(list, calls)
	if err != nil {
		return runs.ListItem{}, err
	}
	if runID == "" {
		return item, nil
	}
	runItem, err := (runs.RunListSource{}).Read(ctx, tx, runID)
	if err != nil {
		return runs.ListItem{}, err
	}
	if runItem.State == string(runs.Waiting) {
		item.State = "waiting"
		item.Reason = runItem.Reason
	}
	return item, nil
}

// goalQuietWork hides a dispatch card that already has a goal-task row.
type goalQuietWork struct{ inner runs.WorkListSource }

func (s goalQuietWork) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	ids, err := s.inner.Candidates(ctx, tx, person)
	if err != nil {
		return nil, err
	}
	steps, err := goalStepCards(ctx, tx, person)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if i := strings.LastIndexByte(id, ':'); i > 0 && steps[id[:i]] {
			continue
		}
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

// goalStepCards names the cards the person's goals dispatched for their tasks.
// A card carries its dispatch's id, so a goal's worker actions are the link,
// as progress reads them; the dispatch record is marked as a goal's only after
// the card exists.
func goalStepCards(ctx context.Context, tx *canon.Tx, person string) (map[string]bool, error) {
	turns := map[string]string{}
	rows, err := tx.QueryContext(ctx, "SELECT body FROM work_records WHERE kind='goal'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var g goal.Goal
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &g); err != nil {
			return nil, err
		}
		if g.Person == person && g.Turn != "" {
			turns[g.ID] = g.Turn
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT body FROM work_records WHERE kind='action'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cards := map[string]bool{}
	for rows.Next() {
		var raw []byte
		var a goal.Action
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		turn, ok := turns[a.GoalID]
		if !ok || a.TaskID == "" || !goal.WorkerAction(a.Kind) {
			continue
		}
		source := a.ID
		if a.ObserveActionID != "" {
			source = a.ObserveActionID
		}
		cards[work.DispatchID(turn, source)] = true
	}
	return cards, rows.Err()
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
	case goal.TaskStep, "":
		// An omitted kind is a worker task. The card is the result the row covers.
		listID, err := r.goalWorkListID(ctx, person, task, actions)
		if err != nil {
			return "", 0, err
		}
		if r.work == nil {
			return "", 0, runs.ErrListItem
		}
		return r.work.SummaryPage(ctx, listID, offset)
	default:
		return "", 0, runs.ErrListItem
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
		var err error
		id, err = goalTaskRunID(ctx, tx, person, calls)
		return err
	})
	if err != nil || id == "" {
		if err == nil {
			err = runs.ErrListItem
		}
		return "", err
	}
	return id, nil
}

func goalTaskActions(ctx context.Context, tx *canon.Tx, goalID, taskID string) ([]goal.Action, error) {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM work_records WHERE kind='action' AND json_extract(body,'$.goal_id')=? AND json_extract(body,'$.task_id')=?", goalID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var actions []goal.Action
	for rows.Next() {
		var body []byte
		var action goal.Action
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &action); err != nil {
			return nil, err
		}
		if action.GoalID == goalID && action.TaskID == taskID {
			actions = append(actions, action)
		}
	}
	return actions, rows.Err()
}

// allGoalTaskActions groups every goal action by its goal and task.
func allGoalTaskActions(ctx context.Context, tx *canon.Tx) (map[string][]goal.Action, error) {
	rows, err := tx.QueryContext(ctx, "SELECT body FROM work_records WHERE kind='action'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]goal.Action{}
	for rows.Next() {
		var body []byte
		var action goal.Action
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(body, &action); err != nil {
			return nil, err
		}
		if action.GoalID != "" && action.TaskID != "" {
			key := goalTaskKey(action.GoalID, action.TaskID)
			out[key] = append(out[key], action)
		}
	}
	return out, rows.Err()
}

type personRun struct {
	id     string
	record runs.Record
}

// personRuns reads the person's background runs that a step started.
func personRuns(ctx context.Context, tx *canon.Tx, person string) ([]personRun, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, record FROM background_runs WHERE json_extract(record,'$.Origin.Person')=?", person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []personRun
	for rows.Next() {
		var raw []byte
		var item personRun
		if err = rows.Scan(&item.id, &raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &item.record); err != nil {
			return nil, err
		}
		if item.record.ID == item.id && item.record.Origin.Person == person && item.record.StartKey != "" {
			out = append(out, item)
		}
	}
	return out, rows.Err()
}

func goalTaskRunID(ctx context.Context, tx *canon.Tx, person string, calls []string) (string, error) {
	if person == "" || len(calls) == 0 {
		return "", nil
	}
	list, err := personRuns(ctx, tx, person)
	if err != nil {
		return "", err
	}
	return matchRunID(list, calls)
}

// matchRunID finds the run the first call started. Two runs for one call are
// ambiguous.
func matchRunID(runsForPerson []personRun, calls []string) (string, error) {
	for _, call := range calls {
		var matched string
		for _, item := range runsForPerson {
			if item.record.StartKey != runs.CallKey(item.record.Origin, call) {
				continue
			}
			if matched != "" && matched != item.id {
				return "", runs.ErrListItem
			}
			matched = item.id
		}
		if matched != "" {
			return matched, nil
		}
	}
	return "", nil
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
