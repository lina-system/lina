// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"bytes"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/persona"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestStartupPublishesEveryShippedPreset(t *testing.T) {
	r, _, _, _ := managementFixture(t)
	if err := r.refreshPersona(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"system", "lina", "dev", "knowledge", "life", "creative", "investing", "legal"} {
		a, err := r.agents.Create(t.Context(), agents.Create{Name: "fixture", PersonID: r.person, Preset: key})
		if err != nil {
			t.Errorf("preset %s is unavailable: %v", key, err)
			continue
		}
		p, err := r.agents.Prepare(t.Context(), "fixture:"+key, a.ID)
		if err != nil || p.Layers.Core == "" || p.Layers.DefaultVoice == "" {
			t.Errorf("preset %s has no working pair: %v", key, err)
		}
	}
}

func TestPresetRefreshIsIsolatedAtomicAndPreservesHistory(t *testing.T) {
	r, _, a, b := managementFixture(t)
	source := fstest.MapFS{}
	for _, s := range persona.Starters() {
		source[s.Key+"/core-v1.md"] = &fstest.MapFile{Data: []byte(s.Key + " fixture core")}
		source[s.Key+"/default-voice-v1.md"] = &fstest.MapFile{Data: []byte(s.Key + " fixture voice")}
	}
	var warnings bytes.Buffer
	r.presets = newPresetPublisher(source, &warnings)
	if err := r.agents.UpdatePreset(t.Context(), agents.Preset{ID: "lina", Core: "legacy private fixture", DefaultVoice: "legacy voice", BlankSettings: []string{"color"}}, r.person); err != nil {
		t.Fatal(err)
	}
	// Create after declaring blanks, then preserve its filled value on publication.
	filled, err := r.agents.Create(t.Context(), agents.Create{Name: "filled", PersonID: r.person, Preset: "lina"})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := r.agents.State(t.Context(), filled.ID)
	if _, err := r.agents.FillBlank(t.Context(), filled.ID, agents.StateRevision(state), "color", "blue", "confirmed", r.person); err != nil {
		t.Fatal(err)
	}
	before, err := r.agents.Prepare(t.Context(), "legacy", a)
	if err != nil {
		t.Fatal(err)
	}
	layers := before.Layers
	layers.UserVoice = "user fixture"
	layers.GrowthSnapshot = "growth fixture"
	if _, err := r.agents.Revise(t.Context(), a, before.Revision, layers, r.person); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := r.agents.Delete(t.Context(), b, r.person, now); err != nil {
		t.Fatal(err)
	}
	if err := r.refreshPersona(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := r.agents.Restore(t.Context(), b, r.person, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a, b, filled.ID} {
		p, err := r.agents.Prepare(t.Context(), "published:"+id, id)
		if err != nil || p.Layers.Core != "lina fixture core" || p.Layers.DefaultVoice != "lina fixture voice" {
			t.Fatalf("publication missing: %+v %v", p, err)
		}
		if id == a && (p.Layers.UserVoice != layers.UserVoice || p.Layers.GrowthSnapshot != layers.GrowthSnapshot) {
			t.Fatal("agent layers lost")
		}
	}
	after, err := r.agents.State(t.Context(), filled.ID)
	if err != nil || after.Settings["color"] != "blue" || after.SettingSources["color"] != "confirmed" {
		t.Fatal("filled setting lost", err)
	}
	fresh, err := r.agents.Create(t.Context(), agents.Create{Name: "fresh", PersonID: r.person, Preset: "lina"})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := r.agents.State(t.Context(), fresh.ID)
	if _, ok := st.Settings["color"]; !ok {
		t.Fatal("preset blank definitions lost")
	}
	pinned, err := r.agents.Prepare(t.Context(), "legacy", a)
	if err != nil || pinned != before {
		t.Fatal("historical preparation changed", err)
	}
	other, err := r.agents.Create(t.Context(), agents.Create{Name: "Sion", PersonID: r.person, Preset: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	otherBefore, _ := r.agents.Prepare(t.Context(), "other-before", other.ID)
	current, _ := r.agents.Get(t.Context(), a)
	r.agent = a
	if err := r.refreshPersona(t.Context()); err != nil {
		t.Fatal(err)
	}
	same, _ := r.agents.Get(t.Context(), a)
	if same.Revision != current.Revision {
		t.Fatal("identical refresh minted revision")
	}
	// Failed publication must leave both catalog and every agent at the old pair.
	source["lina/core-v1.md"].Data = []byte("changed fixture")
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), `CREATE TRIGGER refuse_preset BEFORE INSERT ON agent_revisions BEGIN SELECT RAISE(ABORT,'fixture'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.refreshPersona(t.Context()); err == nil {
		t.Fatal("failed publication accepted")
	}
	for _, id := range []string{a, b, filled.ID, fresh.ID} {
		p, err := r.agents.Prepare(t.Context(), "failed:"+id, id)
		if err != nil || p.Layers.Core != "lina fixture core" {
			t.Fatal("partial publication", err)
		}
	}
	if err := r.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "DROP TRIGGER refuse_preset")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.refreshPersona(t.Context()); err != nil {
		t.Fatal(err)
	}
	changed, err := r.agents.Prepare(t.Context(), "changed", a)
	if err != nil || changed.Layers.Core != "changed fixture" {
		t.Fatal("next turn not refreshed", err)
	}
	delete(source, "lina/default-voice-v1.md")
	for range 3 {
		if err := r.refreshPersona(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	empty, err := r.agents.Prepare(t.Context(), "empty", a)
	if err != nil || empty.Layers.Core != "" || empty.Layers.DefaultVoice != "" || empty.Layers.UserVoice != layers.UserVoice || empty.Layers.GrowthSnapshot != layers.GrowthSnapshot {
		t.Fatalf("invalid fallback: %+v %v", empty, err)
	}
	otherAfter, _ := r.agents.Prepare(t.Context(), "other-after", other.ID)
	if otherAfter.Commit != otherBefore.Commit || otherAfter.Layers != otherBefore.Layers {
		t.Fatal("cross-preset change")
	}
	if strings.Count(warnings.String(), persona.SetupWarning("lina")) != 1 {
		t.Fatal("warnings not bounded", warnings.String())
	}
	if strings.Contains(warnings.String(), "fixture") || strings.Contains(warnings.String(), r.root) {
		t.Fatal("private warning")
	}
	source["lina/default-voice-v1.md"] = &fstest.MapFile{Data: []byte("recovered voice")}
	if err := r.refreshPersona(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered, err := r.agents.Prepare(t.Context(), "recovered", a)
	if err != nil || recovered.Layers.Core != "changed fixture" || recovered.Layers.DefaultVoice != "recovered voice" {
		t.Fatal("pair did not recover", err)
	}
}

func TestStartupWarningsArePerPresetAcrossConversationViews(t *testing.T) {
	r, _, a, b := managementFixture(t)
	source := fstest.MapFS{}
	for _, s := range persona.Starters() {
		source[s.Key+"/core-v1.md"] = &fstest.MapFile{Data: []byte(s.Key + " core")}
		source[s.Key+"/default-voice-v1.md"] = &fstest.MapFile{Data: []byte(s.Key + " voice")}
	}
	delete(source, "lina/core-v1.md")
	source["dev/default-voice-v1.md"].Data = []byte{0xff}
	var warnings bytes.Buffer
	r.presets = newPresetPublisher(source, &warnings)
	if err := r.refreshPersona(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.materials = &conversationMaterials{}
	for _, id := range []string{a, b} {
		view := newConversationView(r, id, "main:"+id)
		for range 2 {
			if err := view.refreshPersona(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if warnings.String() != persona.SetupWarning("lina")+"\n"+persona.SetupWarning("dev")+"\n" {
		t.Fatal("warnings not shared per preset", warnings.String())
	}
	for _, s := range persona.Starters() {
		agent, err := r.agents.Create(t.Context(), agents.Create{Name: s.Name, PersonID: r.person, Preset: s.Key})
		if err != nil {
			t.Fatal(err)
		}
		p, err := r.agents.Prepare(t.Context(), "check:"+s.Key, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		invalid := s.Key == "lina" || s.Key == "dev"
		if invalid && (p.Layers.Core != "" || p.Layers.DefaultVoice != "") || !invalid && (p.Layers.Core != s.Key+" core" || p.Layers.DefaultVoice != s.Key+" voice") {
			t.Fatal("invalid isolation", s.Key)
		}
	}
}
