// SPDX-License-Identifier: Apache-2.0
package runs

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
)

// WorkListSource adapts durable task cards at the running-list layer. Core
// supplies the installation's person; packets and receipts cannot select one.
type WorkListSource struct{ Person string }

func (s WorkListSource) Candidates(ctx context.Context, tx *canon.Tx, person string) ([]string, error) {
	if person == "" || person != s.Person {
		return nil, nil
	}
	return work.ListReferences(ctx, tx)
}
func (s WorkListSource) Read(ctx context.Context, tx *canon.Tx, id string) (ListItem, error) {
	c, err := work.ReadReference(ctx, tx, id)
	if err != nil {
		return ListItem{}, err
	}
	state := c.State
	if c.State == "done" && c.Completion != nil && c.Completion.State == "claimed" {
		state = "ready_for_review"
	}
	if state == "input_needed" {
		state = "needs_input"
	}
	return ListItem{Person: s.Person, Agent: c.Task.AgentID, Summary: c.Task.Title, State: state}, nil
}
