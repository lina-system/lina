// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
)

// ListSource serves the installation's owner. Its person is supplied by Core,
// not by a task packet, receipt or client payload.
type ListSource struct{ Person string }

func generation(c Card) int {
	if c.Run == nil {
		return 0
	}
	return c.Run.Generation
}
func listID(c Card) string { return c.Task.ID + ":" + strconv.Itoa(generation(c)) }
func splitListID(id string) (string, int, error) {
	i := strings.LastIndexByte(id, ':')
	if i <= 0 {
		return "", 0, ErrInvalid
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n < 0 || strconv.Itoa(n) != id[i+1:] {
		return "", 0, ErrInvalid
	}
	return id[:i], n, nil
}

func (s ListSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	if person == "" || person != s.Person {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT record FROM work_cards ORDER BY task_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var data []byte
		var c Card
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &c); err != nil {
			return nil, err
		}
		ids = append(ids, listID(c))
	}
	return ids, rows.Err()
}
func (s ListSource) Read(ctx context.Context, tx *canon.Tx, id string) (runs.ListItem, error) {
	task, expected, err := splitListID(id)
	if err != nil {
		return runs.ListItem{}, err
	}
	var data []byte
	var c Card
	if err := tx.QueryRowContext(ctx, "SELECT record FROM work_cards WHERE task_id=?", task).Scan(&data); err != nil {
		return runs.ListItem{}, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return runs.ListItem{}, err
	}
	if generation(c) != expected {
		return runs.ListItem{}, sql.ErrNoRows
	}
	state := c.State
	if state == "input_needed" {
		state = "needs_input"
	}
	return runs.ListItem{Person: s.Person, Agent: c.Task.AgentID, Summary: c.Task.Title, State: state}, nil
}

// SummaryPage gives the existing running-result surface bounded, source-labeled
// details without exposing worker transcripts or interpreting paths as files.
func (s *Service) SummaryPage(ctx context.Context, id string, offset int64) (string, int64, error) {
	task, expected, err := splitListID(id)
	if err != nil {
		return "", 0, err
	}
	c, err := s.Get(ctx, task)
	if err != nil {
		return "", 0, err
	}
	if generation(c) != expected {
		return "", 0, ErrState
	}
	var b strings.Builder
	b.WriteString(c.State + ": " + c.Task.Title + "\n")
	for _, stage := range c.Stages {
		b.WriteString(stage.Name + " (" + stage.Source + "): " + stage.Summary + "\n")
	}
	chars := []rune(b.String())
	if offset < 0 || offset > int64(len(chars)) {
		return "", 0, ErrInvalid
	}
	end := min(offset+4096, int64(len(chars)))
	next := end
	if end == int64(len(chars)) {
		next = 0
	}
	return string(chars[offset:end]), next, nil
}
