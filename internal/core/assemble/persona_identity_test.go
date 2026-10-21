// SPDX-License-Identifier: Apache-2.0
package assemble_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestPreparedIdentityProjectsPersonaOnceAndCountsItsBudget(t *testing.T) {
	in := assemble.Input{Core: "fixture rules", AgentID: "A", AgentName: "Fixture", OwnerPersonID: "owner", PersonaRevision: "revision", Persona: agents.Layers{Core: "unique fixture core", UserVoice: "unique fixture user voice"}, Model: "fixture", MaxTokens: 10000, Current: json.RawMessage(`{"role":"user","content":"input"}`), AlwaysTools: []provider.ToolDecl{json.RawMessage(`{"type":"function","name":"change_persona"}`)}}
	count := func(r provider.Request) int {
		n := len(r.Instructions)
		for _, items := range [][]provider.Item{r.Items, r.Tools} {
			for _, raw := range items {
				n += len(raw)
			}
		}
		return n
	}
	r, report := assemble.Assemble(in, count)
	if report.Failure != "" {
		t.Fatal(report.Failure)
	}
	all := r.Instructions
	for _, item := range r.Items {
		all += string(item)
	}
	for _, layer := range []string{in.Persona.Core, in.Persona.UserVoice} {
		if strings.Count(all, layer) != 1 {
			t.Fatalf("persona layer injected more than once: %q", layer)
		}
	}
	if !strings.Contains(r.Instructions, `"name":"Fixture"`) || !strings.Contains(r.Instructions, `"owner_person_id":"owner"`) {
		t.Fatal("prepared identity was lost")
	}
	in.MaxTokens = report.Tokens - 1
	if _, report := assemble.Assemble(in, count); report.Failure != assemble.TooLarge {
		t.Fatalf("identity escaped request budget: %+v", report)
	}
	in.OwnerPersonID = ""
	if _, report := assemble.Assemble(in, count); report.Failure != assemble.Invalid {
		t.Fatalf("incomplete identity accepted: %+v", report)
	}
}
