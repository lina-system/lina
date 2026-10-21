// SPDX-License-Identifier: Apache-2.0
package reply

import (
	"github.com/thisisjun786/lina/internal/core/agents"
	"math"
)

// Expression contains presentation hints only. The budget and authorization
// remain Core-owned; this value has no facts, decisions or permission fields.
// Targets are recommendations for generation, never permission to omit facts.
// The existing surface filter retains the full answer if it exceeds the budget.
type Expression struct {
	Kind        string `json:"kind"`
	Wording     string `json:"wording"`
	TargetChars int    `json:"target_chars"`
	TargetLines int    `json:"target_lines"`
	MaxEmoji    int    `json:"max_emoji"`
	Suppressed  bool   `json:"suppressed"`
}

func ShapeEmotion(state agents.EmotionSnapshot, budget Budget) (Expression, error) {
	if budget.Chars <= 0 || budget.Lines <= 0 {
		return Expression{}, ErrConfig
	}
	out := Expression{Kind: "persona_expression", Wording: "plain", TargetChars: budget.Chars, TargetLines: budget.Lines, Suppressed: state.Suppressed}
	if state.Suppressed {
		return out, nil
	}
	for _, a := range []agents.Affect{state.Short, state.Mood, state.Disposition} {
		if math.IsNaN(a.Valence) || math.IsNaN(a.Arousal) || math.Abs(a.Valence) > 1 || math.Abs(a.Arousal) > 1 {
			return Expression{}, ErrConfig
		}
	}
	valence := 0.65*state.Short.Valence + 0.25*state.Mood.Valence + 0.10*state.Disposition.Valence
	arousal := 0.65*state.Short.Arousal + 0.25*state.Mood.Arousal + 0.10*state.Disposition.Arousal
	switch {
	case valence > 0.15:
		out.Wording = "warm"
	case valence < -0.15:
		out.Wording = "reserved"
	}
	if arousal < -0.15 {
		out.TargetChars = max(1, budget.Chars*2/3)
		out.TargetLines = max(1, budget.Lines*2/3)
	}
	if valence > 0.15 && arousal > 0.15 {
		out.MaxEmoji = 1
	}
	return out, nil
}
