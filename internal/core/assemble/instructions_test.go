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
		var names []string
		if err := json.Unmarshal(block("enabled_tools"), &names); err != nil || names == nil {
			t.Fatalf("enabled tools must be an explicit array: %v", err)
		}
		want := make([]string, 0, len(tools))
		for _, raw := range tools {
			var declaration struct{ Name string }
			if err := json.Unmarshal(raw, &declaration); err != nil {
				t.Fatal(err)
			}
			want = append(want, declaration.Name)
		}
		if !reflect.DeepEqual(names, want) {
			t.Fatalf("advertised names differ from native declarations: %v want %v", names, want)
		}
		if strings.Count(got, "</persona>") != 1 {
			t.Fatal("persona field escaped its block")
		}
	}
}
