// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
	"github.com/thisisjun786/lina/persona"
)

func TestWorkMethodDispatchDisabled(t *testing.T) {
	r := controlRuntime(t)
	if err := r.agents.UpdatePreset(t.Context(), agents.Preset{ID: "dev", WorkMethod: `{"schema":1,"preset":"dev","handoff":{"owns":["code"]},"workers":{"dispatch":false}}`}, r.person); err != nil {
		t.Fatal(err)
	}
	_, err := r.assignCoding(t.Context(), dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "method-turn"}, "method-disabled", "worker_start", "/fixture", json.RawMessage(`{}`))
	if !errors.Is(err, errDevUnavailable) {
		t.Fatalf("disabled dev must not own a worker task: %v", err)
	}
}

func TestWorkMethodCodeExchange(t *testing.T) {
	r := controlRuntime(t)
	origin := dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "method-turn"}
	a, err := r.assignCoding(t.Context(), origin, "method-exchange", "worker_start", "/fixture", json.RawMessage(`{"task_packet":"Implement the fixture"}`))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = r.assignCoding(t.Context(), origin, a.ID, a.Tool, a.Target, a.Arguments); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := exchange.NewWithMaterials(r.db, r.materials).Log(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].From != r.agent || lines[0].To != a.Owner || lines[0].State != exchange.Delivered {
		t.Fatalf("code handoff has no single delivered exchange: %+v", lines)
	}
}

func TestWorkMethodGoalResultReturnsToRequester(t *testing.T) {
	r := controlRuntime(t)
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "method-request", ConversationID: r.conversation, AgentID: r.agent, PersonID: r.person, Surface: ledger.Surface{Kind: "app"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Create the result"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.assignCoding(t.Context(), dispatch.Origin{Person: r.person, Agent: r.agent, Conversation: r.conversation, Turn: "turn:" + input.ID}, "method-goal", "dispatch_code_goal", "fixture", json.RawMessage(`{"outcome":"Create the result"}`))
	if err != nil {
		t.Fatal(err)
	}
	g := goal.Goal{ID: a.ID, Person: r.person, Agent: a.Owner, Conversation: a.Conversation, Turn: "coding:" + a.ID, Summary: "Create the result", RecordRevision: "fixture", State: goal.GoalBlocked}
	for range 2 {
		if err = r.publishCodeGoals(t.Context(), goal.State{Goals: []goal.Goal{g}}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := r.journal().Get(t.Context(), "goal-result:"+codingID(g.ID, string(g.State), g.RecordRevision))
	if err != nil || result.ConversationID != r.conversation || result.AgentID != r.agent || result.ParentID != input.ID {
		t.Fatalf("goal result lost origin: %+v %v", result, err)
	}
}

func workAgentFixture(t *testing.T, r *conversationRuntime) string {
	t.Helper()
	method, _, err := persona.LoadWorkMethod(persona.Shipped(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err = r.agents.UpdatePreset(t.Context(), agents.Preset{ID: "dev", WorkMethod: method}, r.person); err != nil {
		t.Fatal(err)
	}
	all, err := r.agents.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		if a.Preset == "dev" {
			return a.ID
		}
	}
	t.Fatal("dev fixture missing")
	return ""
}
