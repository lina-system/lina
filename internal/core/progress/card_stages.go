// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/design"
)

// AttachCardStages uses the card's canonical goal link. Reuse the goal's
// projection in this snapshot; only closed or missing goals need another read.
// An id match alone cannot attach another agent's graph to a work card.
func AttachCardStages(ctx context.Context, tx *canon.Tx, person string, cards, goals []Unit) error {
	type key struct{ goal, agent string }
	cache := map[key][]Stage{}
	parents := map[key]*Unit{}
	for i := range goals {
		g := &goals[i]
		k := key{g.ID, g.Agent}
		cache[k] = g.Stages
		parents[k] = g
	}
	for i := range cards {
		c := &cards[i]
		parent, agent := c.Parent, c.Agent
		if c.StageGoal != "" {
			parent, agent = c.StageGoal, c.StageAgent
		}
		if c.Kind == "goal" || parent == "" {
			continue
		}
		k := key{parent, agent}
		stages, known := cache[k]
		if !known {
			var raw []byte
			err := tx.QueryRowContext(ctx, "SELECT body FROM work_records WHERE kind='goal' AND id=?", parent).Scan(&raw)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				var g goal.Goal
				if err := json.Unmarshal(raw, &g); err != nil {
					return err
				}
				if g.Person == person && g.Agent == agent {
					stages, err = GoalStages(ctx, tx, g.ID, person, design.ConversationLanguage(c.Name, design.English))
					if err != nil {
						return err
					}
				}
			}
			cache[k] = stages
		}
		// An explicit child work item makes this a goal heading. A card for
		// one of the goal's own steps stays a row of its managed project.
		if g := parents[key{c.Parent, c.Agent}]; g != nil {
			stepCard := false
			for _, stage := range g.Stages {
				for _, step := range stage.Steps {
					stepCard = stepCard || step.ID == c.ID
				}
			}
			if !stepCard {
				g.Kind = "goal"
			}
		}
		c.Stages = stages
	}
	return nil
}
