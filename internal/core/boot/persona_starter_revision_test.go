// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/persona"
)

// The shipped prose is opaque input. Compare the published layers with the
// current assets, never with frozen words or sentences. Content fidelity is
// recorded separately in evidence/persona-conversion.md.
func TestShippedStarterRevisionPreservesIdentityAndAgentLayers(t *testing.T) {
	for _, starter := range persona.Starters() {
		t.Run(starter.Key, func(t *testing.T) {
			r, _, _, _ := managementFixture(t)
			if err := r.agents.UpdatePreset(t.Context(), agents.Preset{
				ID: starter.Key, Core: "previous core fixture", DefaultVoice: "previous voice fixture",
			}, r.person); err != nil {
				t.Fatal(err)
			}
			a, err := r.agents.Create(t.Context(), agents.Create{Name: starter.Name, PersonID: r.person, Preset: starter.Key})
			if err != nil {
				t.Fatal(err)
			}
			owned := agents.Layers{
				Core: "previous core fixture", DefaultVoice: "previous voice fixture",
				UserVoice: "agent voice fixture", GrowthSnapshot: "agent growth fixture",
			}
			if _, err := r.agents.Revise(t.Context(), a.ID, a.Revision, owned, r.person); err != nil {
				t.Fatal(err)
			}
			before, err := r.agents.Prepare(t.Context(), "before", a.ID)
			if err != nil {
				t.Fatal(err)
			}
			r.agent = a.ID
			if err := r.refreshPersona(t.Context()); err != nil {
				t.Fatal(err)
			}
			core, voice, err := persona.Load(persona.Shipped(), starter.Key)
			if err != nil {
				t.Fatal(err)
			}
			method, missing, err := persona.LoadWorkMethod(persona.Shipped(), starter.Key)
			if err != nil || missing {
				t.Fatalf("work method unavailable: %v", err)
			}
			after, err := r.agents.Prepare(t.Context(), "after", a.ID)
			want := owned
			want.Core, want.DefaultVoice = core, voice
			if err != nil || after.Layers != want || after.WorkMethod != method || after.Revision != before.Revision+1 || after.Commit == before.Commit {
				t.Fatalf("shipped triple was not published as the next revision: %+v %v", after, err)
			}
			identity, err := r.agents.Get(t.Context(), a.ID)
			if err != nil || identity.Name != starter.Name || identity.Preset != starter.Key {
				t.Fatalf("preset update changed identity: %+v %v", identity, err)
			}
			pinned, err := r.agents.Prepare(t.Context(), "before", a.ID)
			if err != nil || pinned != before {
				t.Fatal("prepared turn changed", err)
			}
			if err := r.refreshPersona(t.Context()); err != nil {
				t.Fatal(err)
			}
			again, err := r.agents.Prepare(t.Context(), "again", a.ID)
			if err != nil || again.Commit != after.Commit || again.Revision != after.Revision {
				t.Fatal("identical shipped triple minted another revision", err)
			}
		})
	}
}
