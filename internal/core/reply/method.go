// SPDX-License-Identifier: Apache-2.0
package reply

import (
	"encoding/json"

	"github.com/thisisjun786/lina/internal/core/agents"
)

// Shapes is a value snapshot of the speaking agent's effective work method.
// It contains no lengths: the person's presentation budgets remain separate.
type Shapes struct {
	Status     string `json:"status"`
	Comparison string `json:"comparison"`
	Candidates int    `json:"candidates"`
	ThirdParty string `json:"third_party"`
}

func ShapesFrom(method agents.WorkMethodSettings) (Shapes, error) {
	var shapes Shapes
	for key, dst := range map[string]any{
		"reply.status": &shapes.Status, "reply.comparison": &shapes.Comparison,
		"reply.candidates": &shapes.Candidates, "reply.third_party": &shapes.ThirdParty,
	} {
		if err := json.Unmarshal(method.Values[key], dst); err != nil {
			return Shapes{}, ErrConfig
		}
	}
	if !shapes.valid() {
		return Shapes{}, ErrConfig
	}
	return shapes, nil
}

func (s Shapes) valid() bool {
	return s.Status == "verdict-first" && (s.Comparison == "verdict-then-card" || s.Comparison == "verdict-then-table") && s.Candidates > 0 && s.ThirdParty == "recipient-only"
}
