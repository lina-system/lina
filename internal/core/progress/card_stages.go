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
	for _, g := range goals {
		cache[key{g.ID, g.Agent}] = g.Stages
	}
	for i := range cards {
		c := &cards[i]
		if c.Kind == "goal" || c.Parent == "" {
			continue
		}
		k := key{c.Parent, c.Agent}
		stages, known := cache[k]
		if !known {
			var raw []byte
			err := tx.QueryRowContext(ctx, "SELECT body FROM work_records WHERE kind='goal' AND id=?", c.Parent).Scan(&raw)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				var g goal.Goal
				if err := json.Unmarshal(raw, &g); err != nil {
					return err
				}
				if g.Person == person && g.Agent == c.Agent {
					stages, err = GoalStages(ctx, tx, g.ID, person, design.ConversationLanguage(c.Name, design.English))
					if err != nil {
						return err
					}
				}
			}
			cache[k] = stages
		}
		c.Stages = stages
	}
	return nil
}
