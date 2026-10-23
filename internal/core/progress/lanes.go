// SPDX-License-Identifier: Apache-2.0
// Package progress projects Core's recorded worker sessions for developer views.
package progress

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

const PageSize = 32

// Lanes reads a bounded page in one snapshot. Agent names are supplied by Core
// from the active agent store; deleted agents and another person's work stay out.
// Empty person ids are legacy records belonging to this single-owner install.
func Lanes(ctx context.Context, db *canon.Store, person, after string, agents map[string]string) (protocol.Progress, error) {
	lanes := []protocol.WorkerLane{}
	var next *string
	err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT task_id, record FROM work_cards WHERE task_id > ? ORDER BY task_id", after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var raw []byte
			var c work.Card
			if err = rows.Scan(&id, &raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &c); err != nil {
				return err
			}
			name, visible := agents[c.Task.AgentID]
			if !visible || c.Run == nil || c.Task.PersonID != "" && c.Task.PersonID != person {
				continue
			}
			if len(lanes) == PageSize {
				cursor := lanes[len(lanes)-1].TaskID
				next = &cursor
				break
			}
			r := c.Run
			details := []string{}
			if c.Completion != nil {
				details = append(details, c.Completion.Label)
			}
			for _, stage := range c.Stages {
				details = append(details, stage.Name+" ("+stage.Source+"): "+stage.Summary)
			}
			state := c.State
			if c.Completion != nil && c.Completion.State == "claimed" {
				state = "claimed"
			}
			lanes = append(lanes, protocol.WorkerLane{LaneID: r.ID, TaskID: c.Task.ID, AgentID: c.Task.AgentID, AgentName: bounded(name), Worker: bounded(r.Worker), ThreadID: bounded(r.ThreadID), TurnID: bounded(r.TurnID), NodeID: bounded(r.NodeID), State: state, Title: bounded(c.Task.Title), Generation: strconv.Itoa(r.Generation), Details: details})
			for i := range lanes[len(lanes)-1].Details {
				lanes[len(lanes)-1].Details[i] = bounded(lanes[len(lanes)-1].Details[i])
			}
		}
		return rows.Err()
	})
	return protocol.Progress{Lanes: &lanes, NextCursor: next}, err
}

// Bound display text before JSON encoding, including worst-case escaping. IDs
// used for pagination and identity stay exact and are not interpreted as paths.
func bounded(s string) string {
	r := []rune(s)
	if len(r) > 512 {
		return string(r[:511]) + "…"
	}
	return s
}
