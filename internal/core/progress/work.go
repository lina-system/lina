// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
)

// WorkCards rereads the disposable projection rebuilt from the work ledger at
// startup. Callers join these observations to plan membership, never use worker
// claims as a plan or a unit verdict.
func WorkCards(ctx context.Context, tx *canon.Tx, person string, agents map[string]string) (map[string]work.Card, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM work_cards ORDER BY task_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cards := map[string]work.Card{}
	for rows.Next() {
		var raw []byte
		var card work.Card
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &card); err != nil {
			return nil, err
		}
		if _, ok := agents[card.Task.AgentID]; !ok || card.Task.PersonID != "" && card.Task.PersonID != person {
			continue
		}
		cards[card.Task.ID] = card
	}
	return cards, rows.Err()
}

// UnplannedWork keeps explicitly handed-off work visible before a confirmed
// plan is recorded. Its title and terminal verdict are observed; its stage and
// task denominator remain unknown. A goal link groups its children.
func UnplannedWork(cards map[string]work.Card) []Unit {
	goals := map[string]bool{}
	for _, c := range cards {
		if c.Task.GoalID != "" {
			goals[c.Task.GoalID] = true
		}
	}
	units := []Unit{}
	for id, c := range cards {
		u := Unit{ID: id, Parent: c.Task.GoalID, Kind: "project", Name: c.Task.Title, Active: c.Run != nil || c.State == "input_needed"}
		if goals[id] {
			u.Kind = "goal"
		}
		if c.State == "input_needed" {
			u.Waiting = 1
		}
		if c.Verdict != "" {
			done := c.State == "done"
			u.Done = &done
		}
		units = append(units, u)
	}
	return units
}

// WorkUnits shares the running list's generation-bound acknowledgement. Seeing
// a prior attempt never hides a new execution of the same work definition.
func WorkUnits(ctx context.Context, tx *canon.Tx, person string, agents map[string]string) ([]Unit, error) {
	cards, err := WorkCards(ctx, tx, person, agents)
	if err != nil {
		return nil, err
	}
	units := UnplannedWork(cards)
	for i := range units {
		card := cards[units[i].ID]
		generation := 0
		if card.Run != nil {
			generation = card.Run.Generation
		}
		var seen int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM running_seen WHERE source='work' AND item_id=? AND person=?", card.Task.ID+":"+strconv.Itoa(generation), person).Scan(&seen); err != nil {
			return nil, err
		}
		units[i].Seen = seen > 0
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM work_ledger WHERE task_id=?", card.Task.ID).Scan(&units[i].Updated); err != nil {
			return nil, err
		}
	}
	return units, nil
}
