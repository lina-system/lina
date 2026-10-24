// SPDX-License-Identifier: Apache-2.0
package reply_test

import (
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/reply"
	"testing"
)

func TestEmotionShapesOnlyPresentationWithinTheExistingBudget(t *testing.T) {
	t.Parallel()
	budget := reply.ChatBudget()
	warm := agents.EmotionSnapshot{Short: agents.Affect{Valence: 1, Arousal: 1}}
	subdued := agents.EmotionSnapshot{Short: agents.Affect{Valence: -1, Arousal: -1}}
	first, err := reply.ShapeEmotion(warm, budget)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reply.ShapeEmotion(subdued, budget)
	if err != nil {
		t.Fatal(err)
	}
	if first.Wording == second.Wording || first.TargetChars == second.TargetChars || first.MaxEmoji == second.MaxEmoji {
		t.Fatalf("shapes identical: %+v %+v", first, second)
	}
	for _, shape := range []reply.Expression{first, second} {
		if shape.TargetChars > budget.Chars || shape.TargetLines > budget.Lines || shape.TargetChars <= 0 || shape.MaxEmoji > 1 {
			t.Fatalf("budget expanded: %+v", shape)
		}
	}
	warm.Suppressed = true
	off, err := reply.ShapeEmotion(warm, budget)
	if err != nil || off.Wording != "plain" || off.MaxEmoji != 0 || !off.Suppressed {
		t.Fatalf("suppression: %+v %v", off, err)
	}
	tiny, err := reply.ShapeEmotion(subdued, reply.Budget{Chars: 1, Lines: 1})
	if err != nil || tiny.TargetChars != 1 || tiny.TargetLines != 1 {
		t.Fatalf("tiny budget: %+v %v", tiny, err)
	}
	if _, err := reply.ShapeEmotion(warm, reply.Budget{}); err == nil {
		t.Fatal("invalid budget accepted")
	}
}
