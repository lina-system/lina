// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/reply"
	"github.com/thisisjun786/lina/kit/loop"
)

func TestConversationToolFamilies(t *testing.T) {
	r, _ := turnRuntime(t)
	if err := r.startSkillMaintenance(t.Context()); err != nil {
		t.Fatal(err)
	}
	event, _ := admitTurnInput(t, r, "family-input")
	const turnID = "turn:family-input"
	persona, err := r.agents.Prepare(t.Context(), turnID, r.agent)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := r.agents.State(t.Context(), r.agent)
	if err != nil {
		t.Fatal(err)
	}
	shapes, err := reply.ShapesFrom(agents.ProjectWorkMethod(persona, settings))
	if err != nil {
		t.Fatal(err)
	}
	input := &toolPreparation{ctx: t.Context(), event: event, persona: persona, settings: settings, at: time.Now().UTC(), replyShapes: shapes, questions: &questionTurn{}}
	for name, prepare := range map[string]toolPreparer{
		"persona":     r.preparePersonaTools,
		"skills":      r.prepareSkillTools,
		"records":     r.prepareRecordTools,
		"results":     r.prepareResultTools,
		"methodology": r.prepareMethodologyTools,
		"planning":    r.preparePlanningTools,
		"runs":        r.prepareRunTools,
		"dispatch":    r.prepareDispatchTools,
		"work":        r.prepareWorkTools,
		"workers":     r.prepareWorkerTools,
		"timers":      r.prepareTimerTools,
		"exchanges":   r.prepareExchangeTools,
	} {
		t.Run(name, func(t *testing.T) {
			bindings := map[string]loop.Tool{}
			declarations, err := prepare(turnID, input, bindings)
			if err != nil {
				t.Fatal(err)
			}
			if len(declarations) == 0 || len(declarations) != len(bindings) {
				t.Fatalf("declarations=%d bindings=%d", len(declarations), len(bindings))
			}
			seen := map[string]bool{}
			for _, raw := range declarations {
				var declaration struct{ Name string }
				if err := json.Unmarshal(raw, &declaration); err != nil {
					t.Fatal(err)
				}
				tool, ok := bindings[declaration.Name]
				if !ok || tool.Validate == nil || tool.Execute == nil || seen[declaration.Name] {
					t.Fatalf("declaration %q has no unique executable binding", declaration.Name)
				}
				seen[declaration.Name] = true
			}
		})
	}
	if len(input.catalog) == 0 || len(input.packet) == 0 {
		t.Fatal("persona preparation lost its catalog or context")
	}
}
