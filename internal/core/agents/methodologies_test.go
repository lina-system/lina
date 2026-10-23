// SPDX-License-Identifier: Apache-2.0
package agents_test

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func TestMethodologiesResolveFromCanonWithoutReplacingPersona(t *testing.T) {
	s, _, _ := setup(t)
	if err := s.UpdatePreset(t.Context(), agents.Preset{ID: "creative", Core: "Synthetic creative character", DefaultVoice: "Synthetic voice"}, "person"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethodologies(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"lina": "to_do", "dev": "development", "knowledge": "knowledge_and_people", "life": "life", "system": "self_maintenance", "creative": "creative", "investing": "advise_only", "legal": "draft_only"}
	if len(agents.Methodologies()) != len(want) {
		t.Fatal("incomplete methodology catalog")
	}
	for _, m := range agents.Methodologies() {
		a := create(t, s, m.Preset)
		if _, err := s.Rename(t.Context(), a.ID, "Name unrelated to role", "person"); err != nil {
			t.Fatal(err)
		}
		method, err := s.Methodology(t.Context(), a.ID)
		if err != nil || method.ID != want[m.Preset] || method.Name == "" || method.Guidance == "" {
			t.Fatalf("methodology: %+v %v", method, err)
		}
		p, err := s.Prepare(t.Context(), "turn-"+m.Preset, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if m.Preset == "creative" && (p.Layers.Core != "Synthetic creative character" || p.Layers.DefaultVoice != "Synthetic voice") {
			t.Fatal("methodology replaced persona")
		}
		if m.Preset != "creative" && p.Layers.Core != "" {
			t.Fatal("methodology leaked into persona layer")
		}
	}
	if err := s.RegisterMethodologies(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(t.Context())
	if err != nil || len(rows) != 8 {
		t.Fatalf("catalog registration changed agents: %+v %v", rows, err)
	}
}
