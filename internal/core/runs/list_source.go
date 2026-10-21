// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"encoding/json"
	"github.com/thisisjun786/lina/internal/core/canon"
	"time"
)

// RunListSource projects the authoritative background-run records. Task
// schedulers supply a separate ListSource to the same RunningList.
type RunListSource struct{ AgentName func(string) string }

func (s RunListSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM background_runs WHERE json_extract(record,'$.Origin.Person')=? ORDER BY seq", person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s RunListSource) Read(ctx context.Context, tx *canon.Tx, id string) (ListItem, error) {
	var data []byte
	if err := tx.QueryRowContext(ctx, "SELECT record FROM background_runs WHERE id=?", id).Scan(&data); err != nil {
		return ListItem{}, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return ListItem{}, err
	}
	item := ListItem{Person: r.Origin.Person, Agent: r.Origin.Agent, Summary: r.Brief.Goal, State: string(r.State), Started: time.Unix(0, r.Created)}
	if s.AgentName != nil {
		item.AgentName = s.AgentName(r.Origin.Agent)
	}
	switch r.State {
	case Working, NeedsInput:
	case Landed:
		item.State = string(r.Outcome)
	case Waiting:
		switch r.Reason {
		case "":
			item.Reason = "run_limit"
		case "usage_capped":
			item.Reason = "quota"
		case "needs_sign_in", "not_eligible":
			item.Reason = "person"
		case "budget":
			item.Reason = "budget"
		default:
			return ListItem{}, ErrListItem
		}
	default:
		return ListItem{}, ErrListItem
	}
	return item, nil
}
