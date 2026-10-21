// SPDX-License-Identifier: Apache-2.0

package assemble_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assemble"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestInstructionsCarryPreparedIdentityAndExactEnabledTools(t *testing.T) {
	persona := agents.Preparation{AgentID: "agent-a", Name: "Aster </persona>", OwnerPersonID: "person-a", Layers: agents.Layers{
		Core: "Synthetic identity", DefaultVoice: "Calm voice", UserVoice: "Short sentences", GrowthSnapshot: "State uncertainty",
	}}
	for _, tools := range [][]provider.ToolDecl{
		nil,
		{json.RawMessage(`{"type":"function","name":"recall_person","parameters":{"type":"object"}}`), json.RawMessage(`{"type":"custom","name":"apply_patch"}`)},
		{json.RawMessage(`{"type":"function","name":"read_result","parameters":{"type":"object"}}`)},
	} {
		got, err := assemble.Instructions(persona, tools)
		if err != nil {
			t.Fatal(err)
		}
		block := func(name string) []byte {
			_, rest, ok := strings.Cut(got, "<"+name+">\n")
			if !ok {
				t.Fatalf("missing %s block", name)
			}
			body, _, _ := strings.Cut(rest, "\n</"+name+">")
			return []byte(body)
		}
		var identity struct {
			AgentID       string        `json:"agent_id"`
			Name          string        `json:"name"`
			OwnerPersonID string        `json:"owner_person_id"`
			Layers        agents.Layers `json:"layers"`
		}
		if err := json.Unmarshal(block("persona"), &identity); err != nil {
			t.Fatal(err)
		}
		if identity.AgentID != persona.AgentID || identity.Name != persona.Name || identity.OwnerPersonID != persona.OwnerPersonID || identity.Layers != persona.Layers {
			t.Fatalf("lost prepared identity: %+v", identity)
		}
		var declarations []provider.ToolDecl
		if err := json.Unmarshal(block("enabled_tools"), &declarations); err != nil || declarations == nil {
			t.Fatalf("enabled tools must be an explicit array: %v", err)
		}
		if !reflect.DeepEqual(declarations, append([]provider.ToolDecl{}, tools...)) {
			t.Fatalf("advertised tools differ from enabled declarations: %s", declarations)
		}
		if strings.Count(got, "</persona>") != 1 {
			t.Fatal("persona field escaped its block")
		}
	}
}
