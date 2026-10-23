// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestDecisionsShareConversationAuthority(t *testing.T) {
	r := controlRuntime(t)
	if r.decisions == nil || r.personaAuthority == nil || r.decisions.Authority != r.personaAuthority {
		t.Fatal("decisions do not use the conversation authority instance")
	}
}

func TestDecisionAgentNamesFollowStore(t *testing.T) {
	r := controlRuntime(t)
	a, err := r.agents.Create(t.Context(), agents.Create{Name: "Cedar", PersonID: r.person, Preset: "lina"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.decisions.AgentName(a.ID); got != a.Name {
		t.Errorf("new agent name=%q, want %q", got, a.Name)
	}
	a, err = r.agents.Rename(t.Context(), a.ID, "Cedar 한글", r.person)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.decisions.AgentName(a.ID); got != a.Name {
		t.Errorf("renamed agent name=%q, want %q", got, a.Name)
	}
	if got := r.decisions.AgentName("missing-agent"); got != "" {
		t.Errorf("unknown agent leaked a routing identifier: %q", got)
	}
}
